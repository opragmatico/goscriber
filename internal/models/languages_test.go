package models

import (
	"slices"
	"testing"
)

func TestEveryCatalogLanguageHasAName(t *testing.T) {
	for _, m := range Default().ASR() {
		for _, l := range m.Languages {
			if LanguageName(l) == l {
				t.Errorf("%s: no readable name for %q", m.ID, l)
			}
		}
	}
	for code, want := range map[string]string{
		"auto": "Automatic", "es-ES": "Spanish (Spain)", "ES-es": "Spanish (Spain)", "en": "English", "xx-YY": "xx-YY",
	} {
		if got := LanguageName(code); got != want {
			t.Errorf("LanguageName(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestLanguageChoices(t *testing.T) {
	c := Default()
	nemo, _ := c.Model("nemotron-3.5-asr-streaming-0.6b")
	choices := nemo.LanguageChoices()
	if len(choices) != 33 || choices[0] != LanguageAuto || !slices.Contains(choices, "pt-BR") {
		t.Fatalf("Nemotron choices %v, want auto then its 32 locales", choices)
	}
	if !slices.IsSortedFunc(choices[1:], func(a, b string) int {
		if LanguageName(a) < LanguageName(b) {
			return -1
		}
		return 1
	}) {
		t.Errorf("choices not sorted by name: %v", choices)
	}
	if nemo.FixedLanguage() != "" {
		t.Errorf("Nemotron's language is the user's choice, not fixed %q", nemo.FixedLanguage())
	}
	for setting, want := range map[string]string{"es-ES": "es-ES", "ES-es": "es-ES", "auto": "auto", "": "auto", "es": "auto", "tlh": "auto"} {
		if got := nemo.ResolveLanguage(setting); got != want {
			t.Errorf("Nemotron ResolveLanguage(%q) = %q, want %q", setting, got, want)
		}
	}

	// Parakeet TDT v2 is English only: the choice is locked.
	v2, _ := c.Model("parakeet-tdt-0.6b-v2")
	if v2.LanguageChoices() != nil || v2.FixedLanguage() != "en" || v2.ResolveLanguage("es-ES") != "" {
		t.Errorf("Parakeet v2: choices %v, fixed %q", v2.LanguageChoices(), v2.FixedLanguage())
	}
	// v3 detects the language and takes no choice.
	v3, _ := c.Model("parakeet-tdt-0.6b-v3")
	if v3.LanguageChoices() != nil || v3.FixedLanguage() != LanguageAuto || v3.ResolveLanguage("de-DE") != "" {
		t.Errorf("Parakeet v3: choices %v, fixed %q", v3.LanguageChoices(), v3.FixedLanguage())
	}
}
