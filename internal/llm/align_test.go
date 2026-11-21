package llm

import (
	"fmt"
	"strings"
	"testing"
)

// talkOf is n words of numbered sentences, five words each.
func talkOf(sentences int) string {
	var b []string
	for i := range sentences {
		b = append(b, fmt.Sprintf("this is sentence number %d.", i))
	}
	return strings.Join(b, " ")
}

func TestCovered(t *testing.T) {
	src := talkOf(40) // 200 words
	for _, tc := range []struct {
		name, answer string
		want         int
	}{
		{"whole", strings.ToUpper(src), 200},
		{"first half", talkOf(20), 100},
		{"first half, last word alone", talkOf(19) + " This is sentence number", 99},
		{"fillers left out", strings.ReplaceAll(talkOf(20), "is ", ""), 100},
		{"nothing alike", "uma resposta que não tem nada a ver", 0},
	} {
		if got := covered(src, tc.answer); got != tc.want {
			t.Errorf("%s: covered = %d, want %d", tc.name, got, tc.want)
		}
	}

	// Punctuation and case do not count, and the count is of the
	// transcript's own words.
	if got := covered("Hello, world: it's me.", "hello world it s"); got != 3 {
		t.Errorf("punctuation: covered = %d, want 3", got)
	}

	// A transcript that repeats itself is matched where the answer is: an
	// answer that stops in the first copy covers only that far.
	once := "the quick brown fox jumps over the lazy dog while the cat sleeps by the warm fire tonight."
	twice := once + " " + once
	if got := covered(twice, once); got != len(strings.Fields(once)) {
		t.Errorf("repeated transcript: covered = %d, want %d", got, len(strings.Fields(once)))
	}

	// A passage the model rewrote does not lose the place for good.
	rewritten := strings.Replace(talkOf(30), "this is sentence number 10. this is sentence number 11.", "two sentences here, put another way entirely.", 1)
	if got := covered(src, rewritten); got != 150 {
		t.Errorf("rewritten passage: covered = %d, want 150", got)
	}
}
