#!/usr/bin/env bash
# Package a Linux build of Goscriber as a tarball and a Debian package, with
# the NeMo-Speech.cpp libraries it runs on. `make dist-linux` runs this
# script after building ./goscriber with the engine; docs/linux.md explains
# the choices.
#
# Usage: scripts/package-linux.sh [--tarball | --deb | --help]
#
#   --tarball  only dist/goscriber-VERSION-linux-ARCH.tar.gz
#   --deb      only dist/goscriber_VERSION_ARCH.deb
#
# Both are made by default. They hold the same files:
#
#   tarball                         .deb
#   goscriber-VERSION/bin/          /opt/goscriber/bin/goscriber (and /usr/bin/goscriber)
#   goscriber-VERSION/lib/          /opt/goscriber/lib/ (NeMo-Speech.cpp and ggml)
#   goscriber-VERSION/share/        /usr/share/applications, icons and doc
#
# The binary finds the libraries through a run path relative to itself
# ($ORIGIN/../lib), so the tarball runs from wherever it is unpacked.
#
# Environment:
#   VERSION   the version to stamp (default: git describe, as make build)
#   BIN       the goscriber binary to package (default: ./goscriber)
#   OUT       where the packages go (default: dist)
#
# Needs patchelf and, for the .deb, dpkg-deb and dpkg-shlibdeps (dpkg-dev).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SDK="$ROOT/.deps/nemo-speech"
STAMP="$SDK/share/goscriber/build-stamp"
APP_ID=io.github.opragmatico.goscriber

log() { printf '==> %s\n' "$*" >&2; }
die() { printf 'package-linux: %s\n' "$*" >&2; exit 1; }
usage() { sed -n '2,/^set -euo/{/^set -euo/d;s/^# \{0,1\}//;p;}' "${BASH_SOURCE[0]}"; }

tarball=1 deb=1
case "${1:-}" in
    "") ;;
    --tarball) deb=0 ;;
    --deb) tarball=0 ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument '$1' (see --help)" ;;
esac

[ "$(uname -s)" = Linux ] || die "Linux packages are built on Linux"
BIN="${BIN:-$ROOT/goscriber}"
OUT="${OUT:-$ROOT/dist}"
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}"
[ -x "$BIN" ] || die "$BIN not found: run make build first"
[ -f "$STAMP" ] || die "no NeMo-Speech.cpp SDK in .deps: run make deps first"
command -v patchelf >/dev/null || die "patchelf not found (sudo apt-get install patchelf)"
if [ "$deb" = 1 ]; then
    command -v dpkg-deb >/dev/null || die "dpkg-deb not found"
    command -v dpkg-shlibdeps >/dev/null || die "dpkg-shlibdeps not found (sudo apt-get install dpkg-dev)"
fi

# A binary without the engine is no app to ship.
if ! readelf -d "$BIN" | grep -q 'libnemo_speech_asr_c'; then
    die "$BIN has no speech engine: build it after make deps (make build adds the nemo tag)"
fi
# ggml tunes its CPU code for the build machine unless told not to; such a
# build may crash with SIGILL on an older processor.
if ! grep -q 'GGML_NATIVE=OFF' "$STAMP"; then
    log "warning: the SDK was built for this machine's processor; for packages others run, rebuild it with"
    log "         NEMO_SPEECH_CMAKE_ARGS=-DGGML_NATIVE=OFF scripts/build-nemo-speech.sh --force"
fi

arch="$(uname -m)"
case "$arch" in
    x86_64) deb_arch=amd64 ;;
    aarch64) deb_arch=arm64 ;;
    *) deb_arch="$arch" ;;
esac
preset="$(sed -n 's/^preset //p' "$STAMP")"

# Debian versions must start with a digit: v1.2.3 is 1.2.3, a build past
# the tag is 1.2.3+4.gabc1234, and an untagged one 0.0.0~gitabc1234.
deb_version="$(printf '%s' "$VERSION" | sed -E \
    -e 's/^v//' \
    -e 's/^([0-9]+(\.[0-9]+)*)-([0-9]+)-g([0-9a-f]+)/\1+\3.g\4/' \
    -e 's/-dirty$/+dirty/')"
case "$deb_version" in
    [0-9]*) ;;
    *) deb_version="0.0.0~git$(printf '%s' "$deb_version" | tr -c 'A-Za-z0-9.+~\n' '.')" ;;
esac

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# stage DIR: the app's files under DIR/{bin,lib} and DIR/share.
stage() {
    local dir="$1" share="$2"
    mkdir -p "$dir/bin" "$dir/lib" "$share/applications" "$share/icons/hicolor/scalable/apps" "$share/doc/goscriber/handy"
    install -m 0755 "$BIN" "$dir/bin/goscriber"
    patchelf --set-rpath '$ORIGIN/../lib' "$dir/bin/goscriber"
    # The shared libraries with their version symlinks, each finding the
    # others next to it.
    cp -P "$SDK"/lib/*.so* "$dir/lib/"
    local lib
    for lib in "$dir"/lib/*.so*; do
        [ -L "$lib" ] && continue
        chmod 0644 "$lib"
        patchelf --set-rpath '$ORIGIN' "$lib"
    done
    install -m 0644 "$ROOT/packaging/linux/$APP_ID.desktop" "$share/applications/$APP_ID.desktop"
    install -m 0644 "$ROOT/internal/ui/assets/goscriber.svg" "$share/icons/hicolor/scalable/apps/$APP_ID.svg"
    install -m 0644 "$ROOT/LICENSE" "$share/doc/goscriber/LICENSE"
    # Handy's, for the code adapted from it (see the README's Acknowledgements).
    install -m 0644 "$ROOT/third_party/handy/LICENSE" "$share/doc/goscriber/handy/LICENSE"
    install -m 0644 "$ROOT/docs/linux.md" "$share/doc/goscriber/linux.md"
    # NeMo-Speech.cpp's licence, NOTICE and third-party notices, and those of
    # ggml, miniaudio and SentencePiece with what it bundles, as the SDK
    # installs them.
    cp -R "$SDK/share/licenses/nemo-speech" "$share/doc/goscriber/nemo-speech"
    find "$share/doc/goscriber/nemo-speech" -type d -exec chmod 0755 {} + -o -type f -exec chmod 0644 {} +
}

mkdir -p "$OUT"

if [ "$tarball" = 1 ]; then
    name="goscriber-$VERSION-linux-$arch"
    top="$work/tar/goscriber-$VERSION"
    stage "$top" "$top/share"
    tar -C "$work/tar" -czf "$OUT/$name.tar.gz" --owner=0 --group=0 "goscriber-$VERSION"
    log "Built $OUT/$name.tar.gz ($preset)"
fi

if [ "$deb" = 1 ]; then
    root="$work/deb/goscriber"
    stage "$root/opt/goscriber" "$root/usr/share"
    mkdir -p "$root/usr/bin"
    ln -s /opt/goscriber/bin/goscriber "$root/usr/bin/goscriber"

    # dpkg-shlibdeps reads the libraries the binary and the SDK link
    # against (libc, libGL, X11, XTEST, libgomp, libvulkan in a Vulkan
    # build) and names the packages that hold them. It wants a
    # debian/control to sit in, and the package root's DEBIAN directory to
    # resolve $ORIGIN and leave out the libraries the package carries.
    mkdir -p "$work/deb/debian" "$root/DEBIAN"
    printf 'Source: goscriber\n\nPackage: goscriber\nArchitecture: any\n' >"$work/deb/debian/control"
    objects=("$root/opt/goscriber/bin/goscriber")
    while IFS= read -r lib; do objects+=("$lib"); done < <(find "$root/opt/goscriber/lib" -type f -name '*.so*' | sort)
    if ! shlibs="$(cd "$work/deb" && dpkg-shlibdeps -O "${objects[@]}" 2>"$work/shlibdeps.log")"; then
        cat "$work/shlibdeps.log" >&2
        die "dpkg-shlibdeps failed"
    fi
    depends="$(printf '%s\n' "$shlibs" | sed -n 's/^shlibs:Depends=//p')"
    [ -n "$depends" ] || die "dpkg-shlibdeps found no dependencies"

    size="$(du -sk "$root" | cut -f1)"
    cat >"$root/DEBIAN/control" <<EOF
Package: goscriber
Version: $deb_version
Architecture: $deb_arch
Maintainer: Goscriber <https://github.com/opragmatico/goscriber/issues>
Installed-Size: $size
Depends: $depends
Recommends: libasound2t64 | libasound2
Suggests: gnome-shell-extension-appindicator, ydotool, wtype, dotool, xdotool
Section: utils
Priority: optional
Homepage: https://github.com/opragmatico/goscriber
Description: Offline push-to-talk speech-to-text
 Press a shortcut, speak, and the transcript is pasted into the app you are
 using. Speech recognition runs on this computer with NVIDIA's Nemotron and
 Parakeet models through NeMo-Speech.cpp ($preset); audio never leaves it.
 Models are downloaded on first use.
 .
 On Wayland, the dictation shortcut is set in the desktop's keyboard
 settings, and pasting needs wtype, dotool or ydotool: see
 /usr/share/doc/goscriber/linux.md.
EOF
    deb_file="$OUT/goscriber_${deb_version}_${deb_arch}.deb"
    dpkg-deb --root-owner-group -Zxz --build "$root" "$deb_file" >/dev/null
    log "Built $deb_file ($preset)"
fi
