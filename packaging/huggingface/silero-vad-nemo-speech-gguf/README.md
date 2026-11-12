---
license: mit
tags:
  - gguf
  - nemo-speech.cpp
  - silero-vad
  - voice-activity-detection
pipeline_tag: voice-activity-detection
---

# Silero VAD v6.2.0: GGUF for NeMo-Speech.cpp

[Silero VAD](https://github.com/snakers4/silero-vad) v6.2.0, a small voice activity detector by the Silero Team, converted to the GGUF that [NeMo-Speech.cpp](https://github.com/NVIDIA/NeMo-Speech.cpp) v0.3.0 loads as its VAD companion. NeMo-Speech.cpp uses it to mask silence before the speech recogniser sees it, and to end utterances.

It was converted for [Goscriber](https://github.com/opragmatico/goscriber), an offline push-to-talk dictation app, and works with any NeMo-Speech.cpp runtime. This is not a Silero Team or NVIDIA release.

## Files

| File | Weights | Size (bytes) | SHA-256 |
| --- | --- | --- | --- |
| `silero-vad-v6.2.0.gguf` | F32 | 1,240,224 | `83d702b11378ced4b1bbfcb53394bd00994f7382ff0b9a11b87605dd0898369d` |

## Use

Pass it next to the ASR model and turn masking on:

```bash
nemo-speech transcribe audio.wav --model parakeet-tdt-0.6b-v2.q8_0.gguf \
  --vad-model silero-vad-v6.2.0.gguf --vad-masking
```

Through the C ABI, set `model_path` and `enable_masking` in `nemo_speech_asr_vad_config`. The file holds the 16 kHz model only, and it is not the whisper.cpp Silero GGML format.

## How it was made

- **Source**: the [`silero-vad` 6.2.0](https://pypi.org/project/silero-vad/6.2.0/) package from PyPI (wheel SHA-256 `8a31f1e58e73cdc20820d842f757e678e0ae1b0ee1cb7b8aa50f20f8fd7f7638`), which carries the model weights.
- **Converter**: `convert_model.py` from NeMo-Speech.cpp [v0.3.0](https://github.com/NVIDIA/NeMo-Speech.cpp/tree/v0.3.0) (commit `20999cc9`), on Python 3.12 with hash-pinned packages.
- **Command**:

  ```bash
  python convert_model.py silero --silero-version 6.2.0 --outfile silero-vad-v6.2.0.gguf
  ```

- **Reproduce**: [`scripts/convert-models.sh`](https://github.com/opragmatico/goscriber/blob/main/scripts/convert-models.sh) in Goscriber builds this file and Parakeet TDT 0.6B v2 from scratch in an isolated environment, then checks their SHA-256 against these. See [docs/models/gguf.md](https://github.com/opragmatico/goscriber/blob/main/docs/models/gguf.md).

The weights are copied as they are, in F32: nothing is quantised or retrained.

## License and attribution

Silero VAD is © 2020-present Silero Team and released under the MIT License. This GGUF is the same model in another file format and keeps that licence: see [LICENSE](LICENSE), copied from the `silero-vad` 6.2.0 package.
