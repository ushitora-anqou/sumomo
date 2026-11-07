package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
)

// plamoModel is the model identifier understood by llama-server.
const plamoModel = "plamo-3-translate-0.8b"

// llamaServer manages a llama-server child process that hosts the
// plamo-3-translate GGUF model and answers OpenAI-compatible
// /v1/chat/completions requests. The model is loaded once at startup and
// kept resident; the chat template embedded in the official GGUF converts
// the [plamo-translate] system block into the PFML3 prompt.
type llamaServer struct {
	// ai talks to llama-server's OpenAI-compatible API (official SDK).
	ai openai.Client
	// http is a plain client for the /health probe.
	http    *http.Client
	baseURL string
	cmd     *exec.Cmd
	logPath string
	// jobs feeds a single worker goroutine that runs completions strictly
	// one at a time (llama-server has a single model slot). The channel is
	// a bounded FIFO queue: requests that arrive while it is full fail
	// fast with 503 instead of piling up, which keeps concurrent load from
	// consuming unbounded server resources.
	jobs chan *completionJob
}

// completionJob is one translation request waiting for the worker.
type completionJob struct {
	ctx context.Context
	req openai.ChatCompletionNewParams
	// done receives exactly one result; it is buffered so the worker never
	// blocks on a caller that went away.
	done chan completionResult
}

type completionResult struct {
	content string
	err     error
}

// errServerBusy is returned when the pending-work queue is full. It maps
// to HTTP 503 so clients retry later instead of the server accumulating
// waiters.
var errServerBusy = &apiError{
	status: http.StatusServiceUnavailable,
	msg:    "Translation server is busy, please try again later",
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// startLlamaServer launches llama-server and waits until /health reports ok.
// A missing model file is downloaded automatically when its name matches an
// official release.
func startLlamaServer(ctx context.Context, cfg *config) (*llamaServer, error) {
	if cfg.queueDepth < 1 {
		cfg.queueDepth = 32
	}
	bin := cfg.llamaServerBin
	if bin == "" {
		if env := os.Getenv("LLAMA_SERVER_BIN"); env != "" {
			bin = env
		} else if found, err := exec.LookPath("llama-server"); err == nil {
			bin = found
		} else {
			return nil, errors.New("llama-server not found: pass --llama-server-bin, set LLAMA_SERVER_BIN, or add it to PATH")
		}
	}
	if err := ensureModel(ctx, cfg.modelPath); err != nil {
		return nil, err
	}

	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("could not allocate a port: %w", err)
	}
	logFile, err := os.CreateTemp("", "llama-server-*.log")
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(bin, llamaServerArgs(cfg, port)...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		_ = os.Remove(logFile.Name())
		return nil, fmt.Errorf("failed to start llama-server: %w", err)
	}

	ls := &llamaServer{
		ai:      openai.NewClient(option.WithBaseURL(fmt.Sprintf("http://127.0.0.1:%d/v1", port))),
		http:    &http.Client{},
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		cmd:     cmd,
		logPath: logFile.Name(),
		jobs:    make(chan *completionJob, cfg.queueDepth),
	}
	go ls.work()

	deadline := time.Now().Add(cfg.startTimeout)
	for time.Now().Before(deadline) {
		if cmd.ProcessState != nil {
			ls.stop()
			return nil, fmt.Errorf("llama-server exited early (code %d):\n%s", cmd.ProcessState.ExitCode(), logTail(ls.logPath, 15))
		}
		resp, err := ls.http.Get(ls.baseURL + "/health")
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			var h struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(body, &h) == nil && h.Status == "ok" {
				return ls, nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	ls.stop()
	return nil, fmt.Errorf("llama-server did not become healthy within %s:\n%s", cfg.startTimeout, logTail(ls.logPath, 15))
}

// stop terminates the child process gracefully.
func (l *llamaServer) stop() {
	if l.cmd.Process != nil {
		_ = l.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = l.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = l.cmd.Process.Kill()
			<-done
		}
	}
	_ = os.Remove(l.logPath)
}

// llamaServerArgs builds the llama-server command line. With --threads 0
// (auto), llama-server would use every hardware thread, but hyper-threads
// add cache contention instead of speed: measured 1.51 s CPU per request
// on a 4-core/8-thread machine versus 0.75 s with half the threads, at the
// same latency (scripts/bench.py). Default to half the logical CPUs,
// which usually equals the physical core count.
func llamaServerArgs(cfg *config, port int) []string {
	threads := cfg.threads
	if threads <= 0 {
		threads = max(runtime.NumCPU()/2, 1)
	}
	return append([]string{
		"-m", cfg.modelPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"-c", strconv.Itoa(cfg.nCtx),
		"-t", strconv.Itoa(threads),
		"--no-webui",
	}, cfg.llamaExtra...)
}

// logTail returns the last n lines of the llama-server log for diagnostics.
func logTail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "(no log available)"
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// buildSystemMessage renders the [plamo-translate] settings block that the
// chat template embedded in the official GGUF understands.
func buildSystemMessage(sourcePlamo, targetPlamo string) string {
	var b strings.Builder
	b.WriteString("[plamo-translate]\n")
	if sourcePlamo != "" {
		fmt.Fprintf(&b, "source_lang=%s\n", sourcePlamo)
	}
	fmt.Fprintf(&b, "target_lang=%s\n", targetPlamo)
	b.WriteString("---")
	return b.String()
}

// work is the single completion worker. It pops jobs from the queue in
// FIFO order and runs them strictly one at a time, so llama-server's
// single model slot is never contended and at most one completion is in
// flight. Jobs whose client went away while queued are skipped without
// spending model time on them.
func (l *llamaServer) work() {
	for job := range l.jobs {
		if err := job.ctx.Err(); err != nil {
			job.done <- completionResult{err: err}
			continue
		}
		content, err := l.requestCompletion(job.ctx, job.req)
		job.done <- completionResult{content: content, err: err}
	}
}

// complete queues one translation request and waits for the worker to run
// it. An empty sourcePlamo lets the model auto-detect the source language.
// Already-cancelled callers return immediately and a full queue fails fast
// with errServerBusy instead of waiting.
func (l *llamaServer) complete(ctx context.Context, text, sourcePlamo, targetPlamo string, temperature float64, maxTokens int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	job := &completionJob{
		ctx: ctx,
		// Temperature is sent explicitly (param.NewOpt) so llama-server
		// never applies its own default: primary translations run at 0.
		req: openai.ChatCompletionNewParams{
			Model: plamoModel,
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.SystemMessage(buildSystemMessage(sourcePlamo, targetPlamo)),
				openai.UserMessage(text),
			},
			Temperature: param.NewOpt(temperature),
			MaxTokens:   param.NewOpt(int64(maxTokens)),
			Stop: openai.ChatCompletionNewParamsStopUnion{
				OfStringArray: []string{"<|plamo:tag|>", "<|plamo:bos|>"},
			},
		},
		done: make(chan completionResult, 1),
	}
	select {
	case l.jobs <- job:
	default:
		return "", errServerBusy
	}
	select {
	case res := <-job.done:
		return res.content, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// requestCompletion sends one chat completion to llama-server and returns
// the generated text.
func (l *llamaServer) requestCompletion(ctx context.Context, params openai.ChatCompletionNewParams) (string, error) {
	completion, err := l.ai.Chat.Completions.New(ctx, params)
	if err != nil {
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			return "", fmt.Errorf("llama-server error (status %d): %s: %w", apiErr.StatusCode, apiErr.Message, apiErr)
		}
		return "", fmt.Errorf("llama-server request failed: %v\n%s", err, logTail(l.logPath, 10))
	}
	if len(completion.Choices) == 0 {
		return "", errors.New("llama-server returned no choices")
	}
	return strings.TrimSpace(completion.Choices[0].Message.Content), nil
}
