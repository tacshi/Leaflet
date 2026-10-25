package recording

import "sort"

// Languages are the languages speech engines take, by ISO 639-1 code
// (Whisper's set, which OpenAI-compatible endpoints follow).
var Languages = map[string]string{
	"af": "Afrikaans", "am": "Amharic", "ar": "Arabic", "as": "Assamese", "az": "Azerbaijani",
	"ba": "Bashkir", "be": "Belarusian", "bg": "Bulgarian", "bn": "Bengali", "bo": "Tibetan",
	"br": "Breton", "bs": "Bosnian", "ca": "Catalan", "cs": "Czech", "cy": "Welsh",
	"da": "Danish", "de": "German", "el": "Greek", "en": "English", "es": "Spanish",
	"et": "Estonian", "eu": "Basque", "fa": "Persian", "fi": "Finnish", "fo": "Faroese",
	"fr": "French", "gl": "Galician", "gu": "Gujarati", "ha": "Hausa", "haw": "Hawaiian",
	"he": "Hebrew", "hi": "Hindi", "hr": "Croatian", "ht": "Haitian Creole", "hu": "Hungarian",
	"hy": "Armenian", "id": "Indonesian", "is": "Icelandic", "it": "Italian", "ja": "Japanese",
	"jw": "Javanese", "ka": "Georgian", "kk": "Kazakh", "km": "Khmer", "kn": "Kannada",
	"ko": "Korean", "la": "Latin", "lb": "Luxembourgish", "ln": "Lingala", "lo": "Lao",
	"lt": "Lithuanian", "lv": "Latvian", "mg": "Malagasy", "mi": "Maori", "mk": "Macedonian",
	"ml": "Malayalam", "mn": "Mongolian", "mr": "Marathi", "ms": "Malay", "mt": "Maltese",
	"my": "Burmese", "ne": "Nepali", "nl": "Dutch", "nn": "Norwegian Nynorsk", "no": "Norwegian",
	"oc": "Occitan", "pa": "Punjabi", "pl": "Polish", "ps": "Pashto", "pt": "Portuguese",
	"ro": "Romanian", "ru": "Russian", "sa": "Sanskrit", "sd": "Sindhi", "si": "Sinhala",
	"sk": "Slovak", "sl": "Slovenian", "sn": "Shona", "so": "Somali", "sq": "Albanian",
	"sr": "Serbian", "su": "Sundanese", "sv": "Swedish", "sw": "Swahili", "ta": "Tamil",
	"te": "Telugu", "tg": "Tajik", "th": "Thai", "tk": "Turkmen", "tl": "Tagalog",
	"tr": "Turkish", "tt": "Tatar", "uk": "Ukrainian", "ur": "Urdu", "uz": "Uzbek",
	"vi": "Vietnamese", "yi": "Yiddish", "yo": "Yoruba", "yue": "Cantonese", "zh": "Chinese",
}

// LanguageCodes lists "auto" then the codes by name.
func LanguageCodes() []string {
	codes := make([]string, 0, len(Languages)+1)
	for c := range Languages {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool { return Languages[codes[i]] < Languages[codes[j]] })
	return append([]string{"auto"}, codes...)
}

// LanguageName names a code; "auto" and unknown codes detect.
func LanguageName(code string) string {
	if n, ok := Languages[code]; ok {
		return n
	}
	return "Detect"
}
