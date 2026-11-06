package main

import (
	"context"
	"html"
	"strings"
)

// htmlToken is either a raw tag ("<p>", "</p>", "<br />") or a text run
// between tags.
type htmlToken struct {
	raw   string
	isTag bool
}

// tokenizeHTML splits an HTML fragment into tags and text runs. Mastodon
// status bodies are well-formed simple markup (p/a/br/span), so a
// non-nesting scanner is sufficient here.
func tokenizeHTML(s string) []htmlToken {
	var tokens []htmlToken
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			if s != "" {
				tokens = append(tokens, htmlToken{raw: s})
			}
			return tokens
		}
		if i > 0 {
			tokens = append(tokens, htmlToken{raw: s[:i]})
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			tokens = append(tokens, htmlToken{raw: s[i:]})
			return tokens
		}
		tokens = append(tokens, htmlToken{raw: s[i : i+j+1], isTag: true})
		s = s[i+j+1:]
	}
}

// htmlHasTranslatableText reports whether any text run of the fragment
// contains translatable content (letters).
func htmlHasTranslatableText(fragment string) bool {
	for _, tok := range tokenizeHTML(fragment) {
		if !tok.isTag && translatable(html.UnescapeString(tok.raw)) {
			return true
		}
	}
	return false
}

// translateHTML translates the text runs of an HTML fragment while keeping
// the markup intact, mirroring how LibreTranslate handles format=html.
// Entities are decoded before translation and re-encoded afterwards.
func (s *server) translateHTML(ctx context.Context, fragment, srcPlamo, tgtPlamo string) (string, error) {
	var b strings.Builder
	for _, tok := range tokenizeHTML(fragment) {
		if tok.isTag {
			b.WriteString(tok.raw)
			continue
		}
		text := html.UnescapeString(tok.raw)
		if !translatable(text) {
			b.WriteString(tok.raw)
			continue
		}
		out, err := s.llama.complete(ctx, text, srcPlamo, tgtPlamo, 0.0, s.maxTokensFor(text))
		if err != nil {
			return "", err
		}
		b.WriteString(html.EscapeString(out))
	}
	return b.String(), nil
}
