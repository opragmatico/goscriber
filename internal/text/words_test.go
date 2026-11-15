package text

import (
	"slices"
	"strings"
	"testing"
)

func TestCorrectWords(t *testing.T) {
	const threshold = 0.18 // the setting's default
	tests := []struct {
		name  string
		in    string
		words []string
		want  string
	}{
		{
			// Parakeet TDT 0.6B v2 on testdata/audio/queequeg.wav.
			name:  "the fixture's misspelling, possessive kept",
			in:    "Upon waking next morning, about daylight, I found Quequeg's arm thrown over me in the most loving and affectionate manner.",
			words: []string{"Queequeg"},
			want:  "Upon waking next morning, about daylight, I found Queequeg's arm thrown over me in the most loving and affectionate manner.",
		},
		{name: "exact match restores the casing", in: "we use nvidia gpus", words: []string{"NVIDIA"}, want: "we use NVIDIA gpus"},
		{name: "one letter off", in: "ask Kowalczik about it", words: []string{"Kowalczyk"}, want: "ask Kowalczyk about it"},
		{name: "sounds alike, spelled further off", in: "the Nematron model", words: []string{"Nemotron"}, want: "the Nemotron model"},
		{name: "punctuation around the word stays", in: "Was it (Quequeg)?", words: []string{"Queequeg"}, want: "Was it (Queequeg)?"},
		{name: "curly possessive", in: "Quequeg’s arm", words: []string{"Queequeg"}, want: "Queequeg’s arm"},
		{name: "custom word ending in s matches without the apostrophe", in: "lunch at McDonalds today", words: []string{"McDonald's"}, want: "lunch at McDonald's today"},
		{name: "custom word ending in 's matches with it", in: "lunch at McDonald's today", words: []string{"McDonald's"}, want: "lunch at McDonald's today"},
		{name: "words split by the model are joined", in: "we pay with Charge B every month", words: []string{"ChargeBee"}, want: "we pay with ChargeBee every month"},
		{name: "three words joined", in: "deploy with cube control now", words: []string{"kubecontrol"}, want: "deploy with kubecontrol now"},
		{name: "never across punctuation", in: "Charge B, che said", words: []string{"ChargeBee"}, want: "ChargeBee, che said"},
		{name: "lower-case word takes a sentence capital", in: "Cubectl is installed.", words: []string{"kubectl"}, want: "Kubectl is installed."},
		{name: "lower-case word stays lower mid-sentence", in: "run cubectl now", words: []string{"kubectl"}, want: "run kubectl now"},
		{name: "a word with capitals keeps them at a sentence start", in: "Iphone sales grew.", words: []string{"iPhone"}, want: "iPhone sales grew."},
		{name: "the closest custom word wins", in: "Jon called", words: []string{"Joan", "Jon"}, want: "Jon called"},
		{name: "every occurrence", in: "Quequeg and quequeg", words: []string{"Queequeg"}, want: "Queequeg and Queequeg"},
		{
			name: "ordinary words left alone", in: "the quick brown fox jumps over the lazy dog", words: []string{"Queequeg", "Kowalczyk", "NVIDIA"},
			want: "the quick brown fox jumps over the lazy dog",
		},
		{name: "too far off", in: "I found Quig's arm", words: []string{"Queequeg"}, want: "I found Quig's arm"},
		{name: "much shorter word", in: "open the door", words: []string{"OpenAI"}, want: "open the door"},
		{name: "digits compare by edit distance only", in: "model 4o mini", words: []string{"GPT-4o"}, want: "model 4o mini"},
		{name: "non-ASCII words", in: "talk to Mueller", words: []string{"Müller"}, want: "talk to Mueller"},
		{name: "non-ASCII exact match", in: "frag müller", words: []string{"Müller"}, want: "frag Müller"},
		{name: "text without spaces untouched", in: "今日はいい天気ですね", words: []string{"天気"}, want: "今日はいい天気ですね"},
		{name: "spacing collapsed", in: "  hello   Quequeg  ", words: []string{"Queequeg"}, want: "hello Queequeg"},
		{name: "blank custom words ignored", in: "hello  world", words: []string{"", "  "}, want: "hello  world"},
		{name: "no custom words", in: "hello  world", want: "hello  world"},
		{name: "empty text", in: "", words: []string{"Queequeg"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CorrectWords(tt.in, tt.words, threshold); got != tt.want {
				t.Errorf("CorrectWords(%q, %q) = %q, want %q", tt.in, tt.words, got, tt.want)
			}
		})
	}
}

func TestCorrectWordsThreshold(t *testing.T) {
	const in = "I found Quequeg's arm"
	for _, tt := range []struct {
		threshold float64
		want      string
	}{
		{0, in}, // off: not even exact matches
		{0.03, in},
		{0.04, "I found Queequeg's arm"}, // 1/8 edits, sounds alike: 0.0375
		{0.18, "I found Queequeg's arm"},
	} {
		if got := CorrectWords(in, []string{"Queequeg"}, tt.threshold); got != tt.want {
			t.Errorf("threshold %v: %q, want %q", tt.threshold, got, tt.want)
		}
	}
	// Without the phonetic match the score is the plain edit distance:
	// "queque" is two edits from "queequeg", 0.25.
	if got := CorrectWords("Queque was hugging me", []string{"Queequeg"}, 0.18); got != "Queque was hugging me" {
		t.Errorf("0.18: %q", got)
	}
	if got := CorrectWords("Queque was hugging me", []string{"Queequeg"}, 0.3); got != "Queequeg was hugging me" {
		t.Errorf("0.3: %q", got)
	}
	// A possessive keeps its "'s" even when the word with it, "queques",
	// would be the closer match.
	if got := CorrectWords("I found Queque's arm", []string{"Queequeg"}, 0.18); got != "I found Queque's arm" {
		t.Errorf("0.18: %q", got)
	}
	if got := CorrectWords("I found Queque's arm", []string{"Queequeg"}, 0.3); got != "I found Queequeg's arm" {
		t.Errorf("0.3: %q", got)
	}
}

func TestVocabulary(t *testing.T) {
	got := Vocabulary([]string{" Queequeg ", "", "New   York", "queequeg", "  ", "NVIDIA"})
	want := []string{"Queequeg", "New York", "NVIDIA"}
	if !slices.Equal(got, want) {
		t.Errorf("Vocabulary = %q, want %q", got, want)
	}
	if got := Vocabulary(nil); got != nil {
		t.Errorf("Vocabulary(nil) = %q", got)
	}
}

func TestSoundex(t *testing.T) {
	for word, want := range map[string]string{
		"robert":   "R163",
		"rupert":   "R163",
		"rubin":    "R150",
		"ashcraft": "A261", // h does not separate the s and the c
		"tymczak":  "T522",
		"pfister":  "P236", // the f has the p's code
		"honeyman": "H555",
		"queequeg": "Q220",
		"quequeg":  "Q220",
		"a":        "A000",
		"Lee":      "L000",
		"4o":       "",
		"müller":   "",
		"":         "",
	} {
		if got := soundex(word); got != want {
			t.Errorf("soundex(%q) = %q, want %q", word, got, want)
		}
	}
}

func TestLevenshtein(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"queequeg", "quequeg", 1},
		{"müller", "mueller", 2},
		{"天気", "天気", 0},
	} {
		if got := levenshtein(tt.a, tt.b); got != tt.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := levenshtein(tt.b, tt.a); got != tt.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.b, tt.a, got, tt.want)
		}
	}
}

func TestSplitPunct(t *testing.T) {
	for _, tt := range []struct{ in, prefix, suffix string }{
		{"hello", "", ""},
		{"!hello?", "!", "?"},
		{"...hello...", "...", "..."},
		{"(Quequeg's),", "(", "),"},
		{"「你好」", "「", "」"},
		{"你好。", "", "。"},
		{"--", "--", ""},
	} {
		p, s := splitPunct(tt.in)
		if p != tt.prefix || s != tt.suffix {
			t.Errorf("splitPunct(%q) = %q, %q, want %q, %q", tt.in, p, s, tt.prefix, tt.suffix)
		}
	}
}

// A long transcript with many custom words corrects in well under a
// millisecond per word.
func BenchmarkCorrectWords(b *testing.B) {
	text := strings.Repeat("Upon waking next morning, about daylight, I found Quequeg's arm thrown over me. ", 20)
	words := []string{"Queequeg", "Kowalczyk", "NVIDIA", "Nemotron", "Parakeet", "kubectl", "Ishmael", "Starbuck", "Ahab", "Goscriber"}
	for b.Loop() {
		CorrectWords(text, words, 0.18)
	}
}
