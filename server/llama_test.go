package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
