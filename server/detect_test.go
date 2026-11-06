package main

import "testing"

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []detection
	}{
		{"hiragana", "こんにちは", []detection{{Confidence: 100, Language: "ja"}}},
		{"katakana", "ハロー", []detection{{Confidence: 100, Language: "ja"}}},
		{"kana wins over latin", "Hello こんにちは", []detection{{Confidence: 100, Language: "ja"}}},
		{"hangul", "안녕하세요", []detection{{Confidence: 100, Language: "ko"}}},
		{"han", "你好", []detection{{Confidence: 100, Language: "zh"}}},
		{"thai", "สวัสดี", []detection{{Confidence: 100, Language: "th"}}},
		{"arabic", "مرحبا", []detection{{Confidence: 100, Language: "ar"}}},
		{"hebrew", "שלום", []detection{{Confidence: 100, Language: "he"}}},
		{"devanagari", "नमस्ते", []detection{{Confidence: 100, Language: "hi"}}},
		{"cyrillic", "Привет", []detection{{Confidence: 100, Language: "ru"}}},
		{"latin defaults to english", "Hello, world!", []detection{{Confidence: 60, Language: "en"}}},
		{"digits only", "12345", nil},
		{"punctuation only", "!? ---", nil},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectLanguage(tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("detectLanguage(%q) = %v, want %v", tt.text, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("detectLanguage(%q)[%d] = %+v, want %+v", tt.text, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestTranslatable(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"Hello", true},
		{"123", false},
		{"! ? , .", false},
		{"a1b2", true},
		{"", false},
	}
	for _, tt := range tests {
		if got := translatable(tt.text); got != tt.want {
			t.Errorf("translatable(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestJoinTexts(t *testing.T) {
	got := joinTexts([]string{"Hello", "こんにちは"})
	want := "Hello\nこんにちは"
	if got != want {
		t.Errorf("joinTexts() = %q, want %q", got, want)
	}
}
