package text

import "testing"

func TestReplaceUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, in, lang, want string
	}{
		{"none", "nothing to replace", "", "nothing to replace"},
		{"after digits", "com 10<unk> das pessoas", "", "com 10% das pessoas"},
		{"after a decimal", "cresceu 2,5<unk> no ano", "pt-BR", "cresceu 2,5% no ano"},
		{"after a space", "about 10 <unk> of them", "en", "about 10 % of them"},
		{"after a number word", "noventa<unk> dos casos", "", "noventa% dos casos"},
		{"number word, language known", "noventa<unk> dos casos", "pt", "noventa% dos casos"},
		{"number word of another language", "noventa<unk> dos casos", "en", "noventa: dos casos"},
		{"capitalised number word", "Fifty<unk> agreed", "en-US", "Fifty% agreed"},
		{"compound", "twenty-five<unk> of the vote", "en", "twenty-five% of the vote"},
		{"german compound", "fünfundzwanzig<unk> der Stimmen", "de-DE", "fünfundzwanzig% der Stimmen"},
		{"after a word", "Exemplo<unk> isto é um teste", "", "Exemplo: isto é um teste"},
		{"time", "às 10<unk>30", "pt", "às 10:30"},
		{"at the start", "<unk> começa assim", "", ": começa assim"},
		{"after punctuation", "disse.<unk> fim", "", "disse.: fim"},
		{"several", "Nota<unk> subiu 5<unk> e caiu cinco<unk>", "pt", "Nota: subiu 5% e caiu cinco%"},
		{"in a row", "10<unk><unk>", "", "10%:"},
		{"language without number words", "dziesięć<unk> osób", "pl", "dziesięć: osób"},
		{"word ending like a number", "someone<unk> said", "en", "someone: said"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReplaceUnknown(tc.in, tc.lang); got != tc.want {
				t.Errorf("ReplaceUnknown(%q, %q) = %q, want %q", tc.in, tc.lang, got, tc.want)
			}
		})
	}
}
