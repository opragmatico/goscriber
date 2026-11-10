# Test data

Shared fixtures for tests across packages. Go ignores `testdata/` directories when building, so nothing here ends up in the binary.

## Audio

| File | Format | Duration | Transcript | Source and license |
| --- | --- | --- | --- | --- |
| `audio/jfk.wav` | WAV, PCM 16-bit, mono, 16 kHz | 11.0 s | `audio/jfk.txt` | Excerpt of John F. Kennedy's inaugural address (20 January 1961), a work of the US federal government and therefore in the public domain. Copied from [whisper.cpp `samples/jfk.wav`](https://github.com/ggml-org/whisper.cpp/blob/master/samples/jfk.wav). |

SHA-256 of `audio/jfk.wav`: `59dfb9a4acb36fe2a2affc14bacbee2920ff435cb13cc314a08c13f66ba7860e`

Model files are never committed here (see the repository `.gitignore`); tests that need a real model read `GOSCRIBER_TEST_MODELS` and skip when it is unset.
