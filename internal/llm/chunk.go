package llm

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ChunkWords is the most words a request carries. Sent whole, a 4,886-word
// talk made gemma4:26b repeat a sentence over and over in 3 of 5 attempts;
// in 400-word chunks none of 65 requests looped, and the talk was done
// sooner (docs/llm-post-processing.md#long-transcripts).
const ChunkWords = 400

// TokensPerWord sizes each request's max_tokens: three tokens per word of
// its chunk, and never less than for a full one (1,200). No chunk of the
// test talk needed more than 514.
const TokensPerWord = 3

// maxTokens is the output cap for a chunk of words words.
func maxTokens(words int) int { return TokensPerWord * max(words, ChunkWords) }

// Split cuts text into chunks of whole sentences of at most maxWords words
// each, in order. A sentence ends after a ".", "!" or "?" followed by a
// space; one longer than maxWords is a chunk by itself, and so is a text
// with no sentence ends at all. Each chunk is trimmed; a blank text has
// none.
func Split(text string, maxWords int) []string {
	var chunks []string
	start, words := -1, 0 // the chunk being packed: where it starts, its words
	for _, s := range sentences(text) {
		n := len(strings.Fields(text[s.from:s.to]))
		if n == 0 {
			continue
		}
		if start >= 0 && words+n > maxWords {
			chunks = append(chunks, strings.TrimSpace(text[start:s.from]))
			start, words = -1, 0
		}
		if start < 0 {
			start = s.from
		}
		words += n
	}
	if start >= 0 {
		chunks = append(chunks, strings.TrimSpace(text[start:]))
	}
	return chunks
}

// span is a part of a string, from byte offset from to byte offset to.
type span struct{ from, to int }

// sentences cuts text after every ".", "!" or "?" that a space follows,
// covering all of it.
func sentences(text string) []span {
	var out []span
	from := 0
	for i, r := range text {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		next, _ := utf8.DecodeRuneInString(text[i+1:])
		if unicode.IsSpace(next) {
			out = append(out, span{from, i + 1})
			from = i + 1
		}
	}
	if from < len(text) || len(out) == 0 {
		out = append(out, span{from, len(text)})
	}
	return out
}
