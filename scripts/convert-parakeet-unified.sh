#!/usr/bin/env bash
# Download NVIDIA's Parakeet Unified EN 0.6B (nvidia/parakeet-unified-en-0.6b,
# a FastConformer-RNNT that runs both offline and cache-aware streaming) and
# convert it to NeMo-Speech.cpp GGUFs: Q8_0 and F16.
#
# It uses the same pinned toolchain as scripts/convert-models.sh: the
# third_party/nemo-speech submodule as the converter, its Python packages from
# scripts/convert-models-requirements.txt (with hashes) in .deps/convert-venv,
# and the checkpoint at a fixed Hugging Face revision. When
# scripts/convert-parakeet-unified.sha256 exists, the outputs are checked
# against it; otherwise their SHA-256 are printed so they can be pinned there.
#
# Usage: scripts/convert-parakeet-unified.sh [--out DIR] [--outtype TYPE]...
#                                            [--revision REV] [--help]
#
#   --out DIR       write the GGUFs and SHA256SUMS to DIR (default: models/)
#   --outtype TYPE  convert only to TYPE (q8_0, f16, bf16, q4_k, q5_k, q6_k);
#                   repeat for several (default: q8_0 and f16)
#   --revision REV  Hugging Face commit to fetch instead of the pinned one; the
#                   pinned SHA-256 check is skipped
#
# Environment:
#   HF_HOME  Hugging Face cache, where the ~2.5 GB checkpoint is downloaded
#            (default ~/.cache/huggingface)
#
# Needs git, uv (https://docs.astral.sh/uv/, for example `brew install uv`)
# and about 6 GB of disk. Written for the bash 3.2 that macOS ships.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC_REL=third_party/nemo-speech
SRC="$ROOT/$SRC_REL"
VENV="$ROOT/.deps/convert-venv"
LOCK="$ROOT/scripts/convert-models-requirements.txt"
SUMS="$ROOT/scripts/convert-parakeet-unified.sha256"
PYTHON_VERSION=3.12

SOURCE=nvidia/parakeet-unified-en-0.6b
PINNED_REVISION=fe53cd885760c96b6a5f51a0bfd362cb4584a98b
# The converter stores the file name (without .gguf) as the model's
# general.name, so renaming an output changes its bytes and hash.
BASENAME=parakeet-unified-en-0.6b

log() { printf '==> %s\n' "$*" >&2; }
die() { printf 'convert-parakeet-unified: %s\n' "$*" >&2; exit 1; }

usage() { sed -n '2,/^# Needs git/{/^# Needs git/d;s/^# \{0,1\}//;p;}' "${BASH_SOURCE[0]}"; }

out="$ROOT/models"
outtypes=""
revision="$PINNED_REVISION"
while [ $# -gt 0 ]; do
    case "$1" in
        --out) [ $# -ge 2 ] || die "--out needs a directory"; out="$2"; shift ;;
        --outtype)
            [ $# -ge 2 ] || die "--outtype needs a type"
            case "$2" in
                q8_0 | f16 | bf16 | q4_k | q5_k | q6_k) outtypes="$outtypes $2" ;;
                *) die "unsupported --outtype '$2' (q8_0, f16, bf16, q4_k, q5_k, q6_k)" ;;
            esac
            shift ;;
        --revision) [ $# -ge 2 ] || die "--revision needs a commit"; revision="$2"; shift ;;
        -h | --help) usage; exit 0 ;;
        *) die "unknown argument '$1' (see --help)" ;;
    esac
    shift
done
[ -n "$outtypes" ] || outtypes="q8_0 f16"

command -v uv >/dev/null 2>&1 || die "uv not found; install it (brew install uv) and retry"

log "Checking out NeMo-Speech.cpp at the pinned commit"
commit="$(git -C "$ROOT" ls-files --stage -- "$SRC_REL" | awk '$1 == "160000" { print $2 }')"
[ -n "$commit" ] || die "$SRC_REL is not a submodule in this checkout"
git -C "$ROOT" submodule update --init --depth 1 -- "$SRC_REL"
[ "$(git -C "$SRC" rev-parse HEAD)" = "$commit" ] ||
    die "$SRC_REL is not at the pinned commit $commit"

# Shared with scripts/convert-models.sh: recreate the environment when the lock
# or the Python version changes (the stamp format must match that script's).
venv_stamp="$PYTHON_VERSION $(shasum -a 256 "$LOCK" | awk '{ print $1 }')"
if [ "$(cat "$VENV/goscriber-stamp" 2>/dev/null)" != "$venv_stamp" ]; then
    log "Creating the converter environment in ${VENV#"$ROOT"/}"
    rm -rf "$VENV"
    uv venv --quiet --python "$PYTHON_VERSION" "$VENV"
    uv pip sync --quiet --python "$VENV/bin/python" --require-hashes \
        --index-strategy unsafe-best-match "$LOCK"
    printf '%s\n' "$venv_stamp" >"$VENV/goscriber-stamp"
fi
PY="$VENV/bin/python"

mkdir -p "$out"
out="$(cd "$out" && pwd)"
# Convert into a staging directory under the final names, then move the results
# in, so an interrupted run leaves no partial GGUF behind.
stage="$out/.convert-parakeet-unified.tmp"
rm -rf "$stage"
mkdir -p "$stage"
trap 'rm -rf "$stage"' EXIT

names=""
for type in $outtypes; do
    name="$BASENAME.$type.gguf"
    log "Converting $SOURCE@${revision:0:12} to $name"
    (cd "$SRC" && "$PY" convert_model.py "$SOURCE" --revision "$revision" \
        --head-type rnnt --outtype "$type" --outfile "$stage/$name")
    names="$names $name"
done

for name in $names; do mv -f "$stage/$name" "$out/$name"; done
# shellcheck disable=SC2086
(cd "$out" && shasum -a 256 $names >"$BASENAME.SHA256SUMS")

log "Sizes and SHA-256"
for name in $names; do
    printf '  %-36s %11s bytes  %s\n' "$name" "$(wc -c <"$out/$name" | tr -d ' ')" \
        "$(awk -v n="$name" '$2 == n { print $1 }' "$out/$BASENAME.SHA256SUMS")" >&2
done

if [ "$revision" != "$PINNED_REVISION" ]; then
    log "Not the pinned revision; skipping the SHA-256 check"
elif [ -f "$SUMS" ]; then
    log "Checking against ${SUMS#"$ROOT"/}"
    (cd "$out" && shasum -a 256 -c --ignore-missing "$SUMS") >&2 ||
        die "the GGUFs differ from the pinned SHA-256 in ${SUMS#"$ROOT"/}"
else
    log "No ${SUMS#"$ROOT"/} yet; to pin these outputs: cp '$out/$BASENAME.SHA256SUMS' '$SUMS'"
fi
