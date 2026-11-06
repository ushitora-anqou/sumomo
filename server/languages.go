package main

// language describes one language supported by plamo-3-translate-0.8b.
type language struct {
	// plamo is the language code accepted by the model's chat template
	// (e.g. "zh-hans").
	plamo string
	// iso is the LibreTranslate-facing language code (e.g. "zh").
	iso string
	// name is the human-readable language name in English, as returned by
	// LibreTranslate's /languages endpoint.
	name string
}

// languages lists the 53 languages supported by plamo-3-translate-0.8b, in
// the order used by the model card. LibreTranslate uses "zh"/"zt" for
// Simplified/Traditional Chinese while the model uses "zh-hans"/"zh-hant".
var languages = []language{
	{plamo: "ja", iso: "ja", name: "Japanese"},
	{plamo: "en", iso: "en", name: "English"},
	{plamo: "zh-hans", iso: "zh", name: "Chinese (Simplified)"},
	{plamo: "zh-hant", iso: "zt", name: "Chinese (Traditional)"},
	{plamo: "ko", iso: "ko", name: "Korean"},
	{plamo: "ar", iso: "ar", name: "Arabic"},
	{plamo: "az", iso: "az", name: "Azerbaijani"},
	{plamo: "be", iso: "be", name: "Belarusian"},
	{plamo: "bg", iso: "bg", name: "Bulgarian"},
	{plamo: "bn", iso: "bn", name: "Bengali"},
	{plamo: "ca", iso: "ca", name: "Catalan"},
	{plamo: "ceb", iso: "ceb", name: "Cebuano"},
	{plamo: "de", iso: "de", name: "German"},
	{plamo: "es", iso: "es", name: "Spanish"},
	{plamo: "et", iso: "et", name: "Estonian"},
	{plamo: "eu", iso: "eu", name: "Basque"},
	{plamo: "fa", iso: "fa", name: "Persian"},
	{plamo: "fr", iso: "fr", name: "French"},
	{plamo: "gu", iso: "gu", name: "Gujarati"},
	{plamo: "he", iso: "he", name: "Hebrew"},
	{plamo: "hi", iso: "hi", name: "Hindi"},
	{plamo: "hr", iso: "hr", name: "Croatian"},
	{plamo: "hu", iso: "hu", name: "Hungarian"},
	{plamo: "hy", iso: "hy", name: "Armenian"},
	{plamo: "id", iso: "id", name: "Indonesian"},
	{plamo: "it", iso: "it", name: "Italian"},
	{plamo: "jv", iso: "jv", name: "Javanese"},
	{plamo: "ka", iso: "ka", name: "Georgian"},
	{plamo: "kk", iso: "kk", name: "Kazakh"},
	{plamo: "km", iso: "km", name: "Khmer"},
	{plamo: "lt", iso: "lt", name: "Lithuanian"},
	{plamo: "lv", iso: "lv", name: "Latvian"},
	{plamo: "ml", iso: "ml", name: "Malayalam"},
	{plamo: "mr", iso: "mr", name: "Marathi"},
	{plamo: "ms", iso: "ms", name: "Malay"},
	{plamo: "my", iso: "my", name: "Burmese"},
	{plamo: "ne", iso: "ne", name: "Nepali"},
	{plamo: "nl", iso: "nl", name: "Dutch"},
	{plamo: "pa", iso: "pa", name: "Punjabi"},
	{plamo: "pl", iso: "pl", name: "Polish"},
	{plamo: "ru", iso: "ru", name: "Russian"},
	{plamo: "si", iso: "si", name: "Sinhala"},
	{plamo: "sk", iso: "sk", name: "Slovak"},
	{plamo: "sl", iso: "sl", name: "Slovenian"},
	{plamo: "sr", iso: "sr", name: "Serbian"},
	{plamo: "sw", iso: "sw", name: "Swahili"},
	{plamo: "ta", iso: "ta", name: "Tamil"},
	{plamo: "te", iso: "te", name: "Telugu"},
	{plamo: "th", iso: "th", name: "Thai"},
	{plamo: "tl", iso: "tl", name: "Tagalog"},
	{plamo: "ur", iso: "ur", name: "Urdu"},
	{plamo: "uz", iso: "uz", name: "Uzbek"},
	{plamo: "vi", iso: "vi", name: "Vietnamese"},
}

// hiddenLanguages are accepted as source/target but not listed in
// /languages because they are not standard LibreTranslate codes.
var hiddenLanguages = []language{
	{plamo: "ja(easy)", iso: "ja(easy)", name: "Japanese (easy)"},
}

// toPlamo resolves a LibreTranslate-facing code (or a model-native code such
// as "zh-hans") to the model language code. It returns "" when unknown.
var toPlamo = func() func(string) string {
	m := make(map[string]string)
	for _, l := range append(append([]language{}, languages...), hiddenLanguages...) {
		m[l.iso] = l.plamo
		m[l.plamo] = l.plamo
	}
	return func(code string) string { return m[code] }
}()

// allISOCodes returns the LibreTranslate codes of all listed languages,
// used as the "targets" array in /languages (the model translates between
// every supported pair).
var allISOCodes = func() []string {
	codes := make([]string, 0, len(languages))
	for _, l := range languages {
		codes = append(codes, l.iso)
	}
	return codes
}()
