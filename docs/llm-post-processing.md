# LLM post-processing

An optional stage after [text cleanup](architecture.md#text-cleanup) ([#39](https://github.com/opragmatico/goscriber/issues/39)) that has a language model fix what the speech model got wrong (misheard words and terms, numbers, punctuation, grammar) before the text is pasted, and in the [files](architecture.md#transcribing-files) the History page transcribes. This page records how it works and the measurements behind the decisions: how Goscriber talks to providers, how it handles long transcripts and `<unk>`, which models it suggests and what the prompt says. The code is in `internal/llm`.

## Setting it up

The settings window's **Post-processing** page turns it on for every dictation and picks the provider, its base URL, the model and the prompt; **Try it** has the model clean a sample dictation with them. The settings are `llm_enabled` (off by default), `llm_base_url` (Ollama's by default), `llm_model` (empty: the [suggested model](#models), for a provider on this computer) and `llm_prompt` (empty: the [built-in prompt](#prompt)). An API key is saved to the OS keychain (the macOS Keychain, the Secret Service on Linux, the Windows Credential Manager) as "Goscriber LLM API key", under its base URL, so each provider keeps its own and none is in `settings.json`.

The **post-processing shortcut** (`post_process_shortcut`, on the General page) dictates as the dictation shortcut does and post-processes that one dictation even with `llm_enabled` off, as Handy's `transcribe_with_post_process` binding does; its defaults are Handy's, `option+shift+space` on macOS, `ctrl+shift+space` on Windows and `ctrl+alt+shift+space` on Linux. The coordinator marks the session (`SessionInfo.PostProcess`), and `llm.Stage` cleans a transcript when either that or `llm_enabled` says so. Handy registers the shortcut only while post-processing is on, and its first shortcut never post-processes; Goscriber keeps the second registered and lets `llm_enabled` decide for the first, so either way of working fits. Clearing the shortcut gives its keys back. [Shortcuts](architecture.md#shortcuts) has how the two share the activation mode.

`goscriber transcribe --llm FILE` cleans a file's transcript from the command line with the same settings, whether `llm_enabled` is on or not; `--llm-model` and `--llm-url` override them. It asks the provider for its models before decoding any audio, so a server that is down or lacks the model fails at once, naming the models it has. With `--json`, `text` is the cleaned transcript and `raw_text` the speech model's.

## Requests

Goscriber speaks one wire format, OpenAI's Chat Completions, which hosted and local providers alike offer:

| Provider | Base URL |
| --- | --- |
| OpenAI | `https://api.openai.com/v1` |
| OpenRouter | `https://openrouter.ai/api/v1` |
| Groq | `https://api.groq.com/openai/v1` |
| Ollama | `http://localhost:11434/v1` |
| LM Studio | `http://localhost:1234/v1` |
| llama.cpp (`llama-server`) | `http://localhost:8080/v1` |

- **Endpoint.** `POST {base URL}/chat/completions`, with `Authorization: Bearer KEY` when the provider needs a key; `GET {base URL}/models` lists the models to pick from.
- **Body.** `model`; `messages`, one user message holding the prompt with the transcript in it; `stream: false`; `reasoning_effort: "none"`; and the output cap below.
- **No reasoning.** A model that thinks before it answers is far too slow for dictation: with thinking on, `qwen3.5:4b` took 58 to 118 s per short transcript, against a median of 0.45 s with it off. On Ollama, `reasoning_effort: "none"` turns thinking off. A server that rejects the field (400 or 422) gets the request again without it, and Goscriber remembers that for the base URL and model, as Handy does.
- **Output cap.** Every request carries `max_tokens`; OpenAI's own API gets `max_completion_tokens` instead, which all its chat models take and its reasoning models require. Ollama sets no limit of its own, so a model that starts repeating itself runs on until the request times out. A response that stops at the cap (`finish_reason: "length"`) counts as failed.
- **Failures.** A request that fails, takes more than 30 s, stops at the cap or comes back empty leaves its text as cleanup left it, like any failing [text stage](architecture.md#dictation-pipeline), so a dictation never loses its text to the LLM; the user gets a notice (`llm_failed`) that opens the Post-processing page.

## Long transcripts

Transcripts of [files](architecture.md#transcribing-files) can be far longer than a dictation. They go to the model in chunks, cut the way the measurements below cut them:

- **Chunks.** The transcript is split after every `.`, `!` or `?` followed by a space, and whole sentences are packed into chunks of at most 400 words; a sentence longer than that is a chunk by itself. A transcript of 400 words or fewer, as almost every dictation is, stays one request.
- **Requests.** Each chunk is a request of its own, with the same prompt and `max_tokens: 1200`, three tokens per word of the limit (no chunk of the test talk needed more than 514); a single sentence longer than 400 words gets three tokens per word of its own. The chunks go one at a time, in order.
- **Result.** The cleaned chunks are joined with a blank line between them. A chunk whose request fails keeps its own text, and the others still count.

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
