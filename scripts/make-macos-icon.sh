#!/usr/bin/env bash
# Render packaging/macos/AppIcon.svg into packaging/macos/AppIcon.icns, the
# icon `make app` puts in Goscriber.app. The .icns is committed, so building
# the app needs no SVG renderer; run this after changing the SVG.
#
# Usage: scripts/make-macos-icon.sh
#
# Needs macOS (iconutil) and rsvg-convert (brew install librsvg) or Inkscape.
#
# Written for the bash 3.2 that macOS ships.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$ROOT/packaging/macos/AppIcon.svg"
DST="$ROOT/packaging/macos/AppIcon.icns"

die() { printf 'make-macos-icon: %s\n' "$*" >&2; exit 1; }

case "${1:-}" in
    "") ;;
    -h | --help) sed -n '2,/^# Written for/{/^# Written for/d;s/^# \{0,1\}//;p;}' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) die "unknown argument '$1' (see --help)" ;;
esac

command -v iconutil >/dev/null || die "iconutil not found: the .icns is made on macOS"
if command -v rsvg-convert >/dev/null; then
    render() { rsvg-convert -w "$1" -h "$1" -o "$2" "$SRC"; }
elif command -v inkscape >/dev/null; then
    render() { inkscape "$SRC" --export-type=png --export-filename="$2" -w "$1" -h "$1" >/dev/null 2>&1; }
else
    die "no SVG renderer: brew install librsvg (or Inkscape)"
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
set="$work/AppIcon.iconset"
mkdir "$set"

# Each size is drawn from the SVG rather than scaled down from 1024 px, so
# the small ones stay sharp.
for size in 16 32 128 256 512; do
    render "$size" "$set/icon_${size}x${size}.png"
    render $((size * 2)) "$set/icon_${size}x${size}@2x.png"
done
iconutil --convert icns --output "$DST" "$set"
printf '==> Wrote %s\n' "${DST#"$ROOT"/}" >&2
