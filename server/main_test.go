package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testConfig returns a config with the same defaults as main, but with
// short timeouts so a stuck request fails the test quickly.
func testConfig() *config {
	return &config{
		maxTokens:      512,
		nCtx:           4096,
		charLimit:      5000,
		requestTimeout: 10 * time.Second,
	}
}

// newMockLlama returns a llamaServer whose /v1/chat/completions endpoint is
// served in-process: each request is decoded as a chatRequest, passed to
// respond, and the returned status/body is sent back as JSON.
func newMockLlama(t *testing.T, respond func(req chatRequest) (int, any)) *llamaServer {
	t.Helper()
	return newMockLlamaWithQueue(t, 32, respond)
}

// newMockLlamaWithQueue is newMockLlama with an explicit pending-job queue
// depth, for testing back-pressure behavior.
func newMockLlamaWithQueue(t *testing.T, queueDepth int, respond func(req chatRequest) (int, any)) *llamaServer {
	t.Helper()
	if queueDepth < 1 {
		queueDepth = 1
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		status, respBody := respond(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(respBody)
	}))
	t.Cleanup(upstream.Close)
	ls := &llamaServer{
		baseURL: upstream.URL,
		client:  upstream.Client(),
		jobs:    make(chan *completionJob, queueDepth),
	}
	go ls.work()
	return ls
}

// chatCompletionResponse builds an OpenAI-style chat completion body.
func chatCompletionResponse(content string) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
	}
}

// newTestServer builds a sumomo server backed by a mock llama-server and
// serves it (with CORS, like main) on an httptest listener.
func newTestServer(t *testing.T, cfg *config, respond func(req chatRequest) (int, any)) *httptest.Server {
	t.Helper()
	if cfg == nil {
		cfg = testConfig()
	}
	s := &server{cfg: cfg, llama: newMockLlama(t, respond)}
	ts := httptest.NewServer(cors(s.routes()))
	t.Cleanup(ts.Close)
	return ts
}

// postJSON sends a POST request with the given body and decodes the JSON
// response into a map.
func postJSON(t *testing.T, url, contentType, body string) (int, http.Header, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, contentType, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, resp.Header, decodeBody(t, resp)
}

// getJSON sends a GET request and decodes the JSON response into a map.
func getJSON(t *testing.T, url string) (int, http.Header, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, resp.Header, decodeBody(t, resp)
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("response is not a JSON object (%s): %v", data, err)
	}
	return v
}
