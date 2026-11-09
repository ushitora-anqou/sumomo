package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// captureEvents swaps the wide-event logger for one writing JSON lines into
// a buffer, so tests can inspect exactly what would reach the log.
func captureEvents(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	old := eventLog
	eventLog = slog.New(slog.NewJSONHandler(&buf, nil))
	t.Cleanup(func() { eventLog = old })
	return &buf
}

// decodeEvents parses the JSON lines captured by captureEvents, one object
// per request.
func decodeEvents(t *testing.T, buf *strings.Builder) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("event is not valid JSON (%s): %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

// TestAccessLogStatuses verifies that the wide event records the status the
// handler wrote, defaults to 200 when the handler never called WriteHeader,
// and carries the request basics: method, path, status, duration, and
// remote address.
func TestAccessLogStatuses(t *testing.T) {
	buf := captureEvents(t)

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

	events := decodeEvents(t, buf)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (%v)", len(events), events)
	}
	for _, ev := range events {
		if ev["msg"] != "request" {
			t.Errorf("msg = %v, want %q", ev["msg"], "request")
		}
		if _, ok := ev["duration_ms"].(float64); !ok {
			t.Errorf("duration_ms = %v (%T), want a number", ev["duration_ms"], ev["duration_ms"])
		}
		if ev["remote_addr"] == "" {
			t.Errorf("remote_addr is empty in %v", ev)
		}
	}
	first, second := events[0], events[1]
	if first["method"] != http.MethodGet || first["path"] != "/health" || first["status"] != float64(200) {
		t.Errorf("first event = %v, want GET /health 200", first)
	}
	if second["method"] != http.MethodPost || second["path"] != "/teapot" || second["status"] != float64(418) {
		t.Errorf("second event = %v, want POST /teapot 418", second)
	}
}

// TestAccessLogWideEventPayload verifies that the /translate and /detect
// handlers attach the request payload to their wide event: what was sent
// (q, source, target, format, alternatives) appears in the log, while the
// api_key value never does — only whether one was supplied.
func TestAccessLogWideEventPayload(t *testing.T) {
	buf := captureEvents(t)

	cfg := testConfig()
	cfg.apiKey = "s3cret-key"
	s := &server{cfg: cfg, llama: newMockLlama(t, func(p chatProbe) (int, string) {
		return http.StatusOK, chatCompletionJSON("ja: " + p.User)
	})}
	ts := httptest.NewServer(accessLog(cors(s.routes())))
	t.Cleanup(ts.Close)

	// Single text (q as string), all parameters explicit.
	status, _, body := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"Hello","source":"en","target":"ja","format":"text","api_key":"s3cret-key"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}

	// Batch (q as array) with alternatives, source=auto, and no API key:
	// rejected with 400, but the payload must still land in the event.
	status, _, body = postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":["Hello","World"],"source":"auto","target":"ja","alternatives":2}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%v)", status, body)
	}

	// Detect with the API key in the body. Its response is a JSON array,
	// so postJSON (object-only) cannot be used here.
	resp, err := http.Post(ts.URL+"/detect", "application/json",
		strings.NewReader(`{"q":"こんにちは","api_key":"s3cret-key"}`))
	if err != nil {
		t.Fatalf("detect request failed: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detect status = %d, want 200", resp.StatusCode)
	}

	events := decodeEvents(t, buf)
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3 (%v)", len(events), events)
	}

	translate := events[0]
	for key, want := range map[string]any{
		"method":       "POST",
		"path":         "/translate",
		"status":       float64(200),
		"q":            "Hello",
		"source":       "en",
		"target":       "ja",
		"format":       "text",
		"alternatives": float64(0),
		"api_key":      true,
	} {
		if got := translate[key]; got != want {
			t.Errorf("translate event %q = %v, want %v", key, got, want)
		}
	}

	batch := events[1]
	qs, ok := batch["q"].([]any)
	if !ok || len(qs) != 2 || qs[0] != "Hello" || qs[1] != "World" {
		t.Errorf("batch event q = %v, want [Hello World]", batch["q"])
	}
	if batch["status"] != float64(400) {
		t.Errorf("batch event status = %v, want 400", batch["status"])
	}
	if batch["source"] != "auto" || batch["alternatives"] != float64(2) || batch["api_key"] != false {
		t.Errorf("batch event = %v, want source=auto alternatives=2 api_key=false", batch)
	}

	detect := events[2]
	if detect["path"] != "/detect" || detect["q"] != "こんにちは" || detect["api_key"] != true {
		t.Errorf("detect event = %v, want q=こんにちは api_key=true", detect)
	}

	if strings.Contains(buf.String(), "s3cret-key") {
		t.Errorf("log contains the API key value:\n%s", buf.String())
	}
}

// TestAccessLogTranslateStreaming verifies that wrapping the full route
// table in accessLog does not break /translate's streaming responses:
// statusRecorder must forward Flush, or jsonStream would panic on a nil
// flusher and the translated body would never arrive.
func TestAccessLogTranslateStreaming(t *testing.T) {
	captureEvents(t) // discard events; this test only checks streaming

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
