package main

import "testing"

func TestToPlamo(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		// LibreTranslate-facing codes resolve to model codes.
		{"ja", "ja"},
		{"en", "en"},
		{"zh", "zh-hans"}, // LibreTranslate's Simplified Chinese
		{"zt", "zh-hant"}, // LibreTranslate's Traditional Chinese
		// Model-native codes pass through.
		{"zh-hans", "zh-hans"},
		{"zh-hant", "zh-hant"},
		// Hidden (non-LibreTranslate) codes still work as source/target.
		{"ja(easy)", "ja(easy)"},
		// Unknown codes are rejected.
		{"xx", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := toPlamo(tt.code); got != tt.want {
			t.Errorf("toPlamo(%q) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

func TestLanguagesUniqueCodes(t *testing.T) {
	seenISO := map[string]bool{}
	seenPlamo := map[string]bool{}
	for _, l := range languages {
		if seenISO[l.iso] {
			t.Errorf("duplicate iso code %q", l.iso)
		}
		seenISO[l.iso] = true
		if seenPlamo[l.plamo] {
			t.Errorf("duplicate plamo code %q", l.plamo)
		}
		seenPlamo[l.plamo] = true
		if l.name == "" {
			t.Errorf("language %q has an empty name", l.iso)
		}
	}
}

func TestAllISOCodes(t *testing.T) {
	if len(allISOCodes) != len(languages) {
		t.Errorf("allISOCodes has %d entries, want %d", len(allISOCodes), len(languages))
	}
	for i, l := range languages {
		if allISOCodes[i] != l.iso {
			t.Errorf("allISOCodes[%d] = %q, want %q (order must follow languages)", i, allISOCodes[i], l.iso)
		}
	}
}

// hiddenLanguages must be usable as source/target (covered by toPlamo)
// without appearing in the /languages listing.
func TestHiddenLanguagesNotListed(t *testing.T) {
	for _, h := range hiddenLanguages {
		if toPlamo(h.iso) == "" {
			t.Errorf("hidden language %q does not resolve via toPlamo", h.iso)
		}
		for _, code := range allISOCodes {
			if code == h.iso {
				t.Errorf("hidden language %q is listed in allISOCodes", h.iso)
			}
		}
	}
}
