# Model files

Goscriber runs three GGUF files through NeMo-Speech.cpp. None of them is in this repository or in the app. The user downloads the model they choose from Hugging Face at a pinned revision, and Goscriber checks its SHA-256 ([#13](https://github.com/opragmatico/goscriber/issues/13)). Nobody needs Python or NeMo to use Goscriber: that is only for producing the files below.

| Model | Role | Hugging Face repository and revision | File | Size (bytes) | SHA-256 |
| --- | --- | --- | --- | --- | --- |
| Nemotron Streaming 3.5 | ASR, the default | [`nvidia/nemotron-3.5-asr-streaming-0.6b`](https://huggingface.co/nvidia/nemotron-3.5-asr-streaming-0.6b/tree/ea30d66debe3740a08b573244286791d423d6b3e) `ea30d66debe3740a08b573244286791d423d6b3e` | `nemotron-3.5-asr-streaming-0.6b.q8_0.gguf` | 742,090,464 | `3fc991d3badad7277c11030a7519832cddaf2057aafed6d4b25147e953a070b1` |
| Parakeet TDT 0.6B v2 | ASR, English | [`opragmatico/parakeet-tdt-0.6b-v2-nemo-speech-gguf`](https://huggingface.co/opragmatico/parakeet-tdt-0.6b-v2-nemo-speech-gguf/tree/da0de3b9ff0f0b219b71bee25e9ef296f0d9cf8c) `da0de3b9ff0f0b219b71bee25e9ef296f0d9cf8c` | `parakeet-tdt-0.6b-v2.q8_0.gguf` (default) | 700,133,856 | `ad2c2e6ccd12c0f0a23ccd4f3a7d1145978d59a7172a10f3ba23db117909d0cd` |
| | | | `parakeet-tdt-0.6b-v2.f16.gguf` | 1,278,057,440 | `894ee6efa3a35bee3654171b85bb473e07479c420678997c6e63523fe571c2a3` |
| Silero VAD v6.2.0 | VAD companion | [`opragmatico/silero-vad-nemo-speech-gguf`](https://huggingface.co/opragmatico/silero-vad-nemo-speech-gguf/tree/9ae6d95f3640eb5a730eb6fbf2567542fec2d964) `9ae6d95f3640eb5a730eb6fbf2567542fec2d964` | `silero-vad-v6.2.0.gguf` | 1,240,224 | `83d702b11378ced4b1bbfcb53394bd00994f7382ff0b9a11b87605dd0898369d` |

The catalog ([#13](https://github.com/opragmatico/goscriber/issues/13)) copies these values. Q8_0 is the default quantisation for both speech models.

## Nemotron Streaming 3.5: NVIDIA's own file

NVIDIA publishes a ready-to-run Q8_0 GGUF next to the `.nemo`, so Goscriber does not re-host it. It pins a newer revision than NeMo-Speech.cpp v0.3.0's own index (`models/index.json`), which still names `1c8deaecc64b` (2026-08-05; 741,548,352 bytes, SHA-256 `a5c435f2…`). On 2026-09-10 NVIDIA re-converted the file "with embedded SentencePiece tokenizer" (`ea30d66debe3`). The two files have the same 657 tensors with the same types; the newer one adds a single metadata key, `asr.tokenizer.spm_model`. NeMo-Speech.cpp tokenises word-boosting phrases with it and, without it, boosting falls back or does nothing, so custom vocabulary ([#26](https://github.com/opragmatico/goscriber/issues/26)) needs the newer file. Goscriber's model tests ([#9](https://github.com/opragmatico/goscriber/issues/9), [#10](https://github.com/opragmatico/goscriber/issues/10)) already ran on it.

## Parakeet TDT 0.6B v2 and Silero VAD: converted by Goscriber

NVIDIA publishes only `parakeet-tdt-0.6b-v2.nemo` for Parakeet v2, and Silero VAD ships as PyTorch and ONNX. [`scripts/convert-models.sh`](../../scripts/convert-models.sh) converts both with NeMo-Speech.cpp's `convert_model.py`; the [spike](parakeet-tdt-0.6b-v2.md) checked that the Parakeet result is as accurate as NVIDIA's model card says.

```bash
brew install uv                  # or https://docs.astral.sh/uv/
scripts/convert-models.sh        # writes models/*.gguf and models/SHA256SUMS, then checks them
```

The first run downloads the 2.47 GB `.nemo` into the Hugging Face cache and about 1 GB of Python packages; the conversions then take under a minute. It needs about 8 GB of disk, including the `.nemo` the converter unpacks into a temporary directory.

What the script pins, which together decide every byte of the output:

- **The converter**: the `third_party/nemo-speech` submodule, NeMo-Speech.cpp v0.3.0 (commit `20999cc9`), the same pin as the SDK. The ASR and VAD converters are plain Python, so its nested submodules are not fetched.
- **Its Python packages**: [`scripts/convert-models-requirements.txt`](../../scripts/convert-models-requirements.txt), NeMo-Speech.cpp's `requirements.txt` resolved for Python 3.12 with a hash for every file. uv installs them into `.deps/convert-venv`, isolated from any other Python, and fetches Python 3.12 itself when the machine has another version. The converter pins NumPy 1.26, which does not install on Python 3.13 or newer.
- **The sources**: `nvidia/parakeet-tdt-0.6b-v2` at revision `ae9ad07059c7c739ffaf932226a8fe64ae2620b0`, and the `silero-vad` 6.2.0 package from PyPI, which carries the Silero weights and is in the lock with its hashes.
- **The file names**: the converter stores an ASR model's file name as its `general.name`, so a renamed file has different bytes. The script always writes the names in the table.

It converts into a staging directory and moves the files into place only when all three are done, then compares them with [`scripts/convert-models.sha256`](../../scripts/convert-models.sha256).

The Silero VAD file loads as the VAD companion of both speech models (`LoadSpec.VADPath`), masks silence, and leaves their transcript of `testdata/audio/jfk.wav` unchanged.

### Reproducibility

Two runs of the script, each in a newly created environment, produced byte-identical files with the SHA-256 in the table, on macOS 15 on Apple Silicon; the Q8_0 file also matches an earlier conversion made by hand. Nothing in the conversion depends on the clock or on randomness. Other platforms have not been checked: PyTorch, NumPy and librosa may round floating-point results differently on another CPU architecture, and librosa computes the Parakeet mel filterbank. If a run elsewhere differs, the script stops with an error instead of publishing, and the published files remain the reference. The published files were checked after upload: downloaded from `https://huggingface.co/{repo}/resolve/{revision}/{file}`, the URL the downloader uses, they match the table and transcribe as expected.

### Publishing

```bash
.deps/convert-venv/bin/hf auth login     # once, with a token that can write to the namespace
scripts/convert-models.sh --publish
```

After the checksum check passes, `--publish` uploads each repository's GGUFs together with its model card from [`packaging/huggingface/`](../../packaging/huggingface/), then prints each repository's new revision. It creates the repositories as public if they do not exist. `GOSCRIBER_HF_NAMESPACE` changes the user or organisation (default `opragmatico`). The script refuses to publish a model card that does not list the SHA-256 of the files next to it.

The repositories are download sources only: the model cards carry the licence and attribution (CC-BY-4.0 for Parakeet, with the changes made; MIT for Silero VAD, with its licence text), and the source, converter version and command line of each file.

After publishing, record the revisions in the table above and in the catalog.

### Updating a pin

1. Move the pin: the submodule (as in [docs/engine-sdk.md](../engine-sdk.md#updating-the-pin)), the Parakeet revision or the Silero version in the script.
2. `scripts/convert-models.sh --lock` when NeMo-Speech.cpp's `requirements.txt` changed, then `scripts/convert-models.sh`. It fails the checksum check, as it should.
3. Check the new files: the spike's accuracy run and `make test-engine` with them in `GOSCRIBER_TEST_MODELS`.
4. Update `scripts/convert-models.sha256`, the model cards and the table above, publish, and update the catalog with the new revisions.
