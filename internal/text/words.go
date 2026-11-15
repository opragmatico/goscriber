package text

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Vocabulary returns the custom words worth using: each one trimmed, with
// inner runs of spaces collapsed, and the empty ones and repeats (ignoring
// case) left out, in their original order.
func Vocabulary(words []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range words {
		w = strings.Join(strings.Fields(w), " ")
		key := strings.ToLower(w)
		if w == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, w)
	}
	return out
}

// maxNgram is how many words of the transcript a custom word may replace:
// the model may split one unusual word into several ("Charge B" for
// "ChargeBee").
const maxNgram = 3

// maxKeyLen is the longest candidate compared, in runes. Longer runs are
// not names, and the comparison grows with the square of the length.
const maxKeyLen = 50

// phoneticWeight scales the edit distance of a candidate that sounds like
// the custom word (the same Soundex code), so a misspelling that sounds
// right passes a stricter threshold. Soundex keeps only the first few
// consonants, so the discount applies only to candidates already within
// phoneticMaxDistance of the custom word: "Queque was" sounds like
// "Queequeg" too.
const (
	phoneticWeight      = 0.3
	phoneticMaxDistance = 1.0 / 3
)

// CorrectWords replaces near-misses of the custom words in s with their
// spelling: a word, or up to three consecutive words, that is close enough
// to one of words is rewritten as it. Closeness is the edit distance
// between the two, ignoring case and punctuation, divided by the longer
// one's length; when that is at most a third and both sound alike (the
// same Soundex code), it counts 30 %. A candidate closer than threshold is
// replaced by the closest custom word; 0 turns correction off. Lengths
// that differ by more than a quarter (and more than one letter) are never
// compared, so "open" does not become "OpenAI".
//
// The transcript's punctuation around a replaced word stays, as does an
// English possessive ("Quequeg's" becomes "Queequeg's"). A custom word
// written with capitals keeps its own casing; one written in lower case
// takes the transcript's capital at the start of a sentence. Words are
// separated by whitespace, so text in scripts written without spaces is
// left alone. Whitespace between words comes out as single spaces.
func CorrectWords(s string, words []string, threshold float64) string {
	words = Vocabulary(words)
	if threshold <= 0 || len(words) == 0 || strings.TrimSpace(s) == "" {
		return s
	}
	vocab := make([]vocabEntry, 0, len(words))
	for _, w := range words {
		if key := matchKey(w); key != "" {
			vocab = append(vocab, vocabEntry{word: w, key: key, code: soundex(key)})
		}
	}
	if len(vocab) == 0 {
		return s
	}

	tokens := strings.Fields(s)
	out := make([]string, 0, len(tokens))
	for i := 0; i < len(tokens); {
		best, ok := bestMatch(tokens[i:min(i+maxNgram, len(tokens))], vocab, threshold)
		if !ok {
			out = append(out, tokens[i])
			i++
			continue
		}
		first, last := tokens[i], tokens[i+best.n-1]
		prefix, _ := splitPunct(first)
		_, suffix := splitPunct(last)
		if best.possessive {
			suffix = possessiveSuffix(last) + suffix
		}
		out = append(out, prefix+caseLike(first, best.entry.word)+suffix)
		i += best.n
	}
	return strings.Join(out, " ")
}

// vocabEntry is a custom word with its comparison key and Soundex code.
type vocabEntry struct {
	word string
	key  string // matchKey(word)
	code string // soundex(key), or "" when it has none
}

// match is a custom word that fits the tokens starting at the current one.
type match struct {
	entry *vocabEntry
	n     int // tokens replaced
	// possessive: the last token's "'s" was left out of the comparison and
	// stays after the replacement.
	possessive bool
	score      float64
}

// bestMatch returns the closest custom word to the first one, two or three
// of tokens, if any is closer than threshold. A candidate never spans a
// punctuation mark: in "Charge B, che" it ends at "B,".
func bestMatch(tokens []string, vocab []vocabEntry, threshold float64) (match, bool) {
	best := match{score: threshold}
	found := false
	for n := 1; n <= len(tokens); n++ {
		if n > 1 {
			if _, suffix := splitPunct(tokens[n-2]); suffix != "" || possessiveSuffix(tokens[n-2]) != "" {
				break
			}
		}
		key := candidate{key: ngramKey(tokens[:n])}
		key.code = soundex(key.key)
		var bare candidate // key without a possessive "'s"
		if s := possessiveSuffix(tokens[n-1]); s != "" {
			bare.key = ngramKey(tokens[:n-1]) + matchKey(strings.TrimSuffix(withoutPunct(tokens[n-1]), s))
			bare.code = soundex(bare.key)
		}
		for i := range vocab {
			v := &vocab[i]
			// A possessive is compared without its "'s", which stays after
			// the replacement, and with it only to a custom word that ends
			// in s itself ("McDonald's").
			if bare.key == "" || strings.HasSuffix(v.key, "s") {
				if score, ok := closeness(key, v); ok && score < best.score {
					best = match{entry: v, n: n, score: score}
					found = true
				}
			}
			if bare.key != "" {
				if score, ok := closeness(bare, v); ok && score < best.score {
					best = match{entry: v, n: n, possessive: true, score: score}
					found = true
				}
			}
		}
	}
	return best, found
}

// candidate is words of the transcript as they are compared.
type candidate struct {
	key  string // their match keys run together
	code string // soundex(key)
}

// closeness scores how far c is from the custom word, from 0 (the same)
// up; ok is false when they are too different in length to compare.
func closeness(c candidate, v *vocabEntry) (float64, bool) {
	cn, vn := utf8.RuneCountInString(c.key), utf8.RuneCountInString(v.key)
	if cn == 0 || cn > maxKeyLen {
		return 0, false
	}
	// A candidate much longer or shorter than the custom word is another
	// word: allow a quarter of the longer length, and at least one rune.
	longer := max(cn, vn)
	if diff := abs(cn - vn); float64(diff) > max(0.25*float64(longer), 1) {
		return 0, false
	}
	score := float64(levenshtein(c.key, v.key)) / float64(longer)
	if score <= phoneticMaxDistance && v.code != "" && c.code == v.code {
		score *= phoneticWeight
	}
	return score, true
}

// matchKey is what a word is compared by: its letters and digits, in lower
// case.
func matchKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// ngramKey is the match key of tokens run together, so "Charge B" compares
// as "chargeb".
func ngramKey(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteString(matchKey(t))
	}
	return b.String()
}

// splitPunct returns what precedes a token's first letter or digit and
// what follows its last: "(Quequeg)," gives "(" and "),".
func splitPunct(token string) (prefix, suffix string) {
	start := strings.IndexFunc(token, isWordRune)
	if start < 0 {
		return token, ""
	}
	end := strings.LastIndexFunc(token, isWordRune)
	_, size := utf8.DecodeRuneInString(token[end:])
	return token[:start], token[end+size:]
}

// withoutPunct is token without its leading and trailing punctuation.
func withoutPunct(token string) string {
	prefix, suffix := splitPunct(token)
	return token[len(prefix) : len(token)-len(suffix)]
}

// possessiveSuffix returns the "'s" (or "’s") ending token's word, or "".
func possessiveSuffix(token string) string {
	w := withoutPunct(token)
	for _, s := range []string{"'s", "’s", "'S", "’S"} {
		if len(w) > len(s) && strings.HasSuffix(w, s) {
			return s
		}
	}
	return ""
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// caseLike returns the custom word as it should appear in place of
// original: as the user wrote it when that has a capital letter, else with
// original's leading capital.
func caseLike(original, word string) string {
	if strings.IndexFunc(word, unicode.IsUpper) >= 0 {
		return word
	}
	first, _ := utf8.DecodeRuneInString(withoutPunct(original))
	if !unicode.IsUpper(first) {
		return word
	}
	r, size := utf8.DecodeRuneInString(word)
	return string(unicode.ToUpper(r)) + word[size:]
}

// levenshtein is the edit distance between a and b in runes: insertions,
// deletions and substitutions.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// soundex is the American Soundex code of an ASCII word, such as "R163"
// for "robert", or "" when the word has anything but ASCII letters:
// Soundex describes English spelling, and digits or other scripts would
// match by accident.
func soundex(word string) string {
	if word == "" {
		return ""
	}
	for i := range len(word) {
		if c := word[i] | 0x20; c < 'a' || c > 'z' {
			return ""
		}
	}
	const codes = "01230120022455012623010202" // a to z
	code := []byte{word[0] &^ 0x20}
	last := codes[(word[0]|0x20)-'a']
	for i := 1; i < len(word) && len(code) < 4; i++ {
		c := word[i] | 0x20
		d := codes[c-'a']
		switch {
		case c == 'h' || c == 'w':
			// h and w do not separate letters with the same code.
			continue
		case d == '0':
			// Vowels (and y) do.
			last = '0'
		case d != last:
			code = append(code, d)
			last = d
		}
	}
	for len(code) < 4 {
		code = append(code, '0')
	}
	return string(code)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
