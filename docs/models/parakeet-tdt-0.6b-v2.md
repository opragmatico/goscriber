# Spike: Parakeet TDT 0.6B v2 on NeMo-Speech.cpp

**Outcome: it works.** NeMo-Speech.cpp v0.3.0 converts and runs [Parakeet TDT 0.6B v2](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v2) with the accuracy its model card reports, fast enough for dictation on Metal and on the CPU. Goscriber can go ahead with publishing the GGUFs ([#12](https://github.com/opragmatico/goscriber/issues/12)) and use v2, not the officially supported v3, as its English model.

| Question | Answer |
| --- | --- |
| Does the converter handle v2? | Yes. It detects a `tdt` head and writes the duration set `[0, 1, 2, 3, 4]`. Q8_0 is 700 MB, F16 1.28 GB. |
| Is the accuracy intact? | Yes. LibriSpeech WER at Q8_0 is 1.63 % (test-clean) and 3.17 % (test-other), against 1.69 % and 3.19 % on the model card. F16 is no better. |
| Punctuation, capitalisation, timestamps? | All three come through. |
| Streaming? | Rejected by the library, as for v3. Goscriber's binding refuses first with `ErrInvalidArgument`. |
| Fast enough for dictation? | Yes. Once warm, a 5 s utterance decodes in 40 ms on Metal and 160 ms on the CPU; a 2-minute clip in 1.0 s and 2.3 s. |
| Memory? | A footprint of 320–380 MB on Metal for clips up to 2 minutes, plus the memory-mapped 700 MB model. 0.3–1.2 GB on the CPU. |
| 24 minutes in one pass? | It decodes, in 22 s on Metal, but not in one pass: NeMo-Speech.cpp splits audio longer than 6 min 40 s at pauses. Some long inputs come back with whole stretches missing, and NVIDIA's NeMo does the same with them. See [Long audio](#long-audio). |
| Handy's transcribe.cpp GGUF? | Incompatible, as expected: NeMo-Speech.cpp refuses its `parakeet` architecture. |

Everything below was measured on an M4 Max (36 GB) with macOS 15.8 and NeMo-Speech.cpp v0.3.0 built by `make deps` (the `metal-asr` preset), through Goscriber's own CGo binding.

## Conversion

The converter ran in an isolated Python 3.12 environment with NeMo-Speech.cpp's `requirements.txt` resolved and hash-pinned (PyTorch 2.15.0, NumPy 1.26.4, gguf 0.19.0, librosa 0.11.0). Python 3.14, the Homebrew default, cannot install the NumPy 1.26 the converter pins.

```bash
python convert_model.py nvidia/parakeet-tdt-0.6b-v2 \
  --revision ae9ad07059c7c739ffaf932226a8fe64ae2620b0 \
  --outtype q8_0 --outfile parakeet-tdt-0.6b-v2.q8_0.gguf     # and --outtype fp16
```

The converter downloads only the 2.47 GB `.nemo` and needs no NeMo toolkit. It reports:

```text
[convert] head_type = tdt
[convert] d_model=1024 n_layers=24 n_heads=8 d_ff=4096 conv_kernel=9 subsample=8 feat_in=128 xscaling=False
[convert] rnnt vocab_size=1025 blank_id=1024 pred_hidden=640 joint_dim=640 max_symbols_per_step=10
[convert] cache_supported=False train_left_ctx=0 train_right_ctx=0
[convert] emitted 699 tensors, skipped 26
[convert] dtype tally: F16=30, F32=397, Q8_0=272
```

The GGUF records `asr.head_type = tdt`, `asr.tdt.durations = [0, 1, 2, 3, 4]`, `asr.encoder.cache_supported = false`, full-context attention (`offline_left_ctx = offline_right_ctx = -1`), the 1,024-token English SentencePiece vocabulary, and the tokenizer itself (`asr.tokenizer.spm_model`), which word boosting needs. The 26 skipped tensors are the preprocessor's window and filterbank, which the converter recomputes with librosa, and batch-norm counters.

Conversion takes about 45 s, most of it the download; from the Hugging Face cache it takes 7 s. The converter stores the output's file name as `general.name`, so a file's bytes, and its SHA-256, depend on what it is called.

## Accuracy

The full LibriSpeech test-clean (2,620 utterances, 5.4 h) and test-other (2,939 utterances, 5.3 h) sets, one utterance per decode, scored with the Whisper English normaliser that the Hugging Face Open ASR Leaderboard, and so NVIDIA's model card, use. Corpus WER, on Metal:

| | test-clean | test-other |
| --- | --- | --- |
| Q8_0 | 1.63 % | 3.17 % |
| F16 | 1.62 % | 3.19 % |
| Model card (NeMo, PyTorch) | 1.69 % | 3.19 % |

- **Q8_0 costs nothing measurable.** The two files agree word for word on 2,552 of the 2,620 test-clean utterances. Q8_0 should be the default, as planned.
- **The remaining errors are the usual ones**: proper names (*Dedalus* → *Daedalus*), homophones (*harts* → *hearts*), and British spellings. No utterance came back empty.
- **Metal and the CPU agree.** Every clip up to 10 minutes in the timing runs below gave the same transcript on both backends.
- Decoding all 5,559 utterances one by one ran at about 125× real time on Metal.

## Punctuation, capitalisation and timestamps

```text
And so, my fellow Americans, ask not what your country can do for you, ask what you can do for your country.
```

That is `testdata/audio/jfk.wav`. When `Options.WordTimestamps` is set, every word comes back with a timestamp, carrying the punctuation attached to it (`so,`, `Americans,`). The timestamps come from the TDT durations, so each word ends where the next starts unless the model emitted blanks between them: `And` 0.24–0.56 s, `so,` 0.56–1.04 s, `my` 1.04–1.28 s, … `ask` 3.28–3.60 s, `not` 4.24–4.56 s. They mark where the model emitted each word; they are not a forced alignment.

## Streaming

Rejected, as for v3. Called directly through the C ABI, `nemo_speech_asr_streaming_recognize` fails with `NEMO_SPEECH_ASR_ERROR_RUNTIME`:

```text
this transducer encoder is offline-only and cannot serve StreamingRecognize
```

Goscriber's binding never gets that far: it reads `asr.encoder.cache_supported = false` at load, reports `ModelInfo.Streaming = false`, and `StartStream` returns `ErrInvalidArgument` ([#10](https://github.com/opragmatico/goscriber/issues/10)). `TestTDTRefusesToStream`, which #10 had to skip, passes against this GGUF. Live text for Parakeet stays a chunked decode at pauses ([#24](https://github.com/opragmatico/goscriber/issues/24)).

## Latency and memory

One clip per length, made of consecutive LibriSpeech test-clean utterances. *Cold* is the first decode after loading the model; *warm* is the best of five later decodes. Loading takes 60–120 ms once the file is in the page cache.

| Clip | Q8_0 Metal, cold / warm | Q8_0 CPU, cold / warm | F16 Metal, warm | F16 CPU, warm |
| --- | --- | --- | --- | --- |
| 5 s | 80 / 40 ms | 194 / 160 ms | 41 ms | 171 ms |
| 30 s | 224 / 185 ms | 652 / 611 ms | 185 ms | 623 ms |
| 2 min | 1.03 / 0.98 s | 2.52 / 2.34 s | 0.97 s | 2.54 s |

| Clip | Q8_0 Metal | Q8_0 CPU | F16 Metal | F16 CPU |
| --- | --- | --- | --- | --- |
| 5 s | 317 MB | 341 MB | 382 MB | 407 MB |
| 30 s | 331 MB | 509 MB | 396 MB | 579 MB |
| 2 min | 380 MB | 1,154 MB | 446 MB | 1,226 MB |

The second table is the peak physical footprint (`/usr/bin/time -l`), what macOS charges the process. It leaves out the GGUF itself: NeMo-Speech.cpp memory-maps it, and its pages are clean file pages that macOS can drop and read again.

- **Dictation-length audio is fast on both backends.** The first decode after a load costs 40 ms extra on Metal, which a warm-up decode at load can absorb ([#29](https://github.com/opragmatico/goscriber/issues/29)).
- **F16 brings nothing.** It is as fast as Q8_0 on Metal, slower on the CPU, larger on disk and in memory, and no more accurate.
- **The CPU is a usable fallback**, 2.5–4× slower than Metal, but its memory grows with the clip length.

### Long audio

NeMo-Speech.cpp v0.3.0 caps a full-context pass at the positional encoding's 5,000 encoder frames (`asr.encoder.pos_emb_max_len`), which is 400 s, or 6 min 40 s, of audio. Past that, its offline runner splits the audio at the quietest 100 ms window near each 400 s mark and decodes the segments one after another through one decoder, so the transcript and the timestamps continue across segments. NeMo itself would extend the positional encoding instead. That is why the model card can promise 24 minutes in a single pass; here it is never a single pass.

Q8_0, one decode each:

| Clip | Segments | Metal | CPU | WER | WER decoding each utterance alone |
| --- | --- | --- | --- | --- | --- |
| 5 min | 1 | 4.0 s, 425 MB | 8.2 s, 2.6 GB | 0.83 % | 0.83 % |
| 10 min | 2 | 7.8 s, 471 MB | 16.7 s, 3.8 GB | 2.86 % | 2.37 % |
| 24 min | 4 | 21.8 s, 675 MB | 42.7 s, 4.2 GB | 30.6 % | 1.88 % |

Memory and time stay modest. The 24-minute result does not: the first segment, nearly seven minutes, came back with no text at all, while the other three were fine. The cause is not the splitting:

- Decoded on its own, that segment also comes back empty, and so do windows of the clip as short as **90 s**. The first 60 s of the clip decode fine and the first 90 s do not; in another segment, the first 300 s decode fine and the first 312 s do not. In a sweep of windows from 3 to 6.5 minutes long across the clip, 13 of 77 came back empty. The F16 file fails on the 312 s input too.
- **NVIDIA's NeMo 3.0 does the same.** Loaded from the original `.nemo` in PyTorch, it also returns nothing for the 90 s, 150 s and 312 s inputs that fail here. This is how the model behaves, not a NeMo-Speech.cpp or quantisation bug.
- Every input that failed is spliced from utterances of different LibriSpeech speakers and recordings (as are many that worked). In the 90 s case, the loudness halves from 64 s and the speaker changes at 69 s; raising the quieter part to match, or inserting a second of silence at 64 s, does not help.
- **Single-speaker audio did not fail.** All 87 test-clean chapters (one speaker, one recording, 17 s to 8 min 17 s, median 3 min 10 s), each decoded whole, transcribe completely: 1.54 % WER, no chapter missing more than 10 % of its words. The 11 chapters longer than 400 s were split and stitched cleanly. None of the 5,559 single utterances, up to 35 s, came back empty either.

Dictation is one speaker on one microphone, so the risk is low, but the failure is silent: no error, only missing text. Goscriber should therefore:

- Decode Parakeet recordings in chunks split at pauses, of at most about 30 s, rather than whole. No input that short failed here, and the chunked decode for live text ([#24](https://github.com/opragmatico/goscriber/issues/24)) cuts recordings at pauses anyway.
- Treat an empty or very short transcript of a long stretch of speech as a failure, not as silence. The VAD ([#16](https://github.com/opragmatico/goscriber/issues/16)) knows how much speech there was.

## Handy's transcribe.cpp GGUF

[`handy-computer/parakeet-tdt-0.6b-v2-gguf`](https://huggingface.co/handy-computer/parakeet-tdt-0.6b-v2-gguf) (`parakeet-tdt-0.6b-v2-Q8_0.gguf`, revision `58239cf6`) does not load, as expected:

```text
Unknown general.architecture='parakeet'. Supported: 'asr' (preferred), 'parakeet-ctc'.
```

Goscriber's binding rejects it before the library sees it (`not a NeMo-Speech.cpp ASR model (architecture "parakeet")`). The formats differ throughout, not just in the architecture name: transcribe.cpp's file keeps its hyperparameters under `stt.parakeet.*` and `stt.frontend.*`, names its tensors `enc.*`, and has no `asr.*` keys. Bridging them would take a converter between the two formats, and converting from the `.nemo` is simpler.

## Reproduce

```bash
make deps
export PKG_CONFIG_PATH="$PWD/.deps/nemo-speech/lib/pkgconfig"
go build -tags nemo -o bin/nemo-eval ./internal/engine/nemo/cmd/nemo-eval

# LibriSpeech from https://www.openslr.org/12, converted to 16 kHz mono WAV, with a NeMo manifest per set
bin/nemo-eval -model parakeet-tdt-0.6b-v2.q8_0.gguf -manifest test-clean.jsonl > q8-clean.jsonl
uv run scripts/wer.py q8-clean.jsonl

bin/nemo-eval -model parakeet-tdt-0.6b-v2.q8_0.gguf -words testdata/audio/jfk.wav      # timestamps
bin/nemo-eval -model parakeet-tdt-0.6b-v2.q8_0.gguf -stream                            # streaming refusal
/usr/bin/time -l bin/nemo-eval -model parakeet-tdt-0.6b-v2.q8_0.gguf -runs 5 clip.wav  # latency, footprint
```

`nemo-eval` decodes through `internal/engine/nemo`, the binding the app uses. Upstream's `nemo-speech` CLI calls the same library, but Goscriber's SDK build leaves it out. The NeMo cross-check used `nemo_toolkit[asr]` 3.0.0 in a separate environment, with `ASRModel.restore_from` on the `.nemo` and `transcribe` on the CPU.
