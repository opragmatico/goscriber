---
license: cc-by-4.0
language:
  - en
base_model: nvidia/parakeet-tdt-0.6b-v2
base_model_relation: quantized
pipeline_tag: automatic-speech-recognition
tags:
  - gguf
  - nemo-speech.cpp
  - parakeet
  - tdt
  - fastconformer
---

# Parakeet TDT 0.6B v2: GGUF for NeMo-Speech.cpp

NVIDIA's [Parakeet TDT 0.6B v2](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v2), an English speech recognition model, converted to the GGUF that [NeMo-Speech.cpp](https://github.com/NVIDIA/NeMo-Speech.cpp) v0.3.0 runs. NVIDIA publishes only the `.nemo` checkpoint for v2, and NeMo-Speech.cpp officially lists only its multilingual v3 sibling, which has the same FastConformer-TDT architecture.

These files were converted for [Goscriber](https://github.com/opragmatico/goscriber), an offline push-to-talk dictation app, and work with any NeMo-Speech.cpp runtime. This is not an NVIDIA release.

## Files

| File | Weights | Size (bytes) | SHA-256 |
| --- | --- | --- | --- |
| `parakeet-tdt-0.6b-v2.q8_0.gguf` | Q8_0 linear and pointwise-convolution weights; the rest F16/F32 (recommended) | 700,133,856 | `ad2c2e6ccd12c0f0a23ccd4f3a7d1145978d59a7172a10f3ba23db117909d0cd` |
| `parakeet-tdt-0.6b-v2.f16.gguf` | F16 linear and pointwise-convolution weights; the rest F32 | 1,278,057,440 | `894ee6efa3a35bee3654171b85bb473e07479c420678997c6e63523fe571c2a3` |

## Use

```bash
nemo-speech transcribe audio.wav --model parakeet-tdt-0.6b-v2.q8_0.gguf
```

- **English only**, with punctuation and capitalisation in the transcript, and word timestamps.
- **Offline only.** The model decodes whole utterances; NeMo-Speech.cpp rejects streaming requests for it, as it does for v3.
- **Prefer short inputs.** NeMo-Speech.cpp splits audio longer than 6 min 40 s at pauses and decodes the pieces in turn. Some longer inputs, all spliced from several speakers in our tests, came back with whole stretches missing, in NVIDIA's NeMo as well; nothing up to 35 s did. For long recordings, decode chunks split at pauses.
- These files load only in NeMo-Speech.cpp. The `parakeet-tdt-0.6b-v2` GGUFs made for transcribe.cpp (as used by Handy) are a different format, and neither runtime loads the other's.

## Accuracy

Word error rate on LibriSpeech, after the Whisper English normalisation of the Hugging Face Open ASR Leaderboard, decoded by NeMo-Speech.cpp v0.3.0 on Apple Silicon (Metal):

| | test-clean | test-other |
| --- | --- | --- |
| This repository, Q8_0 | 1.63% | 3.17% |
| This repository, F16 | 1.62% | 3.19% |
| NVIDIA's model card (NeMo, PyTorch) | 1.69% | 3.19% |

See Goscriber's [evaluation](https://github.com/opragmatico/goscriber/blob/main/docs/models/parakeet-tdt-0.6b-v2.md) for the method, the latency and memory measurements, and the long-audio limits.

## How these files were made

- **Source**: [`nvidia/parakeet-tdt-0.6b-v2`](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v2/tree/ae9ad07059c7c739ffaf932226a8fe64ae2620b0) at revision `ae9ad07059c7c739ffaf932226a8fe64ae2620b0`, file `parakeet-tdt-0.6b-v2.nemo` (SHA-256 `d99e39955c9d3d0350d8fb7c75e40c64a2b2eaeb003883d7c941fd2e8747b28c`).
- **Converter**: `convert_model.py` from NeMo-Speech.cpp [v0.3.0](https://github.com/NVIDIA/NeMo-Speech.cpp/tree/v0.3.0) (commit `20999cc9`), on Python 3.12 with hash-pinned packages (PyTorch 2.15.0, NumPy 1.26.4, gguf 0.19.0, librosa 0.11.0). It detects the TDT head and stores the duration set `[0, 1, 2, 3, 4]` in the GGUF.
- **Commands**:

  ```bash
  python convert_model.py nvidia/parakeet-tdt-0.6b-v2 --revision ae9ad07059c7c739ffaf932226a8fe64ae2620b0 \
    --outtype q8_0 --outfile parakeet-tdt-0.6b-v2.q8_0.gguf
  python convert_model.py nvidia/parakeet-tdt-0.6b-v2 --revision ae9ad07059c7c739ffaf932226a8fe64ae2620b0 \
    --outtype fp16 --outfile parakeet-tdt-0.6b-v2.f16.gguf
  ```

- **Reproduce**: [`scripts/convert-models.sh`](https://github.com/opragmatico/goscriber/blob/main/scripts/convert-models.sh) in Goscriber builds these files and the Silero VAD companion from scratch in an isolated environment, then checks their SHA-256 against the ones above. See [docs/models/gguf.md](https://github.com/opragmatico/goscriber/blob/main/docs/models/gguf.md).

## License and attribution

Parakeet TDT 0.6B v2 is © NVIDIA and released under the [Creative Commons Attribution 4.0 International](https://creativecommons.org/licenses/by/4.0/) licence (CC-BY-4.0). These files are an adaptation of it under the same licence. The changes: the checkpoint was converted from NeMo's `.nemo` format to GGUF, and in the Q8_0 file the linear and pointwise-convolution weights were quantised to 8 bits; the F16 file stores them at 16 bits. The model was not retrained or fine-tuned. NVIDIA does not endorse these files.

Cite the original model as its [model card](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v2) asks.
