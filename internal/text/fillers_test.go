package text

import (
	"slices"
	"testing"
)

func TestRemoveFillers(t *testing.T) {
	en := Fillers("en-US")
	tests := []struct {
		name    string
		in      string
		fillers []string // nil means en
		want    string
	}{
		// At the start.
		{name: "start, comma", in: "Um, so I think we should ship it.", want: "So I think we should ship it."},
		{name: "start, no punctuation", in: "Uh I think so.", want: "I think so."},
		{name: "start, lower case", in: "uh, I think so.", want: "I think so."},
		{name: "start, sentence end of its own", in: "Um. So I think.", want: "So I think."},
		{name: "start of a later sentence", in: "That works. Um, let me check.", want: "That works. Let me check."},
		{name: "start, two fillers", in: "Um, uh, so yes.", want: "So yes."},
		{name: "start, quote opened before it", in: `“Um, hello,” she said.`, want: `“Hello,” she said.`},
		{name: "start, Spanish inverted mark", in: "¿Hmm, sí?", fillers: Fillers("es"), want: "¿Sí?"},

		// In the middle.
		{name: "middle, between commas", in: "So I was, uh, thinking about this.", want: "So I was, thinking about this."},
		{name: "middle, no punctuation", in: "So uhm I was thinking uh about this", want: "So I was thinking about this"},
		{name: "middle, capitalized mid-sentence", in: "He said, Um, not today.", want: "He said, not today."},
		{name: "middle, period before a lower-case word is dropped", in: "Well, uhm, I think, uh. that's right", want: "Well, I think, that's right"},
		{name: "middle, sentence end moves to the word before", in: "I think, uh. That's right.", want: "I think. That's right."},
		{name: "middle, question mark moves", in: "Is it ready, um? Yes.", want: "Is it ready? Yes."},
		{name: "middle, in brackets", in: "It was (um) fine.", want: "It was fine."},
		{name: "middle, in quotes", in: `He said "uh" twice.`, want: "He said twice."},
		{name: "middle, case-insensitive", in: "UHM this is UH a test", want: "This is a test"},
		{name: "middle, drawn out", in: "It was ummm, fine and hmmmm good.", want: "It was fine and good."},

		// At the end.
		{name: "end, period", in: "It works, um.", want: "It works."},
		{name: "end, no punctuation", in: "It works uh", want: "It works"},
		{name: "end, ellipsis", in: "And then, uh…", want: "And then…"},
		{name: "end, after a sentence end", in: "It works. Um.", want: "It works."},
		{name: "end, closing quote stays", in: `He said “yes, um.” Then left.`, want: `He said “yes.” Then left.`},
		{name: "end, two fillers", in: "Done, uh, um.", want: "Done."},

		// Nothing else changes.
		{name: "words starting like fillers", in: "The umbrella hummed.", want: "The umbrella hummed."},
		{name: "a filler inside a word", in: "Uh-oh, ahead.", want: "Uh-oh, ahead."},
		{name: "only fillers", in: "Um, uh.", want: ""},
		{name: "spacing collapsed", in: "  Hello    world  ", want: "Hello world"},
		{name: "empty", in: "", want: ""},

		// The language decides the gated fillers.
		{name: "Portuguese um is a word", in: "Eu tenho um carro, uh.", fillers: Fillers("pt-BR"), want: "Eu tenho um carro."},
		{name: "German fillers", in: "Ich, äh, weiß nicht. Ähm, ja.", fillers: Fillers("de-DE"), want: "Ich, weiß nicht. Ja."},
		{name: "unknown language keeps um", in: "Um, uh, hello.", fillers: Fillers(""), want: "Um, hello."},

		// The user's list.
		{name: "custom phrase", in: "It was, you know, fine. Like, really.", fillers: []string{"you know", "like"}, want: "It was, fine. Really."},
		{name: "custom phrase never spans punctuation", in: "Do you, know it?", fillers: []string{"you know"}, want: "Do you, know it?"},
		{name: "custom list replaces the built-in one", in: "Um, basically it works.", fillers: []string{"basically"}, want: "Um, it works."},
		{name: "empty list removes nothing", in: "Um, it works.", fillers: []string{}, want: "Um, it works."},
		{name: "blank custom words ignored", in: "Um, it works.", fillers: []string{" ", ","}, want: "Um, it works."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fillers := tt.fillers
			if fillers == nil {
				fillers = en
			}
			if got := Tidy(RemoveFillers(tt.in, fillers)); got != tt.want {
				t.Errorf("RemoveFillers(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFillers(t *testing.T) {
	for _, tt := range []struct {
		lang      string
		has, lack []string
	}{
		{"en-US", []string{"uh", "um", "hmm"}, []string{"äh"}},
		{"en", []string{"um", "ah", "eh"}, nil},
		{"EN_gb", []string{"um"}, nil},
		{"de-DE", []string{"äh", "ähm", "uh"}, []string{"um"}},
		{"fr-CA", []string{"euh"}, []string{"um"}},
		{"pt-BR", []string{"uh"}, []string{"um", "ah", "eh"}},
		{"auto", []string{"uh", "uhm", "umm"}, []string{"um", "ah", "eh"}},
		{"", []string{"uh"}, []string{"um"}},
	} {
		got := Fillers(tt.lang)
		for _, w := range tt.has {
			if !slices.Contains(got, w) {
				t.Errorf("Fillers(%q) lacks %q", tt.lang, w)
			}
		}
		for _, w := range tt.lack {
			if slices.Contains(got, w) {
				t.Errorf("Fillers(%q) has %q", tt.lang, w)
			}
		}
	}
	// The built-in lists are never shared with callers.
	a := Fillers("en")
	a[0] = "changed"
	if Fillers("en")[0] == "changed" {
		t.Error("Fillers returned its own list")
	}
}

func TestTidy(t *testing.T) {
	for in, want := range map[string]string{
		"  Hello \t world \n": "Hello world",
		".":                   "",
		" ? ":                 "",
		"":                    "",
		"42.":                 "42.",
		"日本語。":                "日本語。",
	} {
		if got := Tidy(in); got != want {
			t.Errorf("Tidy(%q) = %q, want %q", in, got, want)
		}
	}
}
