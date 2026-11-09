package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestAccessLogStatuses verifies that accessLog records the status the
// handler wrote, defaults to 200 when the handler never called
// WriteHeader, and that the log line carries method, path, and status.
func TestAccessLogStatuses(t *testing.T) {
	var buf strings.Builder
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	handler := accessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/teapot" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		_, _ = io.WriteString(w, "{}") // implicit 200
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/teapot", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rec.Code)
	}

	logged := buf.String()
	for _, want := range []string{"GET /health 200", "POST /teapot 418"} {
		if !strings.Contains(logged, want) {
			t.Errorf("log %q does not contain %q", logged, want)
		}
	}
}

// TestAccessLogTranslateStreaming verifies that wrapping the full route
// table in accessLog does not break /translate's streaming responses:
// statusRecorder must forward Flush, or jsonStream would panic on a nil
// flusher and the translated body would never arrive.
func TestAccessLogTranslateStreaming(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	cfg := testConfig()
	cfg.keepAliveInterval = 30 * time.Millisecond
	s := &server{cfg: cfg, llama: newMockLlama(t, func(p chatProbe) (int, string) {
		time.Sleep(60 * time.Millisecond) // let the keep-alive flush some spaces
		return http.StatusOK, chatCompletionJSON("ja: " + p.User)
	})}
	ts := httptest.NewServer(accessLog(cors(s.routes())))
	t.Cleanup(ts.Close)

	status, _, body := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"Hello","source":"en","target":"ja"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["translatedText"] != "ja: Hello" {
		t.Errorf("translatedText = %v, want %q", body["translatedText"], "ja: Hello")
	}
}
