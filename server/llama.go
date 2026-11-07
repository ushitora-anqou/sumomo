package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// llamaServer manages a llama-server child process that hosts the
// plamo-3-translate GGUF model and answers OpenAI-compatible
// /v1/chat/completions requests. The model is loaded once at startup and
// kept resident; the chat template embedded in the official GGUF converts
// the [plamo-translate] system block into the PFML3 prompt.
type llamaServer struct {
	baseURL string
	client  *http.Client
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
	req chatRequest
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
func startLlamaServer(cfg *config) (*llamaServer, error) {
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
	if _, err := os.Stat(cfg.modelPath); err != nil {
		return nil, fmt.Errorf("model file not found: %s (run `nix run .#download-model` first)", cfg.modelPath)
	}

	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("could not allocate a port: %w", err)
	}
	logFile, err := os.CreateTemp("", "llama-server-*.log")
	if err != nil {
		return nil, err
	}

	args := []string{
		"-m", cfg.modelPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"-c", strconv.Itoa(cfg.nCtx),
		"--no-webui",
	}
	if cfg.threads > 0 {
		args = append(args, "-t", strconv.Itoa(cfg.threads))
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		_ = os.Remove(logFile.Name())
		return nil, fmt.Errorf("failed to start llama-server: %w", err)
	}

	ls := &llamaServer{
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		client:  &http.Client{},
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
		resp, err := ls.client.Get(ls.baseURL + "/health")
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

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
	Stop        []string      `json:"stop"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
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
		req: chatRequest{
			Model: "plamo-3-translate-0.8b",
			Messages: []chatMessage{
				{Role: "system", Content: buildSystemMessage(sourcePlamo, targetPlamo)},
				{Role: "user", Content: text},
			},
			Temperature: temperature,
			MaxTokens:   maxTokens,
			Stop:        []string{"<|plamo:tag|>", "<|plamo:bos|>"},
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
func (l *llamaServer) requestCompletion(ctx context.Context, reqBody chatRequest) (string, error) {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.baseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("llama-server request failed: %v\n%s", err, logTail(l.logPath, 10))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return "", err
	}
	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return "", fmt.Errorf("invalid response from llama-server: %v", err)
	}
	if cr.Error != nil {
		return "", fmt.Errorf("llama-server error: %s", cr.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llama-server returned %d: %s", resp.StatusCode, string(body))
	}
	if len(cr.Choices) == 0 {
		return "", errors.New("llama-server returned no choices")
	}
	return strings.TrimSpace(cr.Choices[0].Message.Content), nil
}
