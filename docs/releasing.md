# Releases

A release is a git tag. Pushing a `v*` tag runs [`.github/workflows/release.yml`](../.github/workflows/release.yml), which builds, checks and publishes:

| File | What |
| --- | --- |
| `goscriber-vX.Y.Z-macos-arm64.dmg` | `Goscriber.app` for Apple Silicon, macOS 13.3 or later, signed with Developer ID and notarised |
| `goscriber-vX.Y.Z-linux-x86_64.tar.gz`, `…-linux-aarch64.tar.gz` | the Linux tarballs ([linux.md](linux.md#packages)) |
| `goscriber_X.Y.Z_amd64.deb`, `goscriber_X.Y.Z_arm64.deb` | the Debian packages |
| `SHA256SUMS` | the SHA-256 of each file |

None of them holds a model or the VAD: the app downloads them when the user chooses a model ([architecture](architecture.md)). The workflow fails if a package contains a `.gguf`.

## Cutting a release

```bash
git tag -a v0.1.0 -m "Goscriber 0.1.0"
```

```bash
git push origin v0.1.0
```

The workflow then runs three jobs:

1. **macOS** on a `macos-26` runner: builds the NeMo-Speech.cpp SDK with `-DGGML_NATIVE=OFF`, imports the Developer ID certificate into a keychain of its own, runs `make dist-macos` to sign, notarise and staple the app and the disk image, then mounts the disk image and checks it (below).
2. **Linux** on `ubuntu-24.04` and `ubuntu-24.04-arm`: the same SDK build, `make dist-linux`, and the checks.
3. **Publish**, once both have passed: computes `SHA256SUMS` and creates the GitHub release with every file. The notes start with how to install ([`.github/release-notes.md`](../.github/release-notes.md)), followed by GitHub's list of the pull requests since the previous release. A tag with a hyphen, such as `v0.2.0-rc.1`, is published as a pre-release.

**Dry run.** Actions → Release → Run workflow, on any branch, builds and checks the same files without publishing anything; they stay as the run's artifacts. Without the signing secrets it signs ad hoc and skips notarisation, which a tag never does.

## Secrets

| Secret | What |
| --- | --- |
| `MACOS_CERTIFICATE` | A `.p12` holding the **Developer ID Application** certificate and its private key, base64-encoded |
| `MACOS_CERTIFICATE_PASSWORD` | The `.p12`'s password |
| `NOTARY_API_KEY` | An App Store Connect API key: the text of its `.p8` file |
| `NOTARY_API_KEY_ID` | The key's ID |
| `NOTARY_API_ISSUER` | The issuer ID shown above the keys |

- **Certificate.** It needs a paid Apple Developer Program membership, and only the account holder can create it: Xcode → Settings → Accounts → Manage Certificates → + → Developer ID Application. Export it from Keychain Access with its key as a `.p12`, then `base64 -i goscriber.p12 | gh secret set MACOS_CERTIFICATE`.
- **API key.** App Store Connect → Users and Access → Integrations → Team Keys, with the Developer role. The `.p8` downloads once: `gh secret set NOTARY_API_KEY < AuthKey_XXXXXXXXXX.p8`.

A tag pushed without them fails instead of publishing an unsigned app.

## The macOS app

`make app` builds `dist/Goscriber.app`, and `make dist-macos` the disk image as well; both run [`scripts/package-macos.sh`](../scripts/package-macos.sh) after `make build`.

```text
Goscriber.app/Contents/
├── Info.plist              # packaging/macos/Info.plist, with the version and the minimum macOS
├── MacOS/goscriber         # run path: @executable_path/../Frameworks, nothing else
├── Frameworks/             # libnemo_speech_asr_c.1.dylib, libnemo_speech_asr.dylib, libggml*.0.dylib (about 6 MB)
├── Resources/
│   ├── AppIcon.icns
│   └── licenses/           # Goscriber's, NeMo-Speech.cpp's and those of the code it bundles
└── _CodeSignature/
```

- **Libraries.** The script copies the libraries the binary loads, and those they load, from `.deps/nemo-speech/lib` under the names they are loaded by, so the version symlinks stay behind. Each finds the others through an `@loader_path` run path. The binary's development run path into `.deps` is deleted, so a bundle that misses a library cannot quietly use the build machine's: the script runs `goscriber --version` from the bundle, which loads every library before `main`.
- **Minimum macOS.** 13.3, the libraries' deployment target (`MACOS_MIN` in the Makefile, which also fills in `LSMinimumSystemVersion`). Clang would otherwise build the CGo code for the build machine's macOS: a binary built on macOS 15 required macOS 15. The Makefile passes `-mmacosx-version-min` in `CGO_CFLAGS` and `CGO_LDFLAGS`, because Go's build cache ignores `MACOSX_DEPLOYMENT_TARGET` and would reuse objects built for the newer version.
- **Icon.** [`packaging/macos/AppIcon.svg`](../packaging/macos/AppIcon.svg) draws the app icon's microphone on Apple's grid (an 824 pt rounded square on a 1024 pt canvas). The `.icns` is committed, so building needs no SVG renderer; `scripts/make-macos-icon.sh` regenerates it.
- **No weights.** The script refuses to sign a bundle with a `.gguf` in it.

### Signing

The script signs each library, then the bundle, which signs the executable with [`Goscriber.entitlements`](../packaging/macos/Goscriber.entitlements) and seals the rest.

- **With an identity** (Developer ID, or Apple Development for local builds): the hardened runtime, which notarisation requires, and a secure timestamp. The one entitlement is `com.apple.security.device.audio-input`, without which the hardened runtime blocks the microphone. Nothing else is needed: Go generates no code at run time, the libraries carry the app's own team ID, and the shortcut and pasting rely on the Accessibility permission, not on an entitlement.
- **Ad hoc**, the default (`CODESIGN_IDENTITY=-`): no hardened runtime. Its library validation only loads libraries signed by the app's team, and an ad-hoc signature has no team, so the app could not load its own Frameworks. Ad-hoc builds therefore do not exercise the hardened runtime; `make app CODESIGN_IDENTITY="Apple Development: …"` does.

macOS ties the microphone and Accessibility grants to the bundle identifier and the signing team, so a release signed with the same Developer ID keeps them across updates. Ad-hoc builds lose them at every rebuild ([permissions.md](permissions.md#during-development)).

### Notarisation and the disk image

Given credentials, `make dist-macos`:

1. Zips the app, submits it to Apple's notary service, waits for the verdict and staples the ticket to the app, so it opens offline once copied out of the disk image.
2. Builds the disk image, holding the app and a link to `/Applications`, and signs it.
3. Notarises and staples the disk image, then asks Gatekeeper (`spctl`) to accept both.

When Apple refuses a submission, the script prints the notary log, which names each problem. To release from a Mac rather than from the workflow:

```bash
NEMO_SPEECH_CMAKE_ARGS=-DGGML_NATIVE=OFF scripts/build-nemo-speech.sh --force
```

```bash
xcrun notarytool store-credentials goscriber --apple-id YOU@EXAMPLE.COM --team-id TEAMID
```

```bash
make dist-macos CODESIGN_IDENTITY="Developer ID Application: Your Name (TEAMID)" NOTARY_PROFILE=goscriber
```

`NOTARY_PROFILE` names the credentials `store-credentials` saved in the login keychain. The workflow passes an API key instead: `NOTARY_KEY` (the `.p8` file), `NOTARY_KEY_ID` and `NOTARY_ISSUER`.

### Decision: Apple Silicon only

Releases ship an `arm64` app, not a universal one. NeMo-Speech.cpp's macOS build targets Apple Silicon and Metal, and the build script has no Intel preset; on an Intel Mac the engine would run on the CPU alone, untested. Apple has said macOS 26 is the last release for Intel Macs, and a universal binary would double the engine's libraries for them. Intel users can build from source with `NEMO_SPEECH_PRESET=cpu-asr`, which nobody has tried yet.

## Linux packages

Built on Ubuntu 24.04, the oldest distribution [linux.md](linux.md) covers, for x86-64 and arm64: the packages need its glibc and libstdc++ or newer. They carry the CPU engine (`cpu-asr`, with `-DGGML_NATIVE=OFF`); the Vulkan build is for building from source ([linux.md](linux.md#speech-engine-cpu-and-vulkan)).

## What the workflow checks

- **macOS**, on the mounted disk image: no `.gguf`; `codesign --verify --strict --deep`; the executable's only run path is `@executable_path/../Frameworks`; `goscriber --version` runs from it; and, when signed, Gatekeeper accepts the app as notarised Developer ID and both tickets are stapled.
- **Linux**: no `.gguf` in the tarball or the `.deb`; `goscriber --version` runs from the unpacked tarball.

What it cannot check is a first run on a Mac that has never seen Goscriber. Before announcing a release, on such a Mac (or a new user account):

1. Download the disk image with a browser, so macOS quarantines it, open it and drag Goscriber to Applications.
2. Open Goscriber. macOS asks only whether to open an app downloaded from the internet, naming the developer; it never says the app is damaged or cannot be checked for malicious software.
3. Onboarding asks for the microphone, with Goscriber's description, and for Accessibility, then downloads the chosen model.
4. Dictate into another app.

`spctl --assess -vv /Applications/Goscriber.app` reports `source=Notarized Developer ID`.

## Homebrew

There is no cask yet. One would live in a tap repository (`opragmatico/homebrew-tap`) and point at each release's disk image and its SHA-256, which the publish job could update.
