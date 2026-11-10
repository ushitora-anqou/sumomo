package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- parseTranslateRequest -------------------------------------------------

func postBody(t *testing.T, contentType, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "/translate", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req
}

func TestParseTranslateRequestJSON(t *testing.T) {
	req := postBody(t, "application/json",
		`{"q":"Hello","source":"en","target":"ja","format":"text","alternatives":2,"api_key":"k"}`)
	got, err := parseTranslateRequest(req)
	if err != nil {
		t.Fatalf("parseTranslateRequest() error: %v", err)
	}
	if !got.single || len(got.texts) != 1 || got.texts[0] != "Hello" {
		t.Errorf("texts = %v (single=%v), want [Hello] (single=true)", got.texts, got.single)
	}
	if got.source != "en" || got.target != "ja" {
		t.Errorf("source/target = %q/%q, want en/ja", got.source, got.target)
	}
	if got.format != "text" || got.alternatives != 2 || got.apiKey != "k" {
		t.Errorf("format/alternatives/apiKey = %q/%d/%q", got.format, got.alternatives, got.apiKey)
	}
}

func TestParseTranslateRequestJSONArray(t *testing.T) {
	req := postBody(t, "application/json", `{"q":["a","b"],"source":"auto","target":"ja"}`)
	got, err := parseTranslateRequest(req)
	if err != nil {
		t.Fatalf("parseTranslateRequest() error: %v", err)
	}
	if got.single {
		t.Error("single = true, want false for an array q")
	}
	if len(got.texts) != 2 || got.texts[0] != "a" || got.texts[1] != "b" {
		t.Errorf("texts = %v, want [a b]", got.texts)
	}
}

func TestParseTranslateRequestForm(t *testing.T) {
	form := url.Values{"q": {"Hi"}, "source": {"en"}, "target": {"ja"}, "api_key": {"k"}, "alternatives": {"3"}}
	req := postBody(t, "application/x-www-form-urlencoded", form.Encode())
	got, err := parseTranslateRequest(req)
	if err != nil {
		t.Fatalf("parseTranslateRequest() error: %v", err)
	}
	if !got.single || len(got.texts) != 1 || got.texts[0] != "Hi" {
		t.Errorf("texts = %v (single=%v), want [Hi] (single=true)", got.texts, got.single)
	}
	if got.source != "en" || got.target != "ja" || got.apiKey != "k" || got.alternatives != 3 {
		t.Errorf("unexpected parse result: %+v", got)
	}
}

func TestParseTranslateRequestErrors(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{"invalid json", "application/json", `{"q":`},
		{"q is a number", "application/json", `{"q":42}`},
		{"q array has a number", "application/json", `{"q":["a",1]}`},
		{"bad alternatives in form", "application/x-www-form-urlencoded", "q=a&alternatives=x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseTranslateRequest(postBody(t, tt.contentType, tt.body))
			var ae *apiError
			if !asAPIError(err, &ae) || ae.status != http.StatusBadRequest {
				t.Fatalf("error = %v, want a 400 apiError", err)
			}
		})
	}
}

func asAPIError(err error, target **apiError) bool {
	if ae, ok := err.(*apiError); ok {
		*target = ae
		return true
	}
	return false
}

// --- API key and token budget ----------------------------------------------

func TestCheckAPIKey(t *testing.T) {
	// No key configured: requests pass through.
	s := &server{cfg: &config{}}
	if ae := s.checkAPIKey(&translateRequest{}); ae != nil {
		t.Errorf("error = %v, want nil when no key is required", ae)
	}

	s = &server{cfg: &config{apiKey: "secret"}}
	ae := s.checkAPIKey(&translateRequest{}) // key configured but none supplied
	if ae == nil || ae.status != http.StatusBadRequest {
		t.Errorf("error = %v, want 400 for a missing key", ae)
	}
	ae = s.checkAPIKey(&translateRequest{apiKey: "wrong"})
	if ae == nil || ae.status != http.StatusForbidden {
		t.Errorf("error = %v, want 403 for a wrong key", ae)
	}
	if ae := s.checkAPIKey(&translateRequest{apiKey: "secret"}); ae != nil {
		t.Errorf("error = %v, want nil for the correct key", ae)
	}
}

func TestMaxTokensFor(t *testing.T) {
	s := &server{cfg: &config{maxTokens: 512, nCtx: 4096}}
	if got := s.maxTokensFor("Hello"); got != 512 {
		t.Errorf("maxTokensFor(short) = %d, want the 512 default", got)
	}
	long := strings.Repeat("x", 1000)
	if got := s.maxTokensFor(long); got != 1264 {
		t.Errorf("maxTokensFor(long) = %d, want 1264 (scales with input)", got)
	}
	tiny := &server{cfg: &config{maxTokens: 512, nCtx: 256}}
	if got := tiny.maxTokensFor("Hi"); got != 32 {
		t.Errorf("maxTokensFor with a tiny context = %d, want the 32 floor", got)
	}
}

// --- endpoints (through the real routing table) ----------------------------

// echoTranslate is a mock llama-server translating to "<target>: <text>".
func echoTranslate(p chatProbe) (int, string) {
	target := strings.TrimSuffix(strings.Split(p.System, "target_lang=")[1], "\n---")
	return http.StatusOK, chatCompletionJSON(target + ": " + p.User)
}

func TestTranslateSingle(t *testing.T) {
	ts := newTestServer(t, nil, echoTranslate)
	status, _, body := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"Hello","source":"en","target":"ja"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %v", status, body)
	}
	if got := body["translatedText"]; got != "ja: Hello" {
		t.Errorf("translatedText = %v, want %q", got, "ja: Hello")
	}
}

func TestTranslateBatch(t *testing.T) {
	ts := newTestServer(t, nil, echoTranslate)
	status, _, body := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":["Hello","Bye"],"source":"en","target":"ja"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %v", status, body)
	}
	got, _ := body["translatedText"].([]any)
	if len(got) != 2 || got[0] != "ja: Hello" || got[1] != "ja: Bye" {
		t.Errorf("translatedText = %v, want [ja: Hello ja: Bye]", got)
	}
}

func TestTranslateAutoDetectsLanguage(t *testing.T) {
	ts := newTestServer(t, nil, echoTranslate)
	_, _, body := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"こんにちは","source":"auto","target":"en"}`)
	det, _ := body["detectedLanguage"].(map[string]any)
	if det == nil {
		t.Fatalf("detectedLanguage missing: %v", body)
	}
	if det["language"] != "ja" {
		t.Errorf("detectedLanguage = %v, want ja", det)
	}
	// The model itself sees no source_lang when source is auto.
}

func TestTranslateAutoDetectInRequest(t *testing.T) {
	var sys string
	ts := newTestServer(t, nil, func(p chatProbe) (int, string) {
		sys = p.System
		return echoTranslate(p)
	})
	postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"Hello","source":"auto","target":"ja"}`)
	if strings.Contains(sys, "source_lang=") {
		t.Errorf("system prompt %q must omit source_lang for auto", sys)
	}
}

func TestTranslateAlternatives(t *testing.T) {
	ts := newTestServer(t, nil, func(p chatProbe) (int, string) {
		if p.Temperature != nil && *p.Temperature == 0 {
			return http.StatusOK, chatCompletionJSON("primary")
		}
		return http.StatusOK, chatCompletionJSON("alt")
	})
	_, _, body := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"Hello","source":"en","target":"ja","alternatives":2}`)
	if body["translatedText"] != "primary" {
		t.Errorf("translatedText = %v, want primary", body["translatedText"])
	}
	alts, _ := body["alternatives"].([]any)
	// Both sampled alternatives are identical, so dedup keeps one.
	if len(alts) != 1 || alts[0] != "alt" {
		t.Errorf("alternatives = %v, want [alt]", alts)
	}
}

func TestTranslateHTMLFormat(t *testing.T) {
	ts := newTestServer(t, nil, echoTranslate)
	_, _, body := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"<p>Hello</p>","source":"en","target":"ja","format":"html"}`)
	// Decoration is stripped before translation: the model sees "Hello"
	// (no tags) and the markup is not restored, the result is plain text.
	if got := body["translatedText"]; got != "ja: Hello" {
		t.Errorf("translatedText = %v, want the flattened text without tags", got)
	}
}

func TestTranslatePassthroughNonTranslatable(t *testing.T) {
	called := false
	ts := newTestServer(t, nil, func(p chatProbe) (int, string) {
		called = true
		return echoTranslate(p)
	})
	_, _, body := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"123-456","source":"en","target":"ja"}`)
	if called {
		t.Error("the model was called for a non-translatable text")
	}
	if got := body["translatedText"]; got != "123-456" {
		t.Errorf("translatedText = %v, want the original text", got)
	}
}

func TestTranslateFormEncoded(t *testing.T) {
	ts := newTestServer(t, nil, echoTranslate)
	form := url.Values{"q": {"Hello"}, "source": {"en"}, "target": {"ja"}}
	status, _, body := postJSON(t, ts.URL+"/translate", "application/x-www-form-urlencoded", form.Encode())
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %v", status, body)
	}
	if got := body["translatedText"]; got != "ja: Hello" {
		t.Errorf("translatedText = %v, want %q", got, "ja: Hello")
	}
}

func TestTranslateUpstreamFailure(t *testing.T) {
	ts := newTestServer(t, nil, func(p chatProbe) (int, string) {
		return http.StatusInternalServerError, chatErrorJSON("boom")
	})
	status, _, body := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"Hello","source":"en","target":"ja"}`)
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", status)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "Cannot translate text") {
		t.Errorf("error = %v, want it to wrap the failure", body["error"])
	}
}

// TestTranslateStreamsOpeningBrace verifies that the first response byte
// (the JSON opening brace) leaves before the translation finishes, so a
// client like Mastodon starts (and keeps alive) its read deadline early.
func TestTranslateStreamsOpeningBrace(t *testing.T) {
	cfg := testConfig()
	cfg.keepAliveInterval = 30 * time.Millisecond
	ts := newTestServer(t, cfg, func(p chatProbe) (int, string) {
		time.Sleep(300 * time.Millisecond)
		return http.StatusOK, chatCompletionJSON("ja: " + p.User)
	})
	resp, err := http.Post(ts.URL+"/translate", "application/json",
		strings.NewReader(`{"q":"Hello","source":"en","target":"ja"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	br := bufio.NewReader(resp.Body)
	t0 := time.Now()
	b, err := br.ReadByte()
	if err != nil {
		t.Fatalf("reading the first byte: %v", err)
	}
	if b != '{' {
		t.Fatalf("first byte = %q, want '{'", b)
	}
	if d := time.Since(t0); d > 150*time.Millisecond {
		t.Errorf("first byte arrived after %v; it must precede the 300ms translation", d)
	}
	// The rest still parses as the complete object.
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(append([]byte{'{'}, rest...), &out); err != nil {
		t.Fatalf("body %q does not parse: %v", rest, err)
	}
	if out["translatedText"] != "ja: Hello" {
		t.Errorf("translatedText = %v", out["translatedText"])
	}
}

// TestTranslateKeepAliveSpaces verifies that slow translations emit
// JSON-legal spaces and that the assembled body still parses.
func TestTranslateKeepAliveSpaces(t *testing.T) {
	cfg := testConfig()
	cfg.keepAliveInterval = 30 * time.Millisecond
	ts := newTestServer(t, cfg, func(p chatProbe) (int, string) {
		time.Sleep(120 * time.Millisecond)
		return http.StatusOK, chatCompletionJSON("ja: " + p.User)
	})
	resp, err := http.Post(ts.URL+"/translate", "application/json",
		strings.NewReader(`{"q":"Hello","source":"en","target":"ja"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("body %q does not parse: %v", raw, err)
	}
	if !strings.Contains(string(raw), " ") {
		t.Errorf("body %q contains no keep-alive spaces; writeJSON normally emits none", raw)
	}
	if out["translatedText"] != "ja: Hello" {
		t.Errorf("translatedText = %v", out["translatedText"])
	}
}

// TestTranslateStreamErrorAfterOpen verifies that a failure occurring after
// the stream has opened still yields a complete JSON error object.
func TestTranslateStreamErrorAfterOpen(t *testing.T) {
	cfg := testConfig()
	cfg.keepAliveInterval = 30 * time.Millisecond
	ts := newTestServer(t, cfg, func(p chatProbe) (int, string) {
		if p.User == "second" {
			return http.StatusServiceUnavailable, chatErrorJSON("boom")
		}
		time.Sleep(60 * time.Millisecond) // let the stream open
		return http.StatusOK, chatCompletionJSON("ok")
	})
	resp, err := http.Post(ts.URL+"/translate", "application/json",
		strings.NewReader(`{"q":["first","second"],"source":"en","target":"ja"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("streamed error body %q does not parse: %v", raw, err)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "boom") {
		t.Errorf("error = %v, want the upstream message", out["error"])
	}
}

// With one job running and a queue depth of 1, the third concurrent
// request must fail fast with 503 instead of queueing.
func TestTranslateQueueFullReturns503(t *testing.T) {
	block := make(chan struct{})
	closeOnCleanup(t, block)
	started := make(chan struct{})
	var once sync.Once
	mock := newMockLlamaWithQueue(t, 1, func(p chatProbe) (int, string) {
		once.Do(func() { close(started) })
		<-block // the first request occupies the worker
		return http.StatusOK, chatCompletionJSON("ja: " + p.User)
	})
	s := &server{cfg: testConfig(), llama: mock}
	ts := httptest.NewServer(cors(s.routes()))
	t.Cleanup(ts.Close)

	// postInBackground issues a translate request and fully drains the
	// response so the test server can shut down cleanly.
	var bg sync.WaitGroup
	postInBackground := func(q string) {
		bg.Add(1)
		go func() {
			defer bg.Done()
			resp, err := http.Post(ts.URL+"/translate", "application/json",
				strings.NewReader(`{"q":"`+q+`","source":"en","target":"ja"}`))
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}

	postInBackground("first")
	<-started
	postInBackground("second")
	deadline := time.Now().Add(5 * time.Second)
	for len(mock.jobs) < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(mock.jobs) < 1 {
		t.Fatal("second request never queued")
	}

	status, _, body := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"third","source":"en","target":"ja"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the queue is full", status)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "busy") {
		t.Errorf("error = %q, want a busy message", msg)
	}
	close(block)
	bg.Wait()
}

func TestTranslateValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"missing q", `{"source":"en","target":"ja"}`, "Invalid request: missing q parameter"},
		{"empty q", `{"q":"","source":"en","target":"ja"}`, "Invalid request: missing q parameter"},
		{"missing source", `{"q":"Hello","target":"ja"}`, "Invalid request: missing source parameter"},
		{"missing target", `{"q":"Hello","source":"en"}`, "Invalid request: missing target parameter"},
		{"bad format", `{"q":"Hello","source":"en","target":"ja","format":"xml"}`, "xml format is not supported"},
		{"unknown source", `{"q":"Hello","source":"xx","target":"ja"}`, "xx is not supported"},
		{"unknown target", `{"q":"Hello","source":"en","target":"xx"}`, "xx is not supported"},
		{"invalid json", `not json`, "Invalid JSON format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTestServer(t, nil, echoTranslate)
			status, _, body := postJSON(t, ts.URL+"/translate", "application/json", tt.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", status)
			}
			if msg, _ := body["error"].(string); msg != tt.want {
				t.Errorf("error = %q, want %q", msg, tt.want)
			}
		})
	}
}

func TestTranslateCharLimit(t *testing.T) {
	cfg := testConfig()
	cfg.charLimit = 5
	ts := newTestServer(t, cfg, echoTranslate)
	status, _, body := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"Hello there","source":"en","target":"ja"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	want := "Invalid request: request (11) exceeds text limit (5)"
	if msg, _ := body["error"].(string); msg != want {
		t.Errorf("error = %q, want %q", msg, want)
	}
}

func TestTranslateCharLimitHTML(t *testing.T) {
	cfg := testConfig()
	cfg.charLimit = 15
	ts := newTestServer(t, cfg, echoTranslate)

	// The raw fragment (17 runes with tags) exceeds the limit, but the
	// flattened text does not: the limit must apply to the stripped text.
	status, _, body := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"<p>abcdefghij</p>","source":"en","target":"ja","format":"html"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %v, want 200 (tags do not count)", status, body)
	}
	if got := body["translatedText"]; got != "ja: abcdefghij" {
		t.Errorf("translatedText = %v, want %q", got, "ja: abcdefghij")
	}

	// Still over the limit once the markup is stripped.
	status, _, body = postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"<p>abcdefghijklmnopqrstuvwx</p>","source":"en","target":"ja","format":"html"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	want := "Invalid request: request (24) exceeds text limit (15)"
	if msg, _ := body["error"].(string); msg != want {
		t.Errorf("error = %q, want %q", msg, want)
	}
}

func TestTranslateRequiresAPIKey(t *testing.T) {
	cfg := testConfig()
	cfg.apiKey = "secret"
	ts := newTestServer(t, cfg, echoTranslate)

	// Missing key.
	status, _, _ := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"Hello","source":"en","target":"ja"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 without a key", status)
	}
	// Wrong key.
	status, _, _ = postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"Hello","source":"en","target":"ja","api_key":"wrong"}`)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a wrong key", status)
	}
	// Correct key.
	status, _, body := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":"Hello","source":"en","target":"ja","api_key":"secret"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %v", status, body)
	}
	if body["translatedText"] != "ja: Hello" {
		t.Errorf("translatedText = %v, want a translation", body["translatedText"])
	}
}

func TestLanguagesEndpoint(t *testing.T) {
	ts := newTestServer(t, nil, echoTranslate)
	resp, err := http.Get(ts.URL + "/languages")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var list []map[string]any
	decodeInto(t, resp, &list)
	if len(list) != len(languages) {
		t.Fatalf("%d languages listed, want %d", len(list), len(languages))
	}
	first := list[0]
	if first["code"] != "ja" || first["name"] != "Japanese" {
		t.Errorf("first language = %v, want Japanese (ja)", first)
	}
	targets, _ := first["targets"].([]any)
	if len(targets) != len(languages) {
		t.Errorf("targets has %d entries, want %d", len(targets), len(languages))
	}
}

func TestLanguagesRequestMethodNotAllowed(t *testing.T) {
	ts := newTestServer(t, nil, echoTranslate)
	resp, err := http.Post(ts.URL+"/languages", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 for POST /languages", resp.StatusCode)
	}
}

func TestHealthEndpoint(t *testing.T) {
	ts := newTestServer(t, nil, echoTranslate)
	status, _, body := getJSON(t, ts.URL+"/health")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %v, want ok", body["status"])
	}
}

func TestFrontendSettingsEndpoint(t *testing.T) {
	cfg := testConfig()
	cfg.charLimit = 42
	ts := newTestServer(t, cfg, echoTranslate)
	status, _, body := getJSON(t, ts.URL+"/frontend/settings")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["charLimit"] != float64(42) {
		t.Errorf("charLimit = %v, want 42", body["charLimit"])
	}
	if body["keyRequired"] != false {
		t.Errorf("keyRequired = %v, want false without an API key", body["keyRequired"])
	}
}

// The HTML UI is gone: the root path must not serve anything.
func TestRootIsNotFound(t *testing.T) {
	ts := newTestServer(t, nil, echoTranslate)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for GET / (no HTML UI)", resp.StatusCode)
	}
}

func TestCORSPreflight(t *testing.T) {
	ts := newTestServer(t, nil, echoTranslate)
	req, err := http.NewRequest(http.MethodOptions, ts.URL+"/translate", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204 for a preflight", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
}

func TestDetectEndpoint(t *testing.T) {
	ts := newTestServer(t, nil, echoTranslate)

	// JSON body.
	resp, err := http.Post(ts.URL+"/detect", "application/json",
		strings.NewReader(`{"q":"こんにちは"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var dets []detection
	decodeInto(t, resp, &dets)
	if len(dets) != 1 || dets[0].Language != "ja" || dets[0].Confidence != 100 {
		t.Errorf("detections = %v, want ja at 100", dets)
	}

	// Form body.
	form := url.Values{"q": {"Привет"}}
	resp, err = http.Post(ts.URL+"/detect", "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	dets = nil
	decodeInto(t, resp, &dets)
	if len(dets) != 1 || dets[0].Language != "ru" {
		t.Errorf("detections = %v, want ru", dets)
	}
}

func TestDetectEndpointAPIKeyAndErrors(t *testing.T) {
	cfg := testConfig()
	cfg.apiKey = "secret"
	ts := newTestServer(t, cfg, echoTranslate)

	// Missing key.
	status, _, _ := postJSON(t, ts.URL+"/detect", "application/json", `{"q":"hi"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 without a key", status)
	}
	// Missing q even with a key.
	status, _, body := postJSON(t, ts.URL+"/detect", "application/json", `{"q":"","api_key":"secret"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a missing q", status)
	}
	if msg, _ := body["error"].(string); msg != "Invalid request: missing q parameter" {
		t.Errorf("error = %q", msg)
	}
}

// decodeInto decodes a JSON response body into v, failing the test on error.
func decodeInto(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
}
