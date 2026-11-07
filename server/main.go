package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type config struct {
	listen         string
	modelPath      string
	llamaServerBin string
	nCtx           int
	threads        int
	maxTokens      int
	apiKey         string
	charLimit      int
	queueDepth     int
	requestTimeout time.Duration
	startTimeout   time.Duration
	// llamaExtra holds extra arguments forwarded verbatim to llama-server
	// (--llama-server-extra), for tuning.
	llamaExtra []string
}

func main() {
	cfg := &config{}
	flag.StringVar(&cfg.listen, "listen", "127.0.0.1:5000", "address to listen on")
	flag.StringVar(&cfg.modelPath, "model", "models/plamo-3-translate-0.8b-Q2_K.gguf", "path to the GGUF model file")
	flag.StringVar(&cfg.llamaServerBin, "llama-server-bin", "", "path to llama-server (default: $LLAMA_SERVER_BIN or PATH)")
	flag.IntVar(&cfg.nCtx, "n-ctx", 4096, "llama-server context window in tokens")
	flag.IntVar(&cfg.threads, "threads", 0, "CPU threads for llama-server (0 = half of logical CPUs, usually the physical core count)")
	flag.IntVar(&cfg.maxTokens, "max-tokens", 512, "base generation limit; grows with input length")
	flag.StringVar(&cfg.apiKey, "api-key", "", "require this API key for /translate and /detect (empty = open)")
	// 1500 matches the longest text Mastodon submits for translation:
	// media attachment descriptions (MediaAttachment::MAX_DESCRIPTION_LENGTH);
	// post bodies are capped at 500 by StatusLengthValidator.
	flag.IntVar(&cfg.charLimit, "char-limit", 1500, "per-text character limit (<=0 = unlimited)")
	flag.IntVar(&cfg.queueDepth, "queue-depth", 32, "completions to queue while one runs; excess requests get 503")
	flag.DurationVar(&cfg.requestTimeout, "request-timeout", 10*time.Minute, "per-request translation timeout")
	flag.DurationVar(&cfg.startTimeout, "start-timeout", 5*time.Minute, "llama-server startup timeout")
	flag.Func("llama-server-extra", "extra argument forwarded to llama-server (repeatable, e.g. --llama-server-extra=-fa)",
		func(s string) error { cfg.llamaExtra = append(cfg.llamaExtra, s); return nil })
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	llama, err := startLlamaServer(ctx, cfg)
	if err != nil {
		log.Fatalf("error: %v", err)
	}
	defer llama.stop()
	log.Printf("llama-server ready (model=%s, queue-depth=%d)", cfg.modelPath, cfg.queueDepth)

	s := &server{cfg: cfg, llama: llama}

	httpSrv := &http.Server{
		Addr:    cfg.listen,
		Handler: cors(s.routes()),
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	log.Printf("listening on http://%s (LibreTranslate-compatible API)", cfg.listen)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("error: %v", err)
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = os.Stdout.Sync()
}
