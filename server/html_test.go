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

func TestTranslateHTML(t *testing.T) {
	var translated []string
	mock := newMockLlama(t, func(p chatProbe) (int, string) {
		translated = append(translated, p.User)
		return http.StatusOK, chatCompletionJSON("「" + p.User + "」")
	})
	s := &server{cfg: testConfig(), llama: mock}

	out, err := s.translateHTML(context.Background(),
		"<p>Hello &amp; bye</p><br/>123", "en", "ja")
	if err != nil {
		t.Fatalf("translateHTML() error: %v", err)
	}
	// Text runs are wrapped by the mock; tags and non-translatable runs
	// stay verbatim, and the translated text is re-escaped, so the "&"
	// comes back as "&amp;" just like the input entity.
	want := "<p>「Hello &amp; bye」</p><br/>123"
	if out != want {
		t.Errorf("translateHTML() = %q, want %q", out, want)
	}
	// Only the translatable text run was sent to the model.
	if len(translated) != 1 || translated[0] != "Hello & bye" {
		t.Errorf("translated text runs = %v, want [Hello & bye]", translated)
	}
}

func TestTranslateHTMLKeepsMarkupVerbatim(t *testing.T) {
	mock := newMockLlama(t, func(p chatProbe) (int, string) {
		return http.StatusOK, chatCompletionJSON("「" + p.User + "」")
	})
	s := &server{cfg: testConfig(), llama: mock}

	// Attributes and self-closing tags must survive untouched; the
	// unterminated "<" is plain text (a text run), and digits-only runs
	// pass through without a model call.
	in := `<a href="https://example.com/x?a=1&amp;b=2" class="x">Link</a> a < b <br/> 42`
	out, err := s.translateHTML(context.Background(), in, "en", "ja")
	if err != nil {
		t.Fatalf("translateHTML() error: %v", err)
	}
	want := `<a href="https://example.com/x?a=1&amp;b=2" class="x">「Link」</a>「 a &lt; b 」<br/> 42`
	if out != want {
		t.Errorf("translateHTML() =\n %q\nwant\n %q", out, want)
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
