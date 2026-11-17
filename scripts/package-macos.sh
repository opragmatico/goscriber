#!/usr/bin/env bash
# Package a macOS build of Goscriber as Goscriber.app and a disk image, with
# the NeMo-Speech.cpp libraries it runs on, signed with the hardened runtime
# and, given credentials, notarised. `make app` and `make dist-macos` run this
# script after building ./goscriber; docs/releasing.md explains the choices.
#
# Usage: scripts/package-macos.sh [--app | --help]
#
#   --app   only dist/Goscriber.app, as `make app` builds it
#
# By default it also makes dist/goscriber-VERSION-macos-ARCH.dmg, which holds
# the app and a link to /Applications. The app is laid out as:
#
#   Goscriber.app/Contents/
#     Info.plist                packaging/macos/Info.plist, version filled in
#     MacOS/goscriber           run path @executable_path/../Frameworks
#     Frameworks/*.dylib        NeMo-Speech.cpp and ggml, each run path @loader_path
#     Resources/AppIcon.icns
#     Resources/licenses/       Goscriber's licence and the SDK's
#
# No model or VAD weights are bundled: the script refuses an app with a .gguf
# in it. An app built from a ./goscriber without the engine (no make deps) has
# no libraries; `--app` warns and builds it, the disk image refuses.
#
# Environment:
#   VERSION            the version to stamp (default: git describe, as make build)
#   BIN                the goscriber binary to package (default: ./goscriber)
#   OUT                where the app and the disk image go (default: dist)
#   MACOS_MIN          LSMinimumSystemVersion (default 13.3, MACOS_MIN in the Makefile)
#   CODESIGN_IDENTITY  the signing identity; "-", the default, signs ad hoc,
#                      without the hardened runtime. Releases use
#                      "Developer ID Application: NAME (TEAMID)".
#   NOTARY_PROFILE     notarise with credentials saved by
#                      `xcrun notarytool store-credentials NOTARY_PROFILE`
#   NOTARY_KEY         or with an App Store Connect API key: the .p8 file,
#   NOTARY_KEY_ID      its key ID
#   NOTARY_ISSUER      and its issuer ID
#
# Notarisation needs a Developer ID identity and applies to the disk image
# build only: the app is notarised and stapled, then the disk image.
#
# Written for the bash 3.2 that macOS ships.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SDK="$ROOT/.deps/nemo-speech"
STAMP="$SDK/share/goscriber/build-stamp"
PACKAGING="$ROOT/packaging/macos"
ENGINE_LIB=libnemo_speech_asr_c

log() { printf '==> %s\n' "$*" >&2; }
warn() { printf '==> warning: %s\n' "$*" >&2; }
die() { printf 'package-macos: %s\n' "$*" >&2; exit 1; }
usage() { sed -n '2,/^# Written for/{/^# Written for/d;s/^# \{0,1\}//;p;}' "${BASH_SOURCE[0]}"; }

dmg=1
case "${1:-}" in
    "") ;;
    --app) dmg=0 ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument '$1' (see --help)" ;;
esac
[ $# -le 1 ] || die "too many arguments (see --help)"

[ "$(uname -s)" = Darwin ] || die "the macOS app is built on macOS"
BIN="${BIN:-$ROOT/goscriber}"
OUT="${OUT:-$ROOT/dist}"
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}"
MACOS_MIN="${MACOS_MIN:-13.3}"
IDENTITY="${CODESIGN_IDENTITY:--}"
[ -x "$BIN" ] || die "$BIN not found: run make build first"
for tool in codesign install_name_tool otool plutil; do
    command -v "$tool" >/dev/null || die "$tool not found: install the Xcode Command Line Tools (xcode-select --install)"
done

# Notarisation credentials, as notarytool's arguments.
notary_args=()
if [ -n "${NOTARY_PROFILE:-}" ]; then
    notary_args=(--keychain-profile "$NOTARY_PROFILE")
elif [ -n "${NOTARY_KEY:-}" ]; then
    [ -f "$NOTARY_KEY" ] || die "NOTARY_KEY: $NOTARY_KEY not found"
    [ -n "${NOTARY_KEY_ID:-}" ] && [ -n "${NOTARY_ISSUER:-}" ] || die "NOTARY_KEY needs NOTARY_KEY_ID and NOTARY_ISSUER"
    notary_args=(--key "$NOTARY_KEY" --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER")
fi
notarize=0
if [ "${#notary_args[@]}" -gt 0 ] && [ "$dmg" = 1 ]; then
    [ "$IDENTITY" != - ] || die "notarisation needs a Developer ID identity in CODESIGN_IDENTITY, not an ad-hoc signature"
    notarize=1
fi

engine=0
if otool -L "$BIN" | grep -q "@rpath/$ENGINE_LIB\."; then
    engine=1
    [ -f "$STAMP" ] || die "$BIN links the speech engine but .deps has no NeMo-Speech.cpp SDK: run make deps"
elif [ "$dmg" = 1 ]; then
    die "$BIN has no speech engine: build it after make deps (make build adds the nemo tag)"
else
    warn "$BIN has no speech engine (no make deps), so the app carries no libraries and cannot transcribe"
fi
# ggml tunes its CPU code for the build machine unless told not to; such a
# build may crash with SIGILL on an older Apple chip.
if [ "$dmg" = 1 ] && ! grep -q 'GGML_NATIVE=OFF' "$STAMP"; then
    warn "the SDK was built for this machine's processor; for a disk image others run, rebuild it with"
    warn "  NEMO_SPEECH_CMAKE_ARGS=-DGGML_NATIVE=OFF scripts/build-nemo-speech.sh --force"
fi
if [ "$dmg" = 1 ] && [ "$IDENTITY" = - ]; then
    warn "signing ad hoc: Gatekeeper refuses this disk image on other Macs (CODESIGN_IDENTITY, docs/releasing.md)"
fi

# CFBundleVersion only takes dotted numbers: a vX.Y[.Z] tag loses its "v" and
# any "-commits-gHASH" suffix, and anything else (an untagged build) is 0.0.0.
bundle_version="$(printf '%s\n' "$VERSION" | sed -nE 's/^v?([0-9]+\.[0-9]+(\.[0-9]+)?)(-.*)?$/\1/p')"
bundle_version="${bundle_version:-0.0.0}"
arch="$(lipo -archs "$BIN" | tr ' ' '-')"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# rpaths FILE: the file's LC_RPATH entries, one per line.
rpaths() { otool -l "$1" | awk '$1 == "cmd" { rp = ($2 == "LC_RPATH") } rp && $1 == "path" { print $2 }'; }

# rpath_libs FILE: the @rpath libraries FILE loads, by the name it loads them.
rpath_libs() { otool -L "$1" | awk 'NR > 1 && $1 ~ /^@rpath\// { sub(/^@rpath\//, "", $1); print $1 }'; }

# set_rpath FILE RPATH: make RPATH the file's only run path.
set_rpath() {
    local args=() old out keep=0
    while IFS= read -r old; do
        # install_name_tool refuses to delete and add the same path.
        if [ "$old" = "$2" ]; then keep=1; else args+=(-delete_rpath "$old"); fi
    done < <(rpaths "$1")
    [ "$keep" = 1 ] || args+=(-add_rpath "$2")
    [ "${#args[@]}" -gt 0 ] || return 0
    # It warns that the edit invalidates the signature; the file is signed
    # again below.
    out="$(install_name_tool "${args[@]}" "$1" 2>&1)" || die "install_name_tool on $1: $out"
    printf '%s\n' "$out" | grep -v -e '^$' -e 'invalidate the code signature' >&2 || true
}

# sign PATH [codesign args...]: sign with the hardened runtime and a secure
# timestamp, which notarisation requires. An ad-hoc signature gets neither:
# it has no team, and the hardened runtime only loads libraries signed by the
# app's own team, so the app could not load its Frameworks.
sign() {
    local path="$1" out
    shift
    if [ "$IDENTITY" = - ]; then
        set -- --timestamp=none "$@"
    else
        set -- --options runtime --timestamp "$@"
    fi
    out="$(codesign --force --sign "$IDENTITY" "$@" "$path" 2>&1)" || die "codesign ${path#"$ROOT"/}: $out"
    printf '%s\n' "$out" | grep -v -e '^$' -e 'replacing existing signature' >&2 || true
}

# notarise FILE: submit FILE to Apple's notary service, wait for the verdict
# and print the service's log when it refuses.
notarise() {
    local file="$1" result="$work/notary.json" status id
    log "Notarising $(basename "$file"); this takes a few minutes"
    xcrun notarytool submit "$file" "${notary_args[@]}" --wait --output-format json >"$result" || true
    status="$(plutil -extract status raw -o - "$result" 2>/dev/null || true)"
    id="$(plutil -extract id raw -o - "$result" 2>/dev/null || true)"
    if [ "$status" != Accepted ]; then
        cat "$result" >&2
        [ -z "$id" ] || xcrun notarytool log "$id" "${notary_args[@]}" >&2 || true
        die "notarisation of $(basename "$file") ended '${status:-in an error}'"
    fi
    log "Notarised $(basename "$file") (submission $id)"
}

# --- The app bundle -----------------------------------------------------------

app="$OUT/Goscriber.app"
log "Assembling ${app#"$ROOT"/} $VERSION ($arch)"
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources/licenses"
sed -e "s/@VERSION@/$bundle_version/g" -e "s/@MACOS_MIN@/$MACOS_MIN/g" \
    "$PACKAGING/Info.plist" >"$app/Contents/Info.plist"
plutil -lint -s "$app/Contents/Info.plist" || die "the generated Info.plist is invalid"
cp "$PACKAGING/AppIcon.icns" "$app/Contents/Resources/AppIcon.icns"
cp "$ROOT/LICENSE" "$app/Contents/Resources/licenses/LICENSE"
exe="$app/Contents/MacOS/goscriber"
cp "$BIN" "$exe"
chmod 0755 "$exe"

if [ "$engine" = 1 ]; then
    # Copy the libraries the binary loads, and the ones those load, under the
    # names they are loaded by (libggml.0.dylib, not its symlink chain), and
    # make each find the others beside it.
    frameworks="$app/Contents/Frameworks"
    mkdir -p "$frameworks"
    # Library names have no spaces or wildcards, so the lists split on words.
    pending="$(rpath_libs "$exe")"
    while [ -n "$pending" ]; do
        found=
        for name in $pending; do
            [ -e "$frameworks/$name" ] && continue
            [ -e "$SDK/lib/$name" ] || die "$name, which the engine loads, is not in .deps/nemo-speech/lib"
            cp -L "$SDK/lib/$name" "$frameworks/$name"
            chmod 0644 "$frameworks/$name"
            set_rpath "$frameworks/$name" @loader_path
            found="$found $(rpath_libs "$frameworks/$name")"
        done
        pending="$found"
    done
    # The binary's run path pointed into .deps for development; now it points
    # at Frameworks only, so a bundle that misses a library fails here, not on
    # a user's Mac.
    set_rpath "$exe" @executable_path/../Frameworks
    cp -R "$SDK/share/licenses/nemo-speech" "$app/Contents/Resources/licenses/nemo-speech"
fi

# Extended attributes (quarantine, Finder info) break code signatures.
xattr -cr "$app"
weights="$(find "$app" -iname '*.gguf')"
[ -z "$weights" ] || die "model weights must not be bundled: $weights"

# --- Signing ------------------------------------------------------------------

# Inside out: each library, then the bundle, which signs the executable with
# the entitlements and seals the rest.
if [ "$engine" = 1 ]; then
    for lib in "$app"/Contents/Frameworks/*.dylib; do
        sign "$lib"
    done
fi
sign "$app" --entitlements "$PACKAGING/Goscriber.entitlements"
codesign --verify --strict --deep "$app" || die "the signature of $app does not verify"
# Loads every library the binary links, from Frameworks, before main runs.
"$exe" --version >/dev/null || die "${exe#"$ROOT"/} does not run"

signed_by="ad hoc"
[ "$IDENTITY" = - ] || signed_by="$IDENTITY"
log "Built ${app#"$ROOT"/} (signed $signed_by)"
[ "$dmg" = 1 ] || exit 0

# --- Notarisation and the disk image ------------------------------------------

if [ "$notarize" = 1 ]; then
    ditto -c -k --keepParent "$app" "$work/Goscriber.zip"
    notarise "$work/Goscriber.zip"
    xcrun stapler staple -q "$app"
fi

dmg_file="$OUT/goscriber-$VERSION-macos-$arch.dmg"
stage="$work/dmg"
mkdir -p "$stage"
ditto "$app" "$stage/Goscriber.app"
ln -s /Applications "$stage/Applications"
rm -f "$dmg_file"
log "Creating ${dmg_file#"$ROOT"/}"
# hdiutil sometimes fails with "Resource busy" on CI machines; try again.
for attempt in 1 2 3; do
    if hdiutil create -quiet -volname Goscriber -srcfolder "$stage" -fs HFS+ -format UDZO -ov "$dmg_file"; then
        break
    fi
    [ "$attempt" -lt 3 ] || die "hdiutil create failed"
    sleep 5
done

if [ "$IDENTITY" != - ]; then
    codesign --force --sign "$IDENTITY" --timestamp "$dmg_file"
fi
if [ "$notarize" = 1 ]; then
    notarise "$dmg_file"
    xcrun stapler staple -q "$dmg_file"
    spctl --assess --type execute "$app" || die "Gatekeeper refuses ${app#"$ROOT"/}"
    spctl --assess --type open --context context:primary-signature "$dmg_file" ||
        die "Gatekeeper refuses ${dmg_file#"$ROOT"/}"
    log "Built ${dmg_file#"$ROOT"/} (signed $signed_by, notarised)"
else
    log "Built ${dmg_file#"$ROOT"/} (signed $signed_by, not notarised)"
fi
