package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestHTMLHasTranslatableText(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"<p>Hello</p>", true},
		{"<p>123</p>", false},
		{"<br/><hr/>", false},
		{"&amp;", false},
		{"", false},
		{"a &lt; b", true},
	}
	for _, tt := range tests {
		if got := htmlHasTranslatableText(tt.in); got != tt.want {
			t.Errorf("htmlHasTranslatableText(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestHTMLToText(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// Block boundaries become newlines; words never concatenate.
		{"<p>Hello</p><p>World</p>", "Hello\nWorld"},
		{"a<br/>b", "a\nb"},
		{"<ul><li>a</li><li>b</li></ul>", "a\nb"},
		// Entities are decoded by the parser.
		{"<p>a &amp; b &lt; c</p>", "a & b < c"},
		// Tags and attributes are decoration and vanish.
		{`<a href="https://example.com/x" class="x">Link</a> ok`, "Link ok"},
		{"Hello <img src=\"/emoji/smile.png\" alt=\":smile:\"/> world", "Hello  world"},
		// Hashtag/mention sigils are stripped when they start a word.
		{`Hello <a href="https://example.com/tags/x" class="mention hashtag" rel="tag">#<span>I am Tony</span></a>`, "Hello I am Tony"},
		{"Hello #I am Tony", "Hello I am Tony"},
		{`<span class="h-card"><a href="https://x/@u" class="u-url mention">@<span>u</span></a></span> hi`, "u hi"},
		{"#leading and @mid sigils", "leading and mid sigils"},
		// Sigils inside a word (C#, email) are not decoration.
		{"C# and user@example.com stay", "C# and user@example.com stay"},
		// Script/style contents are not text.
		{"<script>alert(1)</script>ok", "ok"},
		{"<style>a{color:red}</style>ok", "ok"},
		{"plain text", "plain text"},
	}
	for _, tt := range tests {
		if got := htmlToText(tt.in); got != tt.want {
			t.Errorf("htmlToText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTranslateHTML(t *testing.T) {
	var sent []string
	mock := newMockLlama(t, func(p chatProbe) (int, string) {
		sent = append(sent, p.User)
		return http.StatusOK, chatCompletionJSON("ja: " + p.User)
	})
	s := &server{cfg: testConfig(), llama: mock}

	out, err := s.translateHTML(context.Background(),
		"<p>Hello &amp; bye</p><br/>123", "en", "ja")
	if err != nil {
		t.Fatalf("translateHTML() error: %v", err)
	}
	// The whole fragment goes to the model as one plain-text unit: tags
	// dropped, entities decoded, block boundaries as newlines. Even the
	// digit-only run is part of the single text, so the sentence context
	// is never fragmented.
	want := "Hello & bye\n\n123"
	if len(sent) != 1 || sent[0] != want {
		t.Errorf("model input = %v, want exactly one call with %q", sent, want)
	}
	if out != "ja: "+want {
		t.Errorf("translateHTML() = %q, want %q", out, "ja: "+want)
	}
}

// TestTranslateHTMLStripsDecoration covers the Mastodon shape: hashtag
// and mention links carry decoration (#/@ sigils, spans, hrefs) that must
// all vanish before translation, leaving one plain sentence.
func TestTranslateHTMLStripsDecoration(t *testing.T) {
	var sent []string
	mock := newMockLlama(t, func(p chatProbe) (int, string) {
		sent = append(sent, p.User)
		return http.StatusOK, chatCompletionJSON("ja: " + p.User)
	})
	s := &server{cfg: testConfig(), llama: mock}

	in := `<p>Hello <span class="h-card"><a href="https://example.com/@tony" class="u-url mention">@<span>tony</span></a></span>, welcome to <a href="https://example.com/tags/iot" class="mention hashtag" rel="tag">#<span>I am Tony</span></a></p>`
	out, err := s.translateHTML(context.Background(), in, "en", "ja")
	if err != nil {
		t.Fatalf("translateHTML() error: %v", err)
	}
	want := "Hello tony, welcome to I am Tony"
	if len(sent) != 1 || sent[0] != want {
		t.Errorf("model input = %v, want exactly one call with %q", sent, want)
	}
	if out != "ja: "+want {
		t.Errorf("translateHTML() = %q, want %q", out, "ja: "+want)
	}
}

// TestTranslatePlainTextStripsDecoration covers format=text input: the
// sigils are stripped even without markup, and sigils inside a word
// (email, C#) survive.
func TestTranslatePlainTextStripsDecoration(t *testing.T) {
	var sent []string
	ts := newTestServer(t, nil, func(p chatProbe) (int, string) {
		sent = append(sent, p.User)
		return http.StatusOK, chatCompletionJSON("ja: " + p.User)
	})
	_, _, body := postJSON(t, ts.URL+"/translate", "application/json",
		`{"q":["Hello #I am Tony","ping @tony now","C# and user@example.com"],"source":"en","target":"ja"}`)
	want := []string{"Hello I am Tony", "ping tony now", "C# and user@example.com"}
	for i, w := range want {
		if body["translatedText"].([]any)[i] != "ja: "+w {
			t.Errorf("translatedText[%d] = %v, want %q", i, body["translatedText"].([]any)[i], "ja: "+w)
		}
	}
	if len(sent) != 3 {
		t.Fatalf("model calls = %d, want 3", len(sent))
	}
	for i, w := range want {
		if sent[i] != w {
			t.Errorf("model input[%d] = %q, want %q", i, sent[i], w)
		}
	}
}

func TestTranslateHTMLUpstreamError(t *testing.T) {
	mock := newMockLlama(t, func(p chatProbe) (int, string) {
		return http.StatusServiceUnavailable, chatErrorJSON("boom")
	})
	s := &server{cfg: testConfig(), llama: mock}
	if _, err := s.translateHTML(context.Background(), "<p>Hello</p>", "en", "ja"); err == nil {
		t.Fatal("translateHTML() succeeded, want an error")
	} else if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v, want it to wrap the upstream error", err)
	}
}
