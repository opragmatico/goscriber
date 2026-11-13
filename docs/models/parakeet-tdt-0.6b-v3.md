# Parakeet TDT 0.6B v3 checks

**Outcome: it works.** NeMo-Speech.cpp v0.3.0 lists [Parakeet TDT 0.6B v3](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3) as a supported model, and NVIDIA's own Q8_0 GGUF runs through Goscriber's binding with no changes. It joins the catalog as the offline option for European languages. For English, v2 stays the better choice.

| Question | Answer |
| --- | --- |
| Does the file match the pin? | Yes. 713,975,456 bytes, SHA-256 `e3880d0a…`, at revision `541d1f99c6b0`, the same pin as NeMo-Speech.cpp's index ([Model files](gguf.md#parakeet-tdt-06b-v3-nvidias-own-file)). |
| English? | `testdata/audio/jfk.wav` comes back word for word, on Metal and on the CPU. The model card reports 1.93 % (test-clean) and 3.59 % (test-other) on LibriSpeech, against v2's 1.69 % and 3.19 %. |
| Other languages? | Near-perfect on most of a spot check of 20 European languages. Weak on Slovenian, uneven on Danish. See [Languages](#languages). |
| Language selection? | None. The model detects the language from the audio, and NeMo-Speech.cpp ignores a language code for a model without language prompts. `--language` accepts `auto` or one of its 25 languages and changes nothing; anything else is refused. |
| Detected language reported? | No. The result has no language, so `transcribe --json` has no `language` field. |
| Punctuation, capitalisation, timestamps? | All three, as with v2. |
| Streaming? | Refused, as for v2: `transcribe --stream` stops with "does not stream". |
| VAD? | Silero VAD loads as its companion and leaves the JFK transcript unchanged. |
| Custom vocabulary? | Not expected to work: the file has no embedded SentencePiece tokenizer (`asr.tokenizer.spm_model`), which word boosting needs. |
| Speed? | Like v2. JFK (11 s) decodes in 68 ms on Metal (161× real time) and 281 ms on the CPU (39×); v2 takes 67 ms and 278 ms. |

Measured on an M4 Max (36 GB) with macOS 15.8, NeMo-Speech.cpp v0.3.0 built by `make deps`, through Goscriber's binding (`nemo-eval` and `goscriber transcribe`).

## Languages

One sentence per language, read by the macOS voice for that locale, decoded on Metal and scored by word error rate after lowercasing and removing punctuation. Synthetic speech in short, clean sentences is easy audio: this is a spot check, not a benchmark.

| Result | Languages |
| --- | --- |
| Word for word | Bulgarian, Croatian, Czech, Dutch, Finnish, French, German, Hungarian, Italian, Polish, Portuguese, Russian, Slovak, Spanish, Ukrainian |
| One word or ending off | Greek (two endings), Romanian ("azi" for "astăzi"), Swedish ("idag" for "i dag") |
| Weak | Danish: the first sentence was taken for English and written phonetically ("Come on, it will screw it…"); two more came back in Danish with about 30 % of words wrong. Slovenian: 50–100 % of words wrong in three sentences, one of them written in Cyrillic. |

Estonian, Latvian, Lithuanian and Maltese have no macOS voice and were not checked.

Choosing a language does not help: `--language de-DE` gives the same phonetic English for the Danish sentence. Turkish, which v3 does not support, comes back as phonetic nonsense, and `--language tr` is refused.

Nemotron Streaming 3.5 in automatic language detection, on the same clips, was worse overall (32 % of words wrong against v3's 11 %, excluding Turkish) and returned nothing for Danish. That comparison is unfair to Nemotron, which Goscriber can run with an explicit locale.

## Reproduce

```bash
hf download nvidia/parakeet-tdt-0.6b-v3 parakeet-tdt-0.6b-v3.q8_0.gguf --revision 541d1f99c6b0c3cd0b11a95167540bb8edefd82b
goscriber models list                                    # v3 shows as ready (Hugging Face cache)
goscriber transcribe --model parakeet-tdt-0.6b-v3 testdata/audio/jfk.wav
say -v Anna --file-format=WAVE --data-format=LEI16@16000 -o de.wav "Guten Morgen, ich möchte heute einen Brief an meine Kollegen schreiben."
goscriber transcribe --model parakeet-tdt-0.6b-v3 --json de.wav
```
