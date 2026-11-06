package main

import (
	"strings"
	"unicode"
)

// detection is one /detect or detectedLanguage entry, matching
// LibreTranslate's format. Confidence ranges from 0 to 100.
type detection struct {
	Confidence float64 `json:"confidence"`
	Language   string  `json:"language"`
}

// detectLanguage guesses the language of text from its Unicode script.
// LibreTranslate uses a proper language-detection library; this server is a
// translation backend, so a script-level heuristic is enough to fill in the
// detectedLanguage field and /detect endpoint. Latin-script text defaults
// to English with reduced confidence.
func detectLanguage(text string) []detection {
	var hiragana, katakana, hangul, han, cyrillic, thai, arabic, hebrew, devanagari, latin int
	for _, r := range text {
		switch {
		case unicode.In(r, unicode.Hiragana, unicode.Katakana):
			if unicode.Is(unicode.Hiragana, r) {
				hiragana++
			} else {
				katakana++
			}
		case unicode.In(r, unicode.Hangul):
			hangul++
		case unicode.In(r, unicode.Han):
			han++
		case unicode.In(r, unicode.Cyrillic):
			cyrillic++
		case unicode.Is(unicode.Thai, r):
			thai++
		case unicode.Is(unicode.Arabic, r):
			arabic++
		case unicode.Is(unicode.Hebrew, r):
			hebrew++
		case unicode.Is(unicode.Devanagari, r):
			devanagari++
		case unicode.IsLetter(r):
			latin++
		}
	}
	switch {
	case hiragana+katakana > 0:
		return []detection{{Confidence: 100, Language: "ja"}}
	case hangul > 0:
		return []detection{{Confidence: 100, Language: "ko"}}
	case han > 0:
		return []detection{{Confidence: 100, Language: "zh"}}
	case thai > 0:
		return []detection{{Confidence: 100, Language: "th"}}
	case arabic > 0:
		return []detection{{Confidence: 100, Language: "ar"}}
	case hebrew > 0:
		return []detection{{Confidence: 100, Language: "he"}}
	case devanagari > 0:
		return []detection{{Confidence: 100, Language: "hi"}}
	case cyrillic > 0:
		return []detection{{Confidence: 100, Language: "ru"}}
	case latin > 0:
		return []detection{{Confidence: 60, Language: "en"}}
	default:
		return nil
	}
}

// translatable reports whether text contains anything worth translating.
// LibreTranslate-compatible servers return the original text unchanged for
// input that consists solely of numbers, punctuation, symbols, or
// whitespace.
func translatable(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// joinTexts concatenates texts so that script detection sees all of them
// (used to compute one detectedLanguage shared by a batch, like
// LibreTranslate does for detected languages with source=auto).
func joinTexts(texts []string) string {
	return strings.Join(texts, "\n")
}
