# Test data

Shared fixtures for tests across packages. Go ignores `testdata/` directories when building, so nothing here ends up in the binary.

## Audio

| File | Format | Duration | Transcript | Source and license |
| --- | --- | --- | --- | --- |
| `audio/jfk.wav` | WAV, PCM 16-bit, mono, 16 kHz | 11.0 s | `audio/jfk.txt` | Excerpt of John F. Kennedy's inaugural address (20 January 1961), a work of the US federal government and therefore in the public domain. Copied from [whisper.cpp `samples/jfk.wav`](https://github.com/ggml-org/whisper.cpp/blob/master/samples/jfk.wav). |

| `audio/es.wav` | WAV, PCM 16-bit, mono, 16 kHz | 10.5 s | `audio/es.txt` | Spanish. The first sentence of Aesop's "La zorra y los racimos de uvas", 16.2 s to 26.7 s of `fabula_01_012_esopo_64kb.mp3` from LibriVox's [*Las Fábulas de Esopo, vol. 01*](https://archive.org/details/fabulas_esopo_01_librivox), read by a LibriVox volunteer. LibriVox recordings are in the public domain, and so is the fable's text. |

SHA-256 of `audio/jfk.wav`: `59dfb9a4acb36fe2a2affc14bacbee2920ff435cb13cc314a08c13f66ba7860e`

SHA-256 of `audio/es.wav`: `e61001450f6a53eee546cea56b43322bee0e7f85473a8497560d786771740177`, cut from the MP3 (SHA-256 `5f2490583cbd131a33293caab38236d1817c5b454b3aceabf70b94420a3ca7f5`) with:

```bash
ffmpeg -i fabula_01_012_esopo_64kb.mp3 -ss 16.2 -to 26.7 -ac 1 -ar 16000 -c:a pcm_s16le \
  -map_metadata -1 -fflags +bitexact -flags:a +bitexact es.wav
```

Model files are never committed here (see the repository `.gitignore`); tests that need a real model read `GOSCRIBER_TEST_MODELS` and skip when it is unset.
