# LLM post-processing

An optional stage after [text cleanup](architecture.md#text-cleanup) ([#39](https://github.com/opragmatico/goscriber/issues/39)) that has a language model fix what the speech model got wrong (misheard words and terms, numbers, punctuation, grammar) before the text is pasted, and in the [files](architecture.md#transcribing-files) the History page transcribes. This page records how it works and the measurements behind the decisions: how Goscriber talks to providers, how it handles long transcripts and `<unk>`, which models it suggests and what the prompt says. The code is in `internal/llm`.

## Setting it up

The settings window's **Post-processing** page turns it on for every dictation and picks the provider first: Gemini, OpenAI, OpenRouter, Groq, Ollama, or Other, any server with an OpenAI-compatible API (LM Studio, llama.cpp's `llama-server`). Claude is not offered for now: the code for it stays, and a settings file that names it (`llm_provider: "claude"`), or `--llm-url https://api.anthropic.com`, still uses it, and the page then shows it as chosen. Only the rows that provider needs follow: an API key for the hosted ones (optional for Other), a base URL for Ollama (where it runs, this computer by default) and Other, then the model and the prompt; **Try it** has the model clean a sample dictation with them. Picking a provider points the base URL at it and clears the model, which the next provider would not serve; Other keeps an address of no known provider and otherwise starts from LM Studio's.

The settings are `llm_enabled` (off by default), `llm_provider` (`gemini`, `openai`, `claude`, `openrouter`, `groq`, `ollama` or `custom`; `ollama` by default), `llm_base_url` (set from the provider, typed for `ollama` and `custom`), `llm_model` (empty: the provider's default, `claude-sonnet-5-5` for Claude, `gemini-4-hydrogen` for Gemini, `gpt-6.1-sol` for OpenAI, each its maker's model at Claude Sonnet's level, and the [suggested model](#models) for a provider on this computer; OpenRouter, Groq and other servers need one chosen) and `llm_prompt` (empty: the [built-in prompt](#prompt)). Settings files from before `llm_provider` (schema version 2) get the provider at their base URL, or `custom` for any other address. An API key is saved to the OS keychain (the macOS Keychain, the Secret Service on Linux, the Windows Credential Manager) as "Goscriber LLM API key", under the provider's base URL, so each provider keeps its own and none is in `settings.json`; Ollama's requests carry none.

The **post-processing shortcut** (`post_process_shortcut`, on the General page) dictates as the dictation shortcut does and post-processes that one dictation even with `llm_enabled` off, as Handy's `transcribe_with_post_process` binding does; its defaults are Handy's, `option+shift+space` on macOS, `ctrl+shift+space` on Windows and `ctrl+alt+shift+space` on Linux. The coordinator marks the session (`SessionInfo.PostProcess`), and `llm.Stage` cleans a transcript when either that or `llm_enabled` says so. Handy registers the shortcut only while post-processing is on, and its first shortcut never post-processes; Goscriber keeps the second registered and lets `llm_enabled` decide for the first, so either way of working fits. Clearing the shortcut gives its keys back. [Shortcuts](architecture.md#shortcuts) has how the two share the activation mode.

The [history](architecture.md#history) keeps the model's output (`processed_text`) beside the speech model's transcript (`raw_text`). **Process again** on a History entry post-processes that transcript once more with the settings as they are now, even with `llm_enabled` off, so switching the provider, model or prompt and processing again compares them on the same input without transcribing the audio again. A failure keeps the saved text.

`goscriber transcribe --llm FILE` cleans a file's transcript from the command line with the same settings, whether `llm_enabled` is on or not; `--llm-model` and `--llm-url` override them (`--llm-url` picks the provider at that address, or a custom server), and `--word` adds custom words to the prompt's `${custom_words}` as well as to the speech model's. It asks the provider for its models before decoding any audio, so a server that is down or lacks the model fails at once, naming the models it has. With `--json`, `text` is the cleaned transcript and `raw_text` the speech model's.

## Requests

Goscriber speaks OpenAI's Chat Completions to every provider but Claude, which it speaks to through Anthropic's Go SDK and its Messages API ([Claude](#claude)):

| Provider | Base URL |
| --- | --- |
| Gemini | `https://generativelanguage.googleapis.com/v1beta/openai` |
| OpenAI | `https://api.openai.com/v1` |
| Claude | `https://api.anthropic.com` (Messages) |
| OpenRouter | `https://openrouter.ai/api/v1` |
| Groq | `https://api.groq.com/openai/v1` |
| Ollama | `http://localhost:11434/v1` |
| Other: LM Studio | `http://localhost:1234/v1` |
| Other: llama.cpp (`llama-server`) | `http://localhost:8080/v1` |

- **Endpoint.** `POST {base URL}/chat/completions`, with `Authorization: Bearer KEY` when the provider needs a key; `GET {base URL}/models` lists the models to pick from. The settings list a hosted provider's models only once its key is stored.
- **Body.** `model`; `messages`, one user message holding the prompt with the transcript in it; `stream: false`; `reasoning_effort`, as below; and the output cap below.
- **No reasoning.** A model that thinks before it answers is far too slow for dictation: with thinking on, `qwen3.5:4b` took 58 to 118 s per short transcript, against a median of 0.45 s with it off. Requests carry `reasoning_effort: "none"`, which turns thinking off on Ollama and Gemini 2.5 Flash. A server that rejects it (400 or 422) gets the request again with `"minimal"`, then `"low"` (the least `openai/gpt-oss-120b` on Groq takes), then without the field; Goscriber starts from the first value that got through for that base URL and model for as long as the app runs, as Handy remembers a rejected field.
- **Gemini thinks anyway.** Google documents that thinking cannot be turned off on Gemini 2.5 Pro or Gemini 3, and that Gemini 3 Flash takes `"none"` as `"minimal"`. `gemini-4-hydrogen` takes `"none"` and rejects `"minimal"`, but still thought for 1,065 to 1,194 tokens on a 387-word chunk, about as much as at `"low"` (943 to 1,192). The thinking counts toward `max_tokens`, so a Gemini request carries at least 16,000, as Claude's do; at 1,200 that chunk's answer was cut off.
- **Output cap.** Every request carries `max_tokens`; OpenAI's own API gets `max_completion_tokens` instead, which all its chat models take and its reasoning models require. Ollama sets no limit of its own, so a model that starts repeating itself runs on until the request times out. A response that stops at the cap (`finish_reason: "length"`) counts as failed.
- **Thinking at medium.** The default models of OpenAI and Gemini, `gpt-6.1-sol` and `gemini-4-hydrogen`, are the exception: they are always asked with `reasoning_effort: "medium"`, with no step-down, and since their thinking counts toward the output cap (`max_completion_tokens` for OpenAI, `max_tokens` for Gemini), the cap gets 16,000 tokens on top of the answer's, and a request may take 2 minutes for each 400 words of its chunk. `gpt-6.1-sol` allows `"low"` at the least. At `"medium"`, `gemini-4-hydrogen` thought for 2,338 to 2,452 tokens on a 387-word chunk and took 16 to 18 s, against 9.5 s at `"none"`; a short dictation took 3 to 4 s.
- **Failures.** A request that fails, outlasts its timeout (30 s for each 400 words of its chunk, 2 minutes for a model that thinks at medium), stops at the cap or comes back empty leaves its text as cleanup left it, like any failing [text stage](architecture.md#dictation-pipeline), so a dictation never loses its text to the LLM; the user gets a notice (`llm_failed`) that opens the Post-processing page.

## Claude

Not offered on the Post-processing page for now (see [Setting it up](#setting-it-up)); what follows holds where it is still used.

Claude goes through Anthropic's official Go SDK (`anthropic-sdk-go`), not an OpenAI-compatible endpoint: `POST /v1/messages` with the key the SDK sends, one user message, and the models from `GET /v1/models`.

- **Thinking** is turned off: requests carry `thinking: {"type": "between_tools"}`, under which the model thinks only between tool calls, and these requests have none; current models reject `"disabled"` and point to it, and reject a thinking budget (`"enabled"`). They also ask for `output_config.effort: "low"`. On `claude-sonnet-5-5`, effort `"low"` alone already did next to no thinking: a 387-word chunk took 820 output tokens and 4.4 s either way, the 2,920-word talk 6,350 and 28.5 s. But with thinking off, the 3,999-word chunk whose last 1,079 words repeated its first came back at 2,899 to 2,914 words, the whole first copy, where effort `"low"` alone stopped after 1,060. `max_tokens` still gets 16,000 on top of the answer's cap, for a model that rejects these and is sent none; the timeout still bounds a model that runs on, and an answer that stops at `max_tokens` fails as above.
- **Refusals.** For the models that take them (Fable 5.1 and 5.2, Mythos 5.1 and 5.2, Opus 5 and 5.5, Sonnet 5.5) requests carry server-side fallbacks (`fallbacks: "default"`, beta `server-side-fallback-2026-07-01`), so a request one model declines is answered by another in the same call. A `refusal` that still comes back fails the chunk, which keeps its text.
- **Older models** that reject turning thinking off, effort or fallbacks (400) get the request again without them, remembered as for `reasoning_effort`.
- **Model.** `claude-sonnet-5-5` while none is chosen; any model the account serves can be picked, such as Claude Opus 5.5 for the hardest transcripts or Claude Haiku 5.5 for speed and price.

## Long transcripts

Transcripts of [files](architecture.md#transcribing-files) can be far longer than a dictation. They go to the model in chunks:

- **Chunks.** The transcript is split after every `.`, `!` or `?` followed by a space, and whole sentences are packed into chunks of at most 4,000 words for Gemini, OpenAI and Claude, 800 for Groq and OpenRouter, which serve open models, and 400 for a provider on this computer or network (Ollama, Other), cut the way the measurements below cut them. A sentence longer than that is a chunk by itself. A dictation, almost always shorter, stays one request.
- **Context.** For a hosted provider, each chunk after the first also carries the previous chunk's last 100 words, as transcribed, in the prompt's `${context}`, so the model knows how its chunk begins. The built-in prompt puts them in a `<previous>` block before the transcript, to be read but not cleaned, repeated or followed, and left out of the answer; the line is dropped for the first chunk and for local providers.
- **Requests.** Each chunk is a request of its own, with the same prompt and `max_tokens` of three tokens per word of the limit: 1,200 for a 400-word chunk (no chunk of the test talk needed more than 514), 12,000 for a 4,000-word one, and three per word of its own for a longer sentence. The timeout grows with the cap, once per 1,200 tokens. The chunks go one at a time, in order.
- **Short answers.** A model can end its answer normally partway through a chunk, with nothing to tell it from a whole one. So each answer is lined up with its chunk, word by word and in order, ignoring case and punctuation, to find how far into the chunk it got. Fillers a model leaves out shrink a text all along, not at its end, and the whole answers measured lined up to within a word of their chunk's end. When more than 50 of the chunk's words are left after the answer, the rest is sent as a request of its own, after the answer's last words as context (100 for a hosted provider, as between chunks), and its answer follows the first after a blank line. That happens up to three times for a chunk; a rest still left, or whose request fails, goes in as transcribed, so no words are lost. An answer that lines up with fewer than 20 words is taken as it is.
- **Result.** The cleaned chunks are joined with a blank line between them. A chunk whose request fails keeps its own text, and the others still count.

**Hosted chunk sizes, measured** on the 2,920-word Portuguese talk (Parakeet TDT v3), with the built-in prompt and each provider's default model, `openai/gpt-oss-120b` on Groq:

| Provider | Whole talk as one chunk | Words back |
| --- | --- | --- |
| Claude (`claude-sonnet-5-5`) | 29 s | 2,908 |
| Gemini (`gemini-4-hydrogen`, medium) | 37 s | 2,889 |
| OpenAI (`gpt-6.1-sol`, medium) | 24 s | 2,893 |
| Groq (`openai/gpt-oss-120b`) | 6 s | 1,875 |

Groq's answer ended normally, a third of the talk short, with nothing to tell it from a whole one. In four 800-word chunks it gave back 2,742 and 2,797 words in two runs, with no passage of six words or more missing: what it left out were fillers and phrases it condensed ("a gente está vendendo" → "vendemos"), against the prompt. In every request with context (1,433-word chunks to the three others, 800-word ones to Groq) none of the 100 words of context came back in the answer. Claude, given a 3,999-word chunk whose last 1,079 words repeated its first, returned only its first 1,059 to 1,077 words, ending normally (`stop_reason: "end_turn"`), streamed or not; the talk itself came back whole. With the rest sent again, the doubled talk came back at 5,748 of 5,840 words, in 60 s and one more request, with both copies whole and nothing repeated or missing where the answers meet. Groq's on-demand tier allows 8,000 tokens a minute, which a long file's chunks exceed whatever their size (429).

Sent in one request, the 4,886-word test talk below made `gemma4:26b` repeat a sentence over and over in 3 of 5 attempts, and with no cap Ollama let it run past 10 minutes. None of the 65 chunked requests to the three Gemma models looped, and chunking was also quicker: 69 s for the talk, against 94 to 107 s for the single requests that finished.

## `<unk>`

A transcript can hold `<unk>` where the speech model had no token for a character. Cleanup replaces it before the LLM sees the text, so the fix holds with post-processing off too: right after a number, in digits or as a word in the transcript's language (`10<unk>`, `noventa<unk>`), it becomes `%`; anywhere else, `:`, including between two numbers, as in a time (`10<unk>30` → `10:30`), which the talk did not have. Number words are known for English, Portuguese, Spanish, French, German and Italian; with the language unknown, as with Parakeet TDT v3, all of them count. That gives the right sign at all 37 places in the test talk (5 `%`, 32 `:`). The models removed every `<unk>` as well, but not always for the right sign: `com 10<unk>` came out as `com 10%` in 2 of 7 runs, and as `com 10` or `com 10 pessoas` in the rest.

## Models

For a local provider Goscriber suggests a model by the machine's memory, and uses it while `llm_model` is empty; any model the provider serves can be picked instead. A Linux kernel reports a little less than the memory installed, so the line is drawn at 30 GiB.

| Memory | Suggested model | Download |
| --- | --- | --- |
| 32 GB or more | `gemma4:26b` | 16 GB |
| Less | `gemma4:e4b` | 9.5 GB |

The downloads are as Ollama installed them on the test Mac.

`gemma4:26b` is a mixture of experts that uses 3.8B of its 26B parameters per token, so it writes faster than the dense `gemma4:12b` while fixing as much as any model tried on the long talk. It rebuilt garbled phrases from context ("Demoração de pensar" became "Deixa eu me apresentar") and kept the speaker's informal register, though it also guessed where the audio was lost, giving the speaker a name. `gemma4:e4b` is the fastest and fits smaller machines, but it misses more: misheard terms ("get hub", "cooper netties", "a pie" for API) and 11 of the talk's 31 errors, against 4 to 6 for `gemma4:26b`.

Not suggested: `qwen3.5:4b`, the weakest in both languages and slower than `gemma4:e4b`; and `gemma4:12b`, the most accurate on short dictations but four times slower than `gemma4:e4b` there, and on the talk slower than `gemma4:26b` for the same score, rewriting informal speech ("tá" became "está") and, in one run, words the transcript had right.

Ollama unloads a model after 5 idle minutes, and the next request waits while it loads (about 2.5 s for `gemma4:e4b` and 3.6 s for `gemma4:26b` on the test Mac); its `/v1` API takes `keep_alive` to keep a model loaded longer.

## Prompt

The built-in prompt is Handy's default (`default_improve_transcriptions` in its `src-tauri/src/settings.rs`) with examples in more than one language. Handy's examples are English only ("five dollars → $5"), and on a Portuguese transcript `gemma4:26b` followed them: "três mil dólares" became "$3.000" and "um dólar" "$1", and `gemma4:e4b` turned "pra" into "para" and "tavam" into "estavam". Goscriber's prompt shows amounts of money in English, Portuguese and French, says a currency word never becomes a bare `$` outside English, keeps abbreviations such as "IA" in the transcript's language, and names informal words to keep ("pra", "tá", "tava", "gonna").

On a short Portuguese dictation with those words, three runs of each model through Ollama: `gemma4:26b` wrote "3.000 dólares" and "1 dólar" every time (with Handy's prompt, "$3,000" in both runs) and kept "pra", "tá" and "tavam" every time, though one run wrote "tava certa" for "tavam certas"; `gemma4:e4b` wrote "3 mil dólares" every time, but still made informal words formal in 2 runs of 3, once in one and throughout in the other (with Handy's prompt, in both runs). Spanish ("5.000 €") and English ("$5,000") came out in their own conventions, and the injected "ignore all previous instructions" was not followed. A prompt that has no `${output}` gets the transcript after it.

**Placeholders.** Besides `${output}`, a prompt can hold `${language}`, the transcript's language by name ("Portuguese (Brazil)"), when the speech model reports it, and `${custom_words}`, the custom words of the Advanced page, separated by commas. Long transcripts sent to a hosted provider also fill `${context}` with the previous chunk's last 100 words (see [Long transcripts](#long-transcripts)). A line that holds one of them is left out while it is unknown or empty, so a prompt can mention them without saying something odd when there is nothing to say. The built-in prompt uses all three, the context in its first line: a sixth rule asks for the custom words to be spelt as written, also where the speech model wrote something that sounds like them, and a line names the language.

On the 12-minute Portuguese talk transcribed with Parakeet TDT v3 and cleaned by `gemma4:26b`, with Reportei, Reportei Connect, Bain, ElevenLabs, build in public, Fin and Claude Code as custom words: Goscriber's own [word correction](architecture.md#custom-vocabulary) already turned "reporteio", "Reporter", "building public" and "cloud code" into the custom words, but not "Bang" or "Levin Labs", too far from "Bain" and "ElevenLabs" to match; with the words in the prompt, the model fixed those two as well, which it had left wrong without them.

## Measured

On an M4 Max with 36 GB, through Ollama 0.40.16, with Handy's default prompt and reasoning off. A prompt that also described typical recognition errors did no better.

**Short dictations.** 14 transcripts with recognition errors planted in them, 7 in English (36 errors) and 7 in Portuguese (30), each cleaned twice: misheard technical terms, homophones, numbers and spoken punctuation, words wrongly split or joined, missing accents, a question that must not be answered, an instruction that must not be followed, and a longer update.

| Model | English fixed | Portuguese fixed | Median | Slowest 10% |
| --- | --- | --- | --- | --- |
| `gemma4:12b` | 70/72 (97%) | 58/60 (97%) | 0.85 s | 1.60 s |
| `gemma4:e4b` | 62/72 (86%) | 56/60 (93%) | 0.20 s | 0.74 s |
| `qwen3.5:4b` | 54/72 (75%) | 38/60 (63%) | 0.45 s | 1.50 s |

`gemma4:26b` was not run on these.

**A long talk.** A 4,886-word talk in Portuguese as a speech model transcribed it, with 31 known errors (garbled phrases, wrong agreement, misheard terms, stray English, and 37 `<unk>`), in 400-word chunks:

| Model | Time | Errors fixed (of 31) |
| --- | --- | --- |
| `gemma4:26b` | 69 s (89 tokens/s) | 25 (27 in another run) |
| `gemma4:12b` | 109 to 114 s (54 to 57 tokens/s) | 25 in both runs |
| `gemma4:e4b` | 51 s (119 tokens/s) | 20 |

## Open points

- **Post-processing from the command line.** Handy's `--toggle-post-process` toggles a post-processed dictation in the running app; Goscriber's `--toggle-transcription` and `SIGUSR2` start plain ones, so on Wayland without the global shortcuts portal the post-processing shortcut is heard in X11 apps only.
- **Upgrading onto the new default.** A user whose dictation shortcut is already the post-processing shortcut's default (`ctrl+shift+space` on Windows, say) gets a `shortcut_failed` notice at the first launch after upgrading, until they pick other keys or clear it.
- **Unpunctuated transcripts.** A transcript with no sentence punctuation stays one chunk however long it is; every transcript measured had punctuation.
- **Other machines.** Only an M4 Max was measured. A base M-series chip has a third or less of its memory bandwidth, which bounds how fast a model generates; Linux was not measured.
- **`gemma4:26b` on short dictations** was not measured.
