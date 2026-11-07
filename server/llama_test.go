package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuildSystemMessage(t *testing.T) {
	got := buildSystemMessage("en", "ja")
	want := "[plamo-translate]\nsource_lang=en\ntarget_lang=ja\n---"
	if got != want {
		t.Errorf("buildSystemMessage(en, ja) = %q, want %q", got, want)
	}
	// An empty source language lets the model auto-detect.
	got = buildSystemMessage("", "ja")
	want = "[plamo-translate]\ntarget_lang=ja\n---"
	if got != want {
		t.Errorf("buildSystemMessage(\"\", ja) = %q, want %q", got, want)
	}
}

func TestCompleteSuccess(t *testing.T) {
	var sent chatRequest
	mock := newMockLlama(t, func(req chatRequest) (int, any) {
		sent = req
		return http.StatusOK, chatCompletionResponse("  こんにちは  ")
	})
	got, err := mock.complete(context.Background(), "Hello", "en", "ja", 0.0, 123)
	if err != nil {
		t.Fatalf("complete() error: %v", err)
	}
	if want := "こんにちは"; got != want {
		t.Errorf("complete() = %q, want %q (output must be trimmed)", got, want)
	}
	if len(sent.Messages) != 2 {
		t.Fatalf("request has %d messages, want 2 (system + user)", len(sent.Messages))
	}
	if sent.Messages[0].Role != "system" || sent.Messages[0].Content != buildSystemMessage("en", "ja") {
		t.Errorf("unexpected system message: %+v", sent.Messages[0])
	}
	if sent.Messages[1].Role != "user" || sent.Messages[1].Content != "Hello" {
		t.Errorf("unexpected user message: %+v", sent.Messages[1])
	}
	if sent.MaxTokens != 123 {
		t.Errorf("MaxTokens = %d, want 123", sent.MaxTokens)
	}
}

// TestCompleteForwardsTemperature verifies that the sampling temperature
// travels through to llama-server (used for the alternatives feature).
func TestCompleteForwardsTemperature(t *testing.T) {
	var gotTemp float64
	mock := newMockLlama(t, func(req chatRequest) (int, any) {
		gotTemp = req.Temperature
		return http.StatusOK, chatCompletionResponse("x")
	})
	if _, err := mock.complete(context.Background(), "Hello", "en", "ja", 0.7, 64); err != nil {
		t.Fatalf("complete() error: %v", err)
	}
	if gotTemp != 0.7 {
		t.Errorf("temperature = %v, want 0.7", gotTemp)
	}
}

func TestCompleteUpstreamError(t *testing.T) {
	mock := newMockLlama(t, func(req chatRequest) (int, any) {
		return http.StatusOK, map[string]any{
			"error": map[string]any{"message": "model overloaded"},
		}
	})
	if _, err := mock.complete(context.Background(), "Hello", "en", "ja", 0, 64); err == nil {
		t.Fatal("complete() succeeded, want an error for an upstream error body")
	} else if !strings.Contains(err.Error(), "model overloaded") {
		t.Errorf("error = %v, want it to mention the upstream message", err)
	}
}

func TestCompleteHTTPError(t *testing.T) {
	mock := newMockLlama(t, func(req chatRequest) (int, any) {
		return http.StatusInternalServerError, map[string]any{}
	})
	_, err := mock.complete(context.Background(), "Hello", "en", "ja", 0, 64)
	if err == nil {
		t.Fatal("complete() succeeded, want an error for a 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %v, want it to mention the status code", err)
	}
}

func TestCompleteNoChoices(t *testing.T) {
	mock := newMockLlama(t, func(req chatRequest) (int, any) {
		return http.StatusOK, map[string]any{"choices": []any{}}
	})
	if _, err := mock.complete(context.Background(), "Hello", "en", "ja", 0, 64); err == nil {
		t.Fatal("complete() succeeded, want an error when no choices are returned")
	}
}

func TestCompleteCancelledContext(t *testing.T) {
	mock := newMockLlama(t, func(req chatRequest) (int, any) {
		return http.StatusOK, chatCompletionResponse("x")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mock.complete(ctx, "Hello", "en", "ja", 0, 64); err == nil {
		t.Fatal("complete() succeeded, want an error for a cancelled context")
	}
}

// TestCompleteSerialized verifies that concurrent completions are executed
// strictly one at a time by the queue worker.
func TestCompleteSerialized(t *testing.T) {
	var running, maxRunning int32
	mock := newMockLlama(t, func(req chatRequest) (int, any) {
		n := atomic.AddInt32(&running, 1)
		for {
			m := atomic.LoadInt32(&maxRunning)
			if n <= m || atomic.CompareAndSwapInt32(&maxRunning, m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&running, -1)
		return http.StatusOK, chatCompletionResponse("ok")
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := mock.complete(context.Background(), "Hello", "en", "ja", 0, 8); err != nil {
				t.Errorf("complete() error: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&maxRunning); got != 1 {
		t.Errorf("max concurrent completions = %d, want 1", got)
	}
}

// closeOnCleanup ensures block is closed exactly once, even when the test
// fails before reaching its own close, so handlers blocked on it cannot
// hang httptest.Server.Close during cleanup.
func closeOnCleanup(t *testing.T, block chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		select {
		case <-block: // already closed
		default:
			close(block)
		}
	})
}

// TestCompleteQueueFullFailsFast verifies that a full pending queue rejects
// the next completion immediately with the 503 errServerBusy.
func TestCompleteQueueFullFailsFast(t *testing.T) {
	block := make(chan struct{})
	closeOnCleanup(t, block)
	started := make(chan struct{})
	var once sync.Once
	mock := newMockLlamaWithQueue(t, 1, func(req chatRequest) (int, any) {
		once.Do(func() { close(started) })
		<-block // the first job occupies the worker
		return http.StatusOK, chatCompletionResponse("ok")
	})

	// First job: picked up by the worker.
	firstDone := make(chan error, 1)
	go func() {
		_, err := mock.complete(context.Background(), "first", "en", "ja", 0, 8)
		firstDone <- err
	}()
	<-started
	// Second job: fills the queue (depth 1).
	secondDone := make(chan error, 1)
	go func() {
		_, err := mock.complete(context.Background(), "second", "en", "ja", 0, 8)
		secondDone <- err
	}()
	waitForQueueLen(t, mock, 1)

	// Third job: the queue is full, so it must fail fast with 503.
	_, err := mock.complete(context.Background(), "third", "en", "ja", 0, 8)
	var ae *apiError
	if !asAPIError(err, &ae) {
		t.Fatalf("error = %v, want errServerBusy", err)
	}
	if ae.status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", ae.status)
	}

	close(block)
	if err := <-firstDone; err != nil {
		t.Errorf("first complete() error: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Errorf("second complete() error: %v", err)
	}
}

// TestCompleteSkipsCancelledJobs verifies that jobs whose client goes away
// while queued are dropped without hitting the model.
func TestCompleteSkipsCancelledJobs(t *testing.T) {
	block := make(chan struct{})
	closeOnCleanup(t, block)
	started := make(chan struct{})
	var once sync.Once
	var calls int32
	mock := newMockLlama(t, func(req chatRequest) (int, any) {
		once.Do(func() { close(started) })
		<-block
		atomic.AddInt32(&calls, 1)
		return http.StatusOK, chatCompletionResponse("ok")
	})

	// First job: occupies the worker.
	firstDone := make(chan error, 1)
	go func() {
		_, err := mock.complete(context.Background(), "first", "en", "ja", 0, 8)
		firstDone <- err
	}()
	<-started
	// Queue a live job, then cancel it while it waits.
	deadCtx, cancel := context.WithCancel(context.Background())
	deadDone := make(chan error, 1)
	go func() {
		_, err := mock.complete(deadCtx, "dead", "en", "ja", 0, 8)
		deadDone <- err
	}()
	waitForQueueLen(t, mock, 1)
	cancel()

	close(block)
	if err := <-firstDone; err != nil {
		t.Errorf("first complete() error: %v", err)
	}
	if err := <-deadDone; err == nil {
		t.Error("cancelled complete() succeeded, want a context error")
	}
	// Only the first job reached the model.
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("model called %d time(s), want 1 (cancelled jobs must be skipped)", n)
	}
}

// waitForQueueLen blocks until the mock's pending queue holds want jobs.
func waitForQueueLen(t *testing.T, mock *llamaServer, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(mock.jobs) < want {
		if time.Now().After(deadline) {
			t.Fatalf("queue length %d, want %d (timed out)", len(mock.jobs), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFreePort(t *testing.T) {
	p, err := freePort()
	if err != nil {
		t.Fatalf("freePort() error: %v", err)
	}
	if p <= 0 || p > 65535 {
		t.Fatalf("freePort() = %d, out of range", p)
	}
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		t.Fatalf("cannot listen on the returned port %d: %v", p, err)
	}
	_ = l.Close()
}

func TestLogTail(t *testing.T) {
	f := filepath.Join(t.TempDir(), "llama.log")
	if err := os.WriteFile(f, []byte("l1\nl2\nl3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := logTail(f, 2); got != "l2\nl3" {
		t.Errorf("logTail(path, 2) = %q, want %q", got, "l2\nl3")
	}
	if got := logTail(f, 10); got != "l1\nl2\nl3" {
		t.Errorf("logTail(path, 10) = %q, want all lines", got)
	}
	if got := logTail(filepath.Join(t.TempDir(), "missing.log"), 5); got != "(no log available)" {
		t.Errorf("logTail(missing) = %q, want a placeholder", got)
	}
}
