package main

import (
	"context"
	"html"
	"strings"

	xhtml "golang.org/x/net/html"
)

// htmlHasTranslatableText reports whether any text run of the fragment
// contains translatable content (letters).
func htmlHasTranslatableText(fragment string) bool {
	z := xhtml.NewTokenizer(strings.NewReader(fragment))
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			return false
		}
		if tt == xhtml.TextToken && translatable(z.Token().Data) {
			return true
		}
	}
}

// translateHTML translates the text runs of an HTML fragment while keeping
// the markup intact, mirroring how LibreTranslate handles format=html.
// Entities are decoded before translation and re-encoded afterwards.
// Tag tokens are copied through verbatim.
func (s *server) translateHTML(ctx context.Context, fragment, srcPlamo, tgtPlamo string) (string, error) {
	var b strings.Builder
	z := xhtml.NewTokenizer(strings.NewReader(fragment))
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			return b.String(), nil
		}
		if tt != xhtml.TextToken {
			b.Write(z.Raw()) // tags, comments, doctype: verbatim
			continue
		}
		// Token().Data is the unescaped text; the raw bytes keep the
		// original escaping for runs that are not translated.
		text := z.Token().Data
		if !translatable(text) {
			b.Write(z.Raw())
			continue
		}
		out, err := s.llama.complete(ctx, text, srcPlamo, tgtPlamo, 0.0, s.maxTokensFor(text))
		if err != nil {
			return "", err
		}
		b.WriteString(html.EscapeString(out))
	}
}
