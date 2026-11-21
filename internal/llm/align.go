package llm

import (
	"strings"
	"unicode"
)

// alignWindow is how far ahead in the transcript the alignment looks for
// the answer's next words: room for the fillers and false starts a model
// leaves out between two words it keeps.
const alignWindow = 50

// resyncWindow is how far ahead the alignment looks for three of the
// answer's words in a row when two cannot be found near: past a passage
// the model rewrote.
const resyncWindow = 400

// lastWordWindow is how far ahead the answer's last word, left without a
// second to pair with, is looked for.
const lastWordWindow = 5

// words are a text's words for alignment: lowercased, split at anything
// but letters and digits, each with the index in strings.Fields(text) of
// the field it came from.
type words struct {
	norm  []string
	field []int
}

func wordsOf(text string) words {
	var w words
	for i, f := range strings.Fields(text) {
		for _, part := range strings.FieldsFunc(strings.ToLower(f), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		}) {
			w.norm = append(w.norm, part)
			w.field = append(w.field, i)
		}
	}
	return w
}

// covered is how many of transcript's leading fields (strings.Fields) the
// answer, a cleaned version of it, accounts for: the field after the last
// one the answer was matched to. It walks the answer and the transcript
// together, matching the answer's next words in the transcript, nearest
// first: two in a row within alignWindow ahead of where the last match
// left off, so fillers the model left out and words it changed do not lose
// its place; else two within alignWindow behind it, where a common pair of
// words matched too early; else three within resyncWindow ahead, past a
// longer gap; else, anywhere further on, a run of six, past a passage the
// model left out. Never searching further back than alignWindow, a passage
// the transcript repeats is matched where the answer is, not in an earlier
// or a later copy. It is 0 when nothing matches.
func covered(transcript, answer string) int {
	src, ans := wordsOf(transcript), wordsOf(answer)
	s, a := src.norm, ans.norm
	next, last := 0, -1 // next transcript word to look from; last one matched
	i := 0
	for i+1 < len(a) {
		k, n := -1, 0
		for _, try := range []struct{ run, from, to int }{
			{2, next, next + alignWindow},
			{2, next - alignWindow, next},
			{3, next, next + resyncWindow},
			{6, next, len(s)},
		} {
			if i+try.run > len(a) {
				continue
			}
			if k = find(s, a[i:i+try.run], max(0, try.from), min(len(s), try.to)); k >= 0 {
				n = try.run
				break
			}
		}
		if k < 0 {
			i++
			continue
		}
		next, last = k+n, k+n-1
		i += n
	}
	// A last word left on its own matches just ahead, or the answer's end
	// would come back at the start of the rest.
	if i == len(a)-1 {
		if k := find(s, a[i:], next, min(len(s), next+lastWordWindow)); k >= 0 {
			last = k
		}
	}
	if last < 0 {
		return 0
	}
	return src.field[last] + 1
}

// find is the first index k in [from, to) where s holds the words of run
// in a row, or -1.
func find(s, run []string, from, to int) int {
	for k := from; k+len(run) <= len(s) && k < to; k++ {
		match := true
		for j, w := range run {
			if s[k+j] != w {
				match = false
				break
			}
		}
		if match {
			return k
		}
	}
	return -1
}
