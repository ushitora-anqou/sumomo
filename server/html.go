package main

import (
	"context"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// blockTags are elements whose closing edge becomes a newline when an
// HTML fragment is flattened to text, so words from separate blocks
// (e.g. two <p> paragraphs) never concatenate into one.
var blockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true,
	"dd": true, "div": true, "dl": true, "dt": true, "figcaption": true,
	"figure": true, "footer": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "header": true, "hr": true,
	"li": true, "main": true, "nav": true, "ol": true, "p": true,
	"pre": true, "section": true, "table": true, "tbody": true,
	"thead": true, "tr": true, "ul": true,
}

// decorationSigil matches a hashtag or mention sigil at the start of a
// word ("#I", "@user"). Mastodon emits these as decoration around linked
// text and they only hurt translation. Sigils embedded in a word (C#,
// user@example.com) are left alone.
var decorationSigil = regexp.MustCompile(`(^|\s)[#@]`)

// stripDecoration removes hashtag/mention sigils that start a word from
// plain text ("Hello #I am Tony" → "Hello I am Tony").
func stripDecoration(text string) string {
	return decorationSigil.ReplaceAllString(text, "$1")
}

// htmlToText flattens an HTML fragment to plain text for translation.
// All markup — the decoration (tags, links, emoji images) — is dropped:
// block boundaries become newlines and hashtag/mention sigils are
// stripped, so `<p>Hello <a …>#<span>I am Tony</span></a></p>` yields
// the single sentence "Hello I am Tony" instead of three fragments.
func htmlToText(fragment string) string {
	nodes, err := xhtml.ParseFragment(strings.NewReader(fragment), &xhtml.Node{
		Type:     xhtml.ElementNode,
		DataAtom: atom.Div,
		Data:     "div",
	})
	if err != nil {
		// The parser recovers from any input and virtually never fails;
		// this is just safety so a broken fragment still translates.
		return strings.TrimSpace(fragment)
	}
	var b strings.Builder
	for _, n := range nodes {
		writeText(&b, n)
	}
	return stripDecoration(strings.TrimSpace(b.String()))
}

// writeText appends the text content of n: text nodes verbatim (Node.Data
// is already entity-decoded), <br> as a newline, a newline after block
// elements, and nothing at all for script/style.
func writeText(b *strings.Builder, n *xhtml.Node) {
	switch n.Type {
	case xhtml.TextNode:
		b.WriteString(n.Data)
	case xhtml.ElementNode:
		if n.DataAtom == atom.Br {
			b.WriteString("\n")
			return
		}
		if n.DataAtom == atom.Script || n.DataAtom == atom.Style {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeText(b, c)
		}
		if blockTags[n.Data] {
			b.WriteString("\n")
		}
	}
}

// htmlHasTranslatableText reports whether the fragment contains any
// translatable content (letters) once flattened to text.
func htmlHasTranslatableText(fragment string) bool {
	return translatable(htmlToText(fragment))
}

// translateHTML translates an HTML-formatted text by first stripping all
// decoration: the fragment is flattened to one plain text (tags removed,
// block boundaries becoming newlines, hashtag/mention sigils dropped)
// which is then translated as a whole. Splitting at tags, as
// LibreTranslate does, fragments sentences that span markup ("Hello",
// "#", "I am Tony" instead of "Hello I am Tony") and hurts accuracy;
// translating one unit keeps the full sentence intact for the model.
// The model preserves the input line structure, so line breaks come
// back out as <br> — the one piece of markup worth restoring, since
// Mastodon renders translatedText as HTML where a plain newline
// collapses to a space.
func (s *server) translateHTML(ctx context.Context, fragment, srcPlamo, tgtPlamo string) (string, error) {
	text := htmlToText(fragment)
	out, err := s.llama.complete(ctx, text, srcPlamo, tgtPlamo, 0.0, s.maxTokensFor(text))
	if err != nil {
		return "", err
	}
	return textToHTML(out), nil
}

// textToHTML turns a translated plain text back into an HTML fragment:
// each line break becomes a <br> and everything else is escaped, so the
// only markup in the output is the line breaks themselves.
func textToHTML(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = xhtml.EscapeString(l)
	}
	return strings.Join(lines, "<br>")
}
