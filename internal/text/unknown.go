package text

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// UnknownToken is what a speech model writes where it has no token for a
// character, such as "%" or ":" for Parakeet TDT 0.6B v3.
const UnknownToken = "<unk>"

// ReplaceUnknown replaces each UnknownToken in s with the sign it stands
// for: "%" right after a number, written in digits or as a number word of
// the transcript's language ("10<unk>", "noventa<unk>"), and ":" anywhere
// else, including between two numbers, as in a time ("10<unk>30"). Spaces
// between the number and the token do not matter. Language is
// a locale or language code such as "pt-BR", or "" when it is not known,
// in which case the number words of every language known here count.
//
// On a 4,886-word Portuguese talk this gave the right sign at all 37
// places (docs/llm-post-processing.md#unk).
func ReplaceUnknown(s, language string) string {
	if !strings.Contains(s, UnknownToken) {
		return s
	}
	words := numberWordsFor(language)
	var b strings.Builder
	b.Grow(len(s))
	for {
		i := strings.Index(s, UnknownToken)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		s = s[i+len(UnknownToken):]
		next, _ := utf8.DecodeRuneInString(s)
		if endsWithNumber(b.String(), words) && !unicode.IsDigit(next) {
			b.WriteByte('%')
		} else {
			b.WriteByte(':')
		}
	}
}

// endsWithNumber reports whether s, less trailing spaces, ends with a
// digit or one of words.
func endsWithNumber(s string, words numberWords) bool {
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	r, _ := utf8.DecodeLastRuneInString(s)
	if unicode.IsDigit(r) {
		return true
	}
	start := strings.LastIndexFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && r != '-' })
	word := strings.ToLower(s[start+1:])
	// Compounds such as "twenty-five" and "quatre-vingt" end in a number word.
	if i := strings.LastIndexByte(word, '-'); i >= 0 {
		word = word[i+1:]
	}
	return word != "" && words.has(word)
}

// numberWords are the number words of a language, or of several.
type numberWords struct {
	words map[string]bool
	// suffixes end compound number words written as one word, such as
	// German "fünfundzwanzig".
	suffixes []string
}

func (n numberWords) has(word string) bool {
	if n.words[word] {
		return true
	}
	for _, s := range n.suffixes {
		if strings.HasSuffix(word, s) {
			return true
		}
	}
	return false
}

// numberWordList holds the number words that can come before a percent
// sign, by base language code: the units, teens, tens, hundreds and
// larger, as a speech model writes them. Compounds of several words end in
// one of these.
var numberWordList = map[string]string{
	"en": "zero one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen " +
		"sixteen seventeen eighteen nineteen twenty thirty forty fifty sixty seventy eighty ninety " +
		"hundred thousand million billion",
	"pt": "zero um uma dois duas três quatro cinco seis sete oito nove dez onze doze treze catorze quatorze " +
		"quinze dezesseis dezasseis dezessete dezassete dezoito dezenove dezanove vinte trinta quarenta " +
		"cinquenta sessenta setenta oitenta noventa cem cento duzentos duzentas trezentos trezentas " +
		"quatrocentos quatrocentas quinhentos quinhentas seiscentos seiscentas setecentos setecentas " +
		"oitocentos oitocentas novecentos novecentas mil milhão milhões bilhão bilhões",
	"es": "cero uno una dos tres cuatro cinco seis siete ocho nueve diez once doce trece catorce quince " +
		"dieciséis diecisiete dieciocho diecinueve veinte veintiuno veintidós veintitrés veinticuatro " +
		"veinticinco veintiséis veintisiete veintiocho veintinueve treinta cuarenta cincuenta sesenta " +
		"setenta ochenta noventa cien ciento doscientos trescientos cuatrocientos quinientos seiscientos " +
		"setecientos ochocientos novecientos mil millón millones",
	"fr": "zéro un une deux trois quatre cinq six sept huit neuf dix onze douze treize quatorze quinze " +
		"seize vingt vingts trente quarante cinquante soixante cent cents mille million millions", //nolint:misspell // French
	"de": "null eins ein eine zwei drei vier fünf sechs sieben acht neun zehn elf zwölf dreizehn vierzehn " +
		"fünfzehn sechzehn siebzehn achtzehn neunzehn zwanzig dreißig vierzig fünfzig sechzig siebzig " +
		"achtzig neunzig hundert tausend million millionen",
	"it": "zero uno una due tre quattro cinque sei sette otto nove dieci undici dodici tredici quattordici " +
		"quindici sedici diciassette diciotto diciannove venti trenta quaranta cinquanta sessanta " +
		"settanta ottanta novanta cento mille mila milione milioni",
}

// numberWordSuffixes end the compound number words of languages that
// write them as one word.
var numberWordSuffixes = map[string][]string{
	"de": {"zwanzig", "dreißig", "vierzig", "fünfzig", "sechzig", "siebzig", "achtzig", "neunzig", "hundert", "tausend"},
}

// numberWordsFor returns the number words of language, or of every
// language in numberWordList when it is not known. A language with no list
// has none: only digits count.
func numberWordsFor(language string) numberWords {
	base, _, _ := strings.Cut(strings.ToLower(language), "-")
	base, _, _ = strings.Cut(base, "_")
	n := numberWords{words: map[string]bool{}}
	add := func(lang string) {
		for _, w := range strings.Fields(numberWordList[lang]) {
			n.words[w] = true
		}
		n.suffixes = append(n.suffixes, numberWordSuffixes[lang]...)
	}
	if base == "" || base == "auto" {
		for lang := range numberWordList {
			add(lang)
		}
	} else {
		add(base)
	}
	return n
}
