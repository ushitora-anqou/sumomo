package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
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

// chatProbe is the flat view of a chat completion request that tests need:
// the system/user messages plus the sampling parameters as seen on the
// wire (Temperature is nil when the field was omitted).
type chatProbe struct {
	System      string
	User        string
	Temperature *float64
	MaxTokens   int64
}

// probeRequest decodes an OpenAI-compatible chat completion request body.
func probeRequest(body []byte) (chatProbe, error) {
	var raw struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Temperature *float64 `json:"temperature"`
		MaxTokens   int64    `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return chatProbe{}, err
	}
	var p chatProbe
	for _, m := range raw.Messages {
		var content string
		if err := json.Unmarshal(m.Content, &content); err != nil {
			return chatProbe{}, err
		}
		switch m.Role {
		case "system":
			p.System = content
		case "user":
			p.User = content
		}
	}
	p.Temperature = raw.Temperature
	p.MaxTokens = raw.MaxTokens
	return p, nil
}

// chatCompletionJSON builds an OpenAI-style chat completion response body.
func chatCompletionJSON(content string) string {
	return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, content)
}

// chatErrorJSON builds an OpenAI-style error response body.
func chatErrorJSON(message string) string {
	return fmt.Sprintf(`{"error":{"code":null,"message":%q,"param":null,"type":null}}`, message)
}

// newMockLlama returns a llamaServer whose /v1/chat/completions endpoint is
// served in-process: each request is probed and passed to respond, whose
// (status, JSON body) result is sent back verbatim.
func newMockLlama(t *testing.T, respond func(p chatProbe) (int, string)) *llamaServer {
	t.Helper()
	return newMockLlamaWithQueue(t, 32, respond)
}

// newMockLlamaWithQueue is newMockLlama with an explicit pending-job queue
// depth, for testing back-pressure behavior.
func newMockLlamaWithQueue(t *testing.T, queueDepth int, respond func(p chatProbe) (int, string)) *llamaServer {
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
		p, err := probeRequest(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		status, respBody := respond(p)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(upstream.Close)
	ls := &llamaServer{
		ai:      openai.NewClient(option.WithBaseURL(upstream.URL + "/v1")),
		http:    upstream.Client(),
		baseURL: upstream.URL,
		jobs:    make(chan *completionJob, queueDepth),
	}
	go ls.work()
	return ls
}

// newTestServer builds a sumomo server backed by a mock llama-server and
// serves it (with CORS, like main) on an httptest listener.
func newTestServer(t *testing.T, cfg *config, respond func(p chatProbe) (int, string)) *httptest.Server {
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
