package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/opragmatico/goscriber/internal/config"
	"github.com/opragmatico/goscriber/internal/pipeline"
)

func TestSplit(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		max  int
		want []string
	}{
		{"blank", "  ", 5, nil},
		{"one short text", "Just a few words here.", 5, []string{"Just a few words here."}},
		{
			"packs whole sentences", "One two. Three four! Five six? Seven.", 4,
			[]string{"One two. Three four!", "Five six? Seven."},
		},
		{"exact fit", "A b. C d.", 4, []string{"A b. C d."}},
		{
			"long sentence alone", "Short one. This sentence is far too long to fit. End here.", 4,
			[]string{"Short one.", "This sentence is far too long to fit.", "End here."},
		},
		{"no sentence ends", "words and words and words and words", 3, []string{"words and words and words and words"}},
		{"no space after the mark", "Version 2.5 is out.Now what? Fine.", 3, []string{"Version 2.5 is out.Now what?", "Fine."}},
		{"trailing space", "Hello. ", 5, []string{"Hello."}},
		{
			"newlines count as spaces", "First line.\nSecond line.\n\nThird line.", 4,
			[]string{"First line.\nSecond line.", "Third line."},
		},
		{"ellipsis", "Wait... what? Yes.", 2, []string{"Wait... what?", "Yes."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Split(tc.text, tc.max); !slices.Equal(got, tc.want) {
				t.Errorf("Split(%q, %d) = %q, want %q", tc.text, tc.max, got, tc.want)
			}
		})
	}
}

// A 4,886-word talk goes in chunks of at most 400 words that hold every
// word once, in order.
func TestSplitLongTranscript(t *testing.T) {
	var b strings.Builder
	for i := range 700 {
		fmt.Fprintf(&b, "Sentence %d has exactly seven words here. ", i)
	}
	text := strings.TrimSpace(b.String())
	chunks := Split(text, ChunkWords)
	if len(chunks) != 13 { // 4,900 words, 57 sentences of 7 per chunk
		t.Errorf("%d chunks, want 13", len(chunks))
	}
	for i, c := range chunks {
		if n := len(strings.Fields(c)); n > ChunkWords {
			t.Errorf("chunk %d has %d words", i, n)
		}
	}
	if strings.Join(chunks, " ") != text {
		t.Error("the chunks do not add up to the transcript")
	}
}

func TestRender(t *testing.T) {
	if got := Render("Fix: ${output}. Again: ${output}", "hi", Vars{}); got != "Fix: hi. Again: hi" {
		t.Errorf("Render = %q", got)
	}
	if got := Render("Fix the transcript below.\n", "hi there", Vars{}); got != "Fix the transcript below.\n\nhi there" {
		t.Errorf("Render without the placeholder = %q", got)
	}
	got := Render("", "olá", Vars{})
	if !strings.HasPrefix(got, "<transcript>\nolá\n</transcript>") || strings.Contains(got, "${") {
		t.Errorf("Render with the default prompt = %q", got)
	}
}

// The language and custom words fill their placeholders, and a line that
// holds one is left out while it has nothing to say.
func TestRenderVars(t *testing.T) {
	prompt := "Clean this.\nIt is in ${language}.\nSpell: ${custom_words}\n${output}"
	for _, tc := range []struct {
		name string
		vars Vars
		want string
	}{
		{
			"both",
			Vars{Language: "Portuguese (Brazil)", Words: []string{"Reportei", "Bain"}},
			"Clean this.\nIt is in Portuguese (Brazil).\nSpell: Reportei, Bain\nolá",
		},
		{"no words", Vars{Language: "French"}, "Clean this.\nIt is in French.\nolá"},
		{"no language", Vars{Words: []string{"Fin"}}, "Clean this.\nSpell: Fin\nolá"},
		{"neither", Vars{}, "Clean this.\nolá"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Render(prompt, "olá", tc.vars); got != tc.want {
				t.Errorf("Render = %q, want %q", got, tc.want)
			}
		})
	}
	// A placeholder in the transcript is the speaker's, not the prompt's.
	if got := Render("Fix: ${output}", "say ${language}", Vars{Language: "English"}); got != "Fix: say ${language}" {
		t.Errorf("Render = %q", got)
	}
	got := Render("", "olá", Vars{Language: "Portuguese", Words: []string{"Reportei"}})
	for _, want := range []string{"The transcript is in Portuguese.", "sounds like them: Reportei"} {
		if !strings.Contains(got, want) {
			t.Errorf("the default prompt lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<previous>") {
		t.Errorf("the default prompt mentions earlier text without any:\n%s", got)
	}
}

// The context goes in its placeholder, on a line that is left out without
// it; like the transcript, a placeholder it holds stays as it was said.
func TestRenderContext(t *testing.T) {
	prompt := "After: ${context}\n[${output}]"
	if got := Render(prompt, "olá", Vars{Context: "the end before"}); got != "After: the end before\n[olá]" {
		t.Errorf("with context: %q", got)
	}
	if got := Render(prompt, "olá", Vars{}); got != "[olá]" {
		t.Errorf("without context: %q", got)
	}
	if got := Render(prompt, "olá", Vars{Context: "say ${output}"}); got != "After: say ${output}\n[olá]" {
		t.Errorf("a placeholder in the context: %q", got)
	}
	got := Render("", "olá", Vars{Context: "a frase anterior"})
	if !strings.HasPrefix(got, "The transcript below continues this earlier text, given only for context: <previous>a frase anterior</previous>") {
		t.Errorf("the default prompt with context:\n%s", got)
	}
}

func TestTidyAnswer(t *testing.T) {
	for in, want := range map[string]string{
		"  Clean text.\n":                           "Clean text.",
		"<think>hmm</think>\n\nClean text.":         "Clean text.",
		"<transcript>\nClean text.\n</transcript>":  "Clean text.",
		"<think>unfinished":                         "<think>unfinished",
		"Text that mentions <transcript> in it.":    "Text that mentions <transcript> in it.",
		"<think></think><transcript>x</transcript>": "x",
	} {
		if got := tidyAnswer(in); got != want {
			t.Errorf("tidyAnswer(%q) = %q, want %q", in, got, want)
		}
	}
}

// upper is a fake model that answers each transcript in capitals, and
// fails those that contain "FAIL" the way fail says.
func upper(fail func() (int, string)) func(req map[string]any) (int, string) {
	return func(req map[string]any) (int, string) {
		p := userText(req)
		text := p[strings.Index(p, "[")+1 : strings.LastIndex(p, "]")]
		if strings.Contains(text, "fail") {
			return fail()
		}
		return 200, reply(strings.ToUpper(text), "stop")
	}
}

func TestCleanChunks(t *testing.T) {
	f := newFakeServer(t, upper(nil))
	var c Client
	var sentences []string
	for i := range 120 {
		sentences = append(sentences, fmt.Sprintf("this is sentence number %d.", i)) // 5 words
	}
	var progress [][2]int
	ctx := WithProgress(context.Background(), func(done, total int) { progress = append(progress, [2]int{done, total}) })
	res := c.Clean(ctx, Job{Endpoint: f.endpoint(), Model: "m", Prompt: "[${output}]"}, strings.Join(sentences, " "))
	if res.Chunks != 2 || res.Failed != 0 || res.Err != nil {
		t.Fatalf("Clean: %d chunks, %d failed, %v; want 2, 0", res.Chunks, res.Failed, res.Err)
	}
	want := strings.ToUpper(strings.Join(sentences[:80], " ") + "\n\n" + strings.Join(sentences[80:], " "))
	if res.Text != want {
		t.Errorf("Text = %q, want the chunks cleaned and joined with a blank line", res.Text)
	}
	for _, r := range f.sent() {
		if r["max_tokens"] != float64(1200) {
			t.Errorf("max_tokens = %v, want 1200", r["max_tokens"])
		}
	}
	if want := [][2]int{{0, 2}, {1, 2}, {2, 2}}; !slices.Equal(progress, want) {
		t.Errorf("progress = %v, want %v", progress, want)
	}
}

// A job with its own chunk size cuts at it, and each chunk after the first
// carries the previous chunk's last words as context, which the answer
// does not repeat.
func TestCleanContext(t *testing.T) {
	f := newFakeServer(t, upper(nil))
	var c Client
	var sentences []string
	for i := range 8 {
		sentences = append(sentences, fmt.Sprintf("this is sentence number %d.", i)) // 5 words
	}
	job := Job{Endpoint: f.endpoint(), Model: "m", Prompt: "After <${context}>\n[${output}]", ChunkWords: 20, ContextWords: 3}
	res := c.Clean(context.Background(), job, strings.Join(sentences, " "))
	if res.Chunks != 2 || res.Failed != 0 {
		t.Fatalf("Clean: %d chunks, %d failed, %v; want 2, 0", res.Chunks, res.Failed, res.Err)
	}
	if want := strings.ToUpper(strings.Join(sentences[:4], " ") + "\n\n" + strings.Join(sentences[4:], " ")); res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
	sent := f.sent()
	if p := userText(sent[0]); strings.Contains(p, "After") {
		t.Errorf("the first chunk has context: %q", p)
	}
	if p := userText(sent[1]); !strings.HasPrefix(p, "After <sentence number 3.>\n[this is sentence number 4.") {
		t.Errorf("the second chunk's prompt: %q", p)
	}
}

// Gemini, OpenAI and Claude take 4,000-word chunks, Groq and OpenRouter
// 800-word ones, both with 100 words of context; a provider on the user's
// own computer or network takes 400-word chunks with none.
func TestProviderChunking(t *testing.T) {
	want := map[config.LLMProvider][2]int{
		config.LLMGemini:     {HostedChunkWords, ContextWords},
		config.LLMOpenAI:     {HostedChunkWords, ContextWords},
		config.LLMClaude:     {HostedChunkWords, ContextWords},
		config.LLMOpenRouter: {OpenModelChunkWords, ContextWords},
		config.LLMGroq:       {OpenModelChunkWords, ContextWords},
		config.LLMOllama:     {ChunkWords, 0},
		config.LLMCustom:     {ChunkWords, 0},
	}
	for _, p := range Providers {
		words, ctxWords := p.Chunking()
		if [2]int{words, ctxWords} != want[p.ID] {
			t.Errorf("%s: Chunking = %d, %d; want %v", p.Name, words, ctxWords, want[p.ID])
		}
		if hosted := want[p.ID][1] > 0; hosted != p.Hosted() {
			t.Errorf("%s: Hosted = %v", p.Name, p.Hosted())
		}
	}
	if len(want) != len(Providers) {
		t.Errorf("%d providers, want %d", len(Providers), len(want))
	}
	set := config.Defaults()
	set.LLMProvider, set.LLMBaseURL = config.LLMClaude, config.LLMClaude.BaseURL()
	job, err := (&Stage{Client: &Client{}}).Job(set)
	if err != nil || job.ChunkWords != HostedChunkWords || job.ContextWords != ContextWords {
		t.Errorf("the stage's job for Claude: %+v, %v", job, err)
	}
}

// shortAnswers answers a bracketed transcript in capitals, but only its
// first keep words of it when it has more than over; fail, when set, is
// the answer from the request numbered failAt (counting from 1).
func shortAnswers(over, keep int, failAt int) func(req map[string]any) (int, string) {
	n := 0
	return func(req map[string]any) (int, string) {
		n++
		if n == failAt {
			return 500, `{"error":"boom"}`
		}
		p := userText(req)
		words := strings.Fields(p[strings.Index(p, "[")+1 : strings.LastIndex(p, "]")])
		if len(words) > over {
			words = words[:keep]
		}
		return 200, reply(strings.ToUpper(strings.Join(words, " ")), "stop")
	}
}

// A chunk whose answer stops short gets its rest sent again, after the
// answer's last words as context, and the answers are joined.
func TestCleanContinuesAShortAnswer(t *testing.T) {
	f := newFakeServer(t, shortAnswers(150, 150, 0))
	var c Client
	text := talkOf(60) // 300 words
	words := strings.Fields(text)
	job := Job{Endpoint: f.endpoint(), Model: "m", Prompt: "After <${context}>\n[${output}]", ContextWords: 3}
	res := c.Clean(context.Background(), job, text)
	if res.Chunks != 1 || res.Failed != 0 || res.Continued != 1 {
		t.Fatalf("Clean: %d chunks, %d failed, %d continued; want 1, 0, 1", res.Chunks, res.Failed, res.Continued)
	}
	want := strings.ToUpper(strings.Join(words[:150], " ") + "\n\n" + strings.Join(words[150:], " "))
	if res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
	sent := f.sent()
	if len(sent) != 2 {
		t.Fatalf("%d requests, want 2", len(sent))
	}
	if p := userText(sent[1]); !strings.HasPrefix(p, "After <SENTENCE NUMBER 29.>\n[this is sentence number 30.") {
		t.Errorf("the rest's prompt: %q", p)
	}
}

// A chunk that keeps stopping short gets maxContinuations more requests,
// and the rest still left goes in as transcribed; a rest whose request
// fails goes in as transcribed too, keeping what was cleaned.
func TestCleanContinuationLimits(t *testing.T) {
	text := talkOf(60) // 300 words
	words := strings.Fields(text)

	f := newFakeServer(t, shortAnswers(0, 30, 0))
	var c Client
	res := c.Clean(context.Background(), Job{Endpoint: f.endpoint(), Model: "m", Prompt: "[${output}]"}, text)
	var want []string
	for i := range maxContinuations + 1 {
		want = append(want, strings.ToUpper(strings.Join(words[30*i:30*(i+1)], " ")))
	}
	want = append(want, strings.Join(words[30*(maxContinuations+1):], " "))
	if res.Text != strings.Join(want, "\n\n") || res.Continued != maxContinuations || res.Failed != 0 {
		t.Errorf("always short: %d continued, %d failed, text %q", res.Continued, res.Failed, res.Text)
	}
	if n := len(f.sent()); n != maxContinuations+1 {
		t.Errorf("%d requests, want %d", n, maxContinuations+1)
	}

	f = newFakeServer(t, shortAnswers(150, 150, 2))
	res = c.Clean(context.Background(), Job{Endpoint: f.endpoint(), Model: "m", Prompt: "[${output}]"}, text)
	if want := strings.ToUpper(strings.Join(words[:150], " ")) + "\n\n" + strings.Join(words[150:], " "); res.Text != want || res.Failed != 0 {
		t.Errorf("failed rest: %d failed, text %q", res.Failed, res.Text)
	}

	// An answer that leaves out a few words at the end, or matches too
	// little to judge, is taken as it is.
	f = newFakeServer(t, shortAnswers(0, len(words)-40, 0))
	if res = c.Clean(context.Background(), Job{Endpoint: f.endpoint(), Model: "m", Prompt: "[${output}]"}, text); res.Continued != 0 {
		t.Errorf("40 words short: %d continued, want 0", res.Continued)
	}
	f = newFakeServer(t, shortAnswers(0, alignedAtLeast-5, 0))
	if res = c.Clean(context.Background(), Job{Endpoint: f.endpoint(), Model: "m", Prompt: "[${output}]"}, text); res.Continued != 0 {
		t.Errorf("an answer too short to judge: %d continued, want 0", res.Continued)
	}
}

// Claude is kept out of the choices for now, unless it is the provider
// already chosen.
func TestChoices(t *testing.T) {
	names := func(ps []Provider) (out []string) {
		for _, p := range ps {
			out = append(out, p.Name)
		}
		return out
	}
	if got := names(Choices(config.LLMOllama)); slices.Contains(got, "Claude") || len(got) != len(Providers)-1 {
		t.Errorf("choices %v, want every provider but Claude", got)
	}
	if got := names(Choices(config.LLMClaude)); !slices.Equal(got, names(Providers)) {
		t.Errorf("choices with Claude chosen %v, want them all", got)
	}
	if p := ProviderByID(config.LLMClaude); p.API != Messages || p.Model != ClaudeModel {
		t.Errorf("Claude is still a provider for settings that name it: %+v", p)
	}
}

// A chunk that fails, stops at the cap or comes back empty keeps its own
// text; the others are cleaned.
func TestCleanFailedChunkKeepsItsText(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func() (int, string)
		want error
	}{
		{"server error", func() (int, string) { return 500, `{"error":"boom"}` }, nil},
		{"stopped at the cap", func() (int, string) { return 200, reply("FAIL FAIL FAIL", "length") }, ErrTruncated},
		{"empty answer", func() (int, string) { return 200, reply("  ", "stop") }, ErrEmptyAnswer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t, upper(tc.fail))
			var c Client
			one := strings.Repeat("first part goes well. ", 100)   // 400 words
			two := strings.Repeat("second part will fail. ", 100)  // 400 words
			three := strings.Repeat("third part goes well. ", 100) // 400 words
			text := strings.TrimSpace(one + two + three)
			res := c.Clean(context.Background(), Job{Endpoint: f.endpoint(), Model: "m", Prompt: "[${output}]"}, text)
			if res.Chunks != 3 || res.Failed != 1 || res.Err == nil {
				t.Fatalf("Clean: %d chunks, %d failed, %v; want 3 chunks, 1 failed", res.Chunks, res.Failed, res.Err)
			}
			if tc.want != nil && !errors.Is(res.Err, tc.want) {
				t.Errorf("Err = %v, want %v", res.Err, tc.want)
			}
			want := strings.ToUpper(strings.TrimSpace(one)) + "\n\n" + strings.TrimSpace(two) + "\n\n" + strings.ToUpper(strings.TrimSpace(three))
			if res.Text != want {
				t.Errorf("Text = %.80q…, want the failed chunk as it was", res.Text)
			}
		})
	}
}

// Once the context is done, the chunks left keep their text and no more
// requests go out.
func TestCleanCancelled(t *testing.T) {
	f := newFakeServer(t, func(map[string]any) (int, string) { return 200, reply("DONE", "stop") })
	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithProgress(ctx, func(done, _ int) {
		if done == 1 {
			cancel()
		}
	})
	var c Client
	text := strings.TrimSpace(strings.Repeat("a b c d. ", 100) + strings.Repeat("e f g h. ", 100))
	res := c.Clean(ctx, Job{Endpoint: f.endpoint(), Model: "m"}, text)
	if len(f.sent()) != 1 || res.Failed != 1 || !errors.Is(res.Err, context.Canceled) {
		t.Errorf("%d requests, %d failed, %v; want 1 request, the second chunk failed", len(f.sent()), res.Failed, res.Err)
	}
	if !strings.HasSuffix(res.Text, "\n\n"+strings.TrimSpace(strings.Repeat("e f g h. ", 100))) {
		t.Errorf("the second chunk did not keep its text: %.60q", res.Text)
	}
}

func TestStage(t *testing.T) {
	f := newFakeServer(t, upper(func() (int, string) { return 500, `{"error":"boom"}` }))
	var failures []error
	var keys []string
	stage := &Stage{
		Client: &Client{},
		Key: func(baseURL string) (string, error) {
			keys = append(keys, baseURL)
			return "sk-test", nil
		},
		Memory: 36 << 30,
		Failed: func(err error) { failures = append(failures, err) },
		Log:    slog.New(slog.DiscardHandler),
	}
	set := config.Defaults()
	set.LLMProvider, set.LLMBaseURL = config.LLMCustom, f.endpoint().BaseURL
	set.LLMPrompt = "[${output}]"
	info := pipeline.SessionInfo{Settings: set}

	// Off: the text goes on as it is, and nothing is sent.
	if got, err := stage.Process(context.Background(), "hello there.", info); err != nil || got != "hello there." {
		t.Errorf("off: %q, %v", got, err)
	}
	if len(f.sent()) != 0 {
		t.Error("a request went out with post-processing off")
	}

	info.Settings.LLMEnabled = true
	got, err := stage.Process(context.Background(), "hello there.", info)
	if err != nil || got != "HELLO THERE." {
		t.Errorf("on: %q, %v", got, err)
	}
	if r := f.sent()[0]; r["model"] != LargeModel || f.auth[0] != "Bearer sk-test" {
		t.Errorf("sent model %v with %q; want the suggested model and the stored key", r["model"], f.auth[0])
	}
	if len(keys) != 1 || keys[0] != set.LLMBaseURL {
		t.Errorf("keys asked for %v", keys)
	}

	// Nothing to clean: nothing is sent.
	n := len(f.sent())
	if got, err := stage.Process(context.Background(), "", info); err != nil || got != "" || len(f.sent()) != n {
		t.Errorf("empty: %q, %v", got, err)
	}

	// Every chunk failed: an error, so the coordinator keeps its input, and
	// the user hears about it.
	if _, err := stage.Process(context.Background(), "this will fail.", info); err == nil {
		t.Error("no error with the only chunk failing")
	}
	if len(failures) != 1 {
		t.Errorf("%d failures reported, want 1", len(failures))
	}

	// Part failed: the rest is cleaned, and the user hears about it.
	text := strings.TrimSpace(strings.Repeat("this one goes well. ", 100) + strings.Repeat("this one will fail. ", 100))
	got, err = stage.Process(context.Background(), text, info)
	if err != nil || !strings.HasPrefix(got, "THIS ONE GOES WELL.") || !strings.HasSuffix(got, "this one will fail.") {
		t.Errorf("partly failed: %.40q…, %v", got, err)
	}
	if len(failures) != 2 || !strings.Contains(failures[1].Error(), "1 of 2 parts failed") {
		t.Errorf("failures = %v", failures)
	}

	// Ollama takes no key: the keychain is not asked, and none is sent.
	info.Settings.LLMProvider = config.LLMOllama
	n, asked := len(f.sent()), len(keys)
	if _, err := stage.Process(context.Background(), "hello there.", info); err != nil {
		t.Fatal(err)
	}
	if len(keys) != asked || f.auth[n] != "" {
		t.Errorf("Ollama: keys asked for %v, sent %q", keys, f.auth[n])
	}

	// A hosted provider with no model chosen fails before sending anything.
	info.Settings.LLMProvider = config.LLMGroq
	n = len(f.sent())
	if _, err := stage.Process(context.Background(), "hello.", info); err == nil || len(f.sent()) != n {
		t.Error("no error without a model for a hosted provider")
	}
}
