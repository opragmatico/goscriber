## Install

**macOS** on Apple Silicon, macOS 13.3 or later: open `goscriber-@VERSION@-macos-arm64.dmg` and drag Goscriber to Applications. The app is signed and notarised. It lives in the menu bar; at first launch it asks for the microphone and Accessibility permissions and helps you choose and download a speech model. Nothing is downloaded until you choose one.

**Linux** on x86-64 or arm64, Ubuntu 24.04 or as recent: `sudo apt install ./goscriber_*.deb`, or unpack the tarball and run `goscriber-@VERSION@/bin/goscriber`. Wayland needs some setup: see [docs/linux.md](https://github.com/opragmatico/goscriber/blob/@VERSION@/docs/linux.md).

`SHA256SUMS` holds each file's checksum: compare it with `shasum -a 256 FILE`.
