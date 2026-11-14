package models

import (
	"cmp"
	"slices"
	"strings"
)

// LanguageAuto asks a model that detects the language to do so.
const LanguageAuto = "auto"

// languageNames are the readable names of the catalog's languages: the
// locales Nemotron Streaming 3.5 takes and the bare language codes of the
// Parakeet models. A language spoken in more than one of the locales gets
// the country too.
var languageNames = map[string]string{
	LanguageAuto: "Automatic",

	"ar-AR": "Arabic",
	"bg-BG": "Bulgarian",
	"cs-CZ": "Czech",
	"da-DK": "Danish",
	"de-DE": "German",
	"en-GB": "English (UK)",
	"en-US": "English (US)",
	"es-ES": "Spanish (Spain)",
	"es-US": "Spanish (US)",
	"et-EE": "Estonian",
	"fi-FI": "Finnish",
	"fr-CA": "French (Canada)",
	"fr-FR": "French (France)",
	"hi-IN": "Hindi",
	"hr-HR": "Croatian",
	"hu-HU": "Hungarian",
	"it-IT": "Italian",
	"ja-JP": "Japanese",
	"ko-KR": "Korean",
	"nb-NO": "Norwegian Bokmål",
	"nl-NL": "Dutch",
	"pl-PL": "Polish",
	"pt-BR": "Portuguese (Brazil)",
	"pt-PT": "Portuguese (Portugal)",
	"ro-RO": "Romanian",
	"ru-RU": "Russian",
	"sk-SK": "Slovak",
	"sv-SE": "Swedish",
	"tr-TR": "Turkish",
	"uk-UA": "Ukrainian",
	"vi-VN": "Vietnamese",
	"zh-CN": "Chinese (Mandarin)",

	"bg": "Bulgarian",
	"cs": "Czech",
	"da": "Danish",
	"de": "German",
	"el": "Greek",
	"en": "English",
	"es": "Spanish",
	"et": "Estonian",
	"fi": "Finnish",
	"fr": "French",
	"hr": "Croatian",
	"hu": "Hungarian",
	"it": "Italian",
	"lt": "Lithuanian",
	"lv": "Latvian",
	"mt": "Maltese",
	"nl": "Dutch",
	"pl": "Polish",
	"pt": "Portuguese",
	"ro": "Romanian",
	"ru": "Russian",
	"sk": "Slovak",
	"sl": "Slovenian",
	"sv": "Swedish",
	"uk": "Ukrainian",
}

// LanguageName returns a readable name for a language code, such as
// "Spanish (Spain)" for "es-ES", "English" for "en" and "Automatic" for
// "auto". A code it does not know, such as one a model detected outside the
// catalog's list, comes back as it is.
func LanguageName(code string) string {
	if n, ok := languageNames[code]; ok {
		return n
	}
	for c, n := range languageNames {
		if strings.EqualFold(c, code) {
			return n
		}
	}
	return code
}

// LanguageChoices returns the languages the user can choose between for m:
// "auto" first when the model detects the language, then its locales by
// name. It returns nil when the language is not the user's choice: a model
// with one language, or one that detects the language without taking a
// choice (see FixedLanguage).
func (m *Model) LanguageChoices() []string {
	if !m.Capabilities.LanguageSelection || len(m.Languages) < 2 {
		return nil
	}
	langs := slices.Clone(m.Languages)
	slices.SortStableFunc(langs, func(a, b string) int { return cmp.Compare(LanguageName(a), LanguageName(b)) })
	if m.Capabilities.LanguageDetection {
		langs = append([]string{LanguageAuto}, langs...)
	}
	return langs
}

// FixedLanguage returns the language of a model whose language is not the
// user's choice: its one language, such as "en", or "auto" for a model that
// detects the language itself. It returns "" when the user chooses.
func (m *Model) FixedLanguage() string {
	switch {
	case m.LanguageChoices() != nil:
		return ""
	case len(m.Languages) == 1:
		return m.Languages[0]
	default:
		return LanguageAuto
	}
}

// ResolveLanguage returns the language to ask m for, given the language
// setting: the setting itself when it is one of LanguageChoices (in the
// catalog's spelling), "auto" when the model detects the language and the
// setting is not a choice, and "" for a model whose language is fixed, which
// takes no language.
func (m *Model) ResolveLanguage(setting string) string {
	choices := m.LanguageChoices()
	if choices == nil {
		return ""
	}
	for _, c := range choices {
		if strings.EqualFold(c, setting) {
			return c
		}
	}
	if m.Capabilities.LanguageDetection {
		return LanguageAuto
	}
	return choices[0]
}
