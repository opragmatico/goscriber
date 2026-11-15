package text

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// universalFillers are hesitation sounds that are not words in any
// language the models write, so they are removed whatever the transcript's
// language. Anything that is a word somewhere ("um" in Portuguese and
// German, "ah", "eh", "mm" for millimetres) belongs in languageFillers.
// After Handy's list.
var universalFillers = []string{
	"uh", "uhm", "umm", "uhh", "uhhh", "ehh", "ehm", "ahm", "hmm", "hm", "mmm", "хм", "ммм",
}

// languageFillers are fillers that are words in other languages, removed
// only from transcripts known to be in the language they belong to, by
// base language code.
var languageFillers = map[string][]string{
	"en": {"um", "ah", "eh"},
	"de": {"äh", "ähm"},
	"fr": {"euh"},
}

// Fillers returns the built-in filler words for a transcript in language:
// a locale or language code such as "en-US" or "de", or "" or "auto" when
// the language is not known, which gets only the fillers that are no word
// in any language.
func Fillers(language string) []string {
	base, _, _ := strings.Cut(strings.ToLower(language), "-")
	base, _, _ = strings.Cut(base, "_")
	return slices.Concat(universalFillers, languageFillers[base])
}

// RemoveFillers removes the filler words and phrases in fillers from s,
// ignoring case. A filler matches whole words only, and also with its last
// letter drawn out ("ummm" for "umm"). The punctuation around a removed
// filler is tidied:
//
//   - its own comma goes with it: "So, um, I think" becomes "So, I think";
//   - a sentence end it carried moves to the word before, replacing a comma
//     there, when a new sentence follows or nothing does: "It works, um."
//     becomes "It works."; before a word in lower case it is dropped;
//   - a filler that opened a sentence hands its capital to the next word:
//     "Um, so I think" becomes "So I think";
//   - brackets or quotes that enclosed only the filler go too, while a
//     quote that opened before it stays with the next word.
//
// Whitespace between words comes out as single spaces.
func RemoveFillers(s string, fillers []string) string {
	phrases := fillerPhrases(fillers)
	if len(phrases) == 0 || strings.TrimSpace(s) == "" {
		return s
	}
	tokens := strings.Fields(s)
	var kept []string
	carry := ""      // an opening bracket or quote to put before the next kept word
	capital := false // the next kept word opens a sentence a filler opened
	pendingEnd := "" // sentence punctuation a filler carried, waiting to see what follows
	for i := 0; i < len(tokens); {
		n := matchFiller(tokens[i:], phrases)
		if n == 0 {
			t := tokens[i]
			if pendingEnd != "" {
				if startsUpper(t) {
					kept = endSentence(kept, pendingEnd)
				}
				pendingEnd = ""
			}
			if capital {
				t = capitalize(t)
				capital = false
			}
			kept = append(kept, carry+t)
			carry = ""
			i++
			continue
		}

		first, last := tokens[i], tokens[i+n-1]
		prefix, _ := splitPunct(first)
		_, suffix := splitPunct(last)
		core := withoutPunct(first)
		if startsUpper(core) && opensSentence(kept) {
			capital = true
		}
		// Brackets and quotes: those the filler opened and closed go
		// with it; one it opened alone waits for the next word, and one
		// it closed alone stays with the word before.
		opened, closed := cancelPairs(splitEnclosers(prefix), closers(suffix))
		carry += opened
		if closed != "" && len(kept) > 0 {
			kept[len(kept)-1] += closed
		}
		if end := sentenceEnd(suffix); end != "" && len(kept) > 0 {
			pendingEnd = end
		}
		i += n
	}
	if pendingEnd != "" {
		kept = endSentence(kept, pendingEnd)
	}
	// A quote a filler opened at the very end has nothing to attach to,
	// so carry is dropped.
	return strings.Join(kept, " ")
}

// fillerPhrases are fillers as lists of lower-case words, the longest
// first, so "you know" is tried before "you".
func fillerPhrases(fillers []string) [][]string {
	var out [][]string
	for _, f := range Vocabulary(fillers) {
		words := strings.Fields(strings.ToLower(f))
		for i, w := range words {
			words[i] = withoutPunct(w)
		}
		if words = slices.DeleteFunc(words, func(w string) bool { return w == "" }); len(words) > 0 {
			out = append(out, words)
		}
	}
	slices.SortStableFunc(out, func(a, b []string) int { return len(b) - len(a) })
	return out
}

// matchFiller returns how many of tokens, from the first, a filler phrase
// matches, or 0. A phrase never spans punctuation between its words.
func matchFiller(tokens []string, phrases [][]string) int {
	for _, p := range phrases {
		if len(p) > len(tokens) {
			continue
		}
		ok := true
		for j, w := range p {
			prefix, suffix := splitPunct(tokens[j])
			if (j > 0 && prefix != "") || (j < len(p)-1 && suffix != "") || !sameFiller(withoutPunct(tokens[j]), w) {
				ok = false
				break
			}
		}
		if ok {
			return len(p)
		}
	}
	return 0
}

// sameFiller reports whether word is filler, ignoring case, or filler with
// its last letter drawn out: "Ummm" for "umm", "hmmmm" for "hmm".
func sameFiller(word, filler string) bool {
	word = strings.ToLower(word)
	if !strings.HasPrefix(word, filler) {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(filler)
	if !unicode.IsLetter(last) {
		return word == filler
	}
	for _, r := range word[len(filler):] {
		if r != last {
			return false
		}
	}
	return true
}

// sentenceEnd returns the sentence-ending punctuation in suffix (".", "?",
// "!", "…" and runs of them), or "".
func sentenceEnd(suffix string) string {
	var b strings.Builder
	for _, r := range suffix {
		if isSentenceEnd(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isSentenceEnd(r rune) bool { return strings.ContainsRune(".?!…", r) }

// endSentence ends the last kept word's sentence with end, replacing a
// comma, semicolon, colon or dash it ended with, unless it ends one
// already.
func endSentence(kept []string, end string) []string {
	if len(kept) == 0 {
		return kept
	}
	w := kept[len(kept)-1]
	// Closing quotes and brackets stay outside the new punctuation's
	// place: "yes,” um." becomes "yes.”".
	body := strings.TrimRightFunc(w, isCloser)
	closing := w[len(body):]
	if r, _ := utf8.DecodeLastRuneInString(body); isSentenceEnd(r) {
		return kept
	}
	body = strings.TrimRight(body, ",;:—–-")
	if body == "" {
		return kept
	}
	kept[len(kept)-1] = body + end + closing
	return kept
}

// opensSentence reports whether a word after kept starts a sentence:
// nothing comes before it, or the last word ends one.
func opensSentence(kept []string) bool {
	if len(kept) == 0 {
		return true
	}
	body := strings.TrimRightFunc(kept[len(kept)-1], isCloser)
	r, _ := utf8.DecodeLastRuneInString(body)
	return isSentenceEnd(r)
}

// Openers and their closers, for brackets and quotes around a filler.
const (
	openers = `([{"'“‘«¿¡`
	closerS = `)]}"'”’»`
)

func isCloser(r rune) bool { return strings.ContainsRune(closerS, r) }

// splitEnclosers returns the brackets and quotes in a filler's prefix.
func splitEnclosers(prefix string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(openers, r) {
			return r
		}
		return -1
	}, prefix)
}

// closers returns the closing brackets and quotes in a filler's suffix.
func closers(suffix string) string {
	return strings.Map(func(r rune) rune {
		if isCloser(r) {
			return r
		}
		return -1
	}, suffix)
}

// cancelPairs drops each opener in opened whose closer is in closed, and
// that closer: a filler in brackets, "(um)", leaves nothing behind. Spanish
// ¿ and ¡ open the sentence, not the filler, so they always stay.
func cancelPairs(opened, closed string) (string, string) {
	pairs := map[rune]rune{'(': ')', '[': ']', '{': '}', '"': '"', '\'': '\'', '“': '”', '‘': '’', '«': '»'}
	var keepOpen strings.Builder
	for _, o := range opened {
		c, ok := pairs[o]
		if !ok {
			keepOpen.WriteRune(o) // ¿ ¡
			continue
		}
		if i := strings.IndexRune(closed, c); i >= 0 {
			closed = closed[:i] + closed[i+utf8.RuneLen(c):]
			continue
		}
		keepOpen.WriteRune(o)
	}
	return keepOpen.String(), closed
}

func startsUpper(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.IsUpper(r)
		}
	}
	return false
}

// capitalize upper-cases the first letter of s, after any leading
// punctuation.
func capitalize(s string) string {
	i := strings.IndexFunc(s, isWordRune)
	if i < 0 {
		return s
	}
	r, size := utf8.DecodeRuneInString(s[i:])
	return s[:i] + string(unicode.ToUpper(r)) + s[i+size:]
}

// Tidy collapses runs of whitespace to single spaces and trims the ends. A
// text with no letter or digit left, such as the lone "." a model may
// write for a short clip, becomes "", so nothing is pasted.
func Tidy(s string) string {
	if strings.IndexFunc(s, isWordRune) < 0 {
		return ""
	}
	return strings.Join(strings.Fields(s), " ")
}
