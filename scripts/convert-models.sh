#!/usr/bin/env bash
# Convert, from scratch, the GGUFs Goscriber downloads but NVIDIA does not
# publish: Parakeet TDT 0.6B v2 (Q8_0 and F16) and the Silero VAD v6.2.0
# companion. Then check them against the SHA-256 in scripts/convert-models.sha256
# and, with --publish, upload them to their Hugging Face repositories.
# docs/models/gguf.md explains the choices.
#
# Everything that decides the bytes is pinned: the converter is the
# third_party/nemo-speech submodule (NeMo-Speech.cpp v0.3.0); its Python
# packages come, with their hashes, from scripts/convert-models-requirements.txt,
# installed by uv into an isolated virtual environment on Python 3.12
# (.deps/convert-venv); the Parakeet checkpoint is fetched at a fixed Hugging
# Face revision; and Silero VAD comes from its pinned pip package.
#
# Usage: scripts/convert-models.sh [--out DIR] [--publish] [--lock] [--help]
#
#   --out DIR   write the GGUFs and SHA256SUMS to DIR (default: models/)
#   --publish   after a successful check, upload each GGUF with its model card
#               to Hugging Face (needs `hf auth login` or HF_TOKEN); prints the
#               new revision of each repository
#   --lock      re-resolve scripts/convert-models-requirements.txt from the
#               converter's requirements.txt, and exit
#
# Environment:
#   GOSCRIBER_HF_NAMESPACE  Hugging Face user or organisation that --publish
#                           uploads to (default: opragmatico)
#   HF_HOME                 Hugging Face cache, where the 2.5 GB Parakeet
#                           checkpoint is downloaded (default ~/.cache/huggingface)
#
# Needs git, uv (https://docs.astral.sh/uv/, for example `brew install uv`)
# and about 8 GB of disk. Written for the bash 3.2 that macOS ships.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC_REL=third_party/nemo-speech
SRC="$ROOT/$SRC_REL"
VENV="$ROOT/.deps/convert-venv"
LOCK="$ROOT/scripts/convert-models-requirements.txt"
SUMS="$ROOT/scripts/convert-models.sha256"
CARDS="$ROOT/packaging/huggingface"
PYTHON_VERSION=3.12

# The sources. The converter fetches the .nemo at this revision of
# nvidia/parakeet-tdt-0.6b-v2; the Silero VAD weights are inside the pinned
# silero-vad package.
PARAKEET_SOURCE=nvidia/parakeet-tdt-0.6b-v2
PARAKEET_REVISION=ae9ad07059c7c739ffaf932226a8fe64ae2620b0
SILERO_VERSION=6.2.0

# The outputs. The converter stores the file name (without .gguf) as the
# model's general.name, so renaming an output changes its bytes and hash.
PARAKEET_Q8=parakeet-tdt-0.6b-v2.q8_0.gguf
PARAKEET_F16=parakeet-tdt-0.6b-v2.f16.gguf
SILERO=silero-vad-v$SILERO_VERSION.gguf

NAMESPACE="${GOSCRIBER_HF_NAMESPACE:-opragmatico}"
PARAKEET_REPO=parakeet-tdt-0.6b-v2-nemo-speech-gguf
SILERO_REPO=silero-vad-nemo-speech-gguf

log() { printf '==> %s\n' "$*" >&2; }
die() { printf 'convert-models: %s\n' "$*" >&2; exit 1; }

usage() { sed -n '2,/^# Needs git/{/^# Needs git/d;s/^# \{0,1\}//;p;}' "${BASH_SOURCE[0]}"; }

out="$ROOT/models"
publish=0
lock=0
while [ $# -gt 0 ]; do
    case "$1" in
        --out) [ $# -ge 2 ] || die "--out needs a directory"; out="$2"; shift ;;
        --publish) publish=1 ;;
        --lock) lock=1 ;;
        -h | --help) usage; exit 0 ;;
        *) die "unknown argument '$1' (see --help)" ;;
    esac
    shift
done

command -v uv >/dev/null 2>&1 || die "uv not found; install it (brew install uv) and retry"

log "Checking out NeMo-Speech.cpp at the pinned commit"
commit="$(git -C "$ROOT" ls-files --stage -- "$SRC_REL" | awk '$1 == "160000" { print $2 }')"
[ -n "$commit" ] || die "$SRC_REL is not a submodule in this checkout"
# The ASR and VAD converters are plain Python: no nested submodule is needed.
git -C "$ROOT" submodule update --init --depth 1 -- "$SRC_REL"
[ "$(git -C "$SRC" rev-parse HEAD)" = "$commit" ] ||
    die "$SRC_REL is not at the pinned commit $commit"

# The converter's requirements.txt adds PyTorch's CPU index. pip merges every
# index and takes the best match; unsafe-best-match makes uv do the same.
UV_PIP_ARGS="--index-strategy unsafe-best-match"

if [ "$lock" = 1 ]; then
    log "Resolving $SRC_REL/requirements.txt for Python $PYTHON_VERSION"
    # shellcheck disable=SC2086
    uv pip compile "$SRC/requirements.txt" $UV_PIP_ARGS --universal \
        --python-version "$PYTHON_VERSION" --generate-hashes --emit-index-url \
        --custom-compile-command "scripts/convert-models.sh --lock" \
        --quiet --output-file "$LOCK"
    log "Wrote ${LOCK#"$ROOT"/}"
    exit 0
fi

# Recreate the environment when the lock or the Python version changes.
venv_stamp="$PYTHON_VERSION $(shasum -a 256 "$LOCK" | awk '{ print $1 }')"
if [ "$(cat "$VENV/goscriber-stamp" 2>/dev/null)" != "$venv_stamp" ]; then
    log "Creating the converter environment in ${VENV#"$ROOT"/}"
    rm -rf "$VENV"
    uv venv --quiet --python "$PYTHON_VERSION" "$VENV"
    # shellcheck disable=SC2086
    uv pip sync --quiet --python "$VENV/bin/python" --require-hashes $UV_PIP_ARGS "$LOCK"
    printf '%s\n' "$venv_stamp" >"$VENV/goscriber-stamp"
fi
PY="$VENV/bin/python"

mkdir -p "$out"
out="$(cd "$out" && pwd)"
# Convert into a staging directory under the same file names, then move the
# results in, so an interrupted run leaves no partial GGUF behind.
stage="$out/.convert-models.tmp"
rm -rf "$stage"
mkdir -p "$stage"
trap 'rm -rf "$stage"' EXIT

convert() { # convert SOURCE OUTFILE [converter arguments...]
    local source="$1" name="$2"
    shift 2
    log "Converting $source to $name"
    (cd "$SRC" && "$PY" convert_model.py "$source" --outfile "$stage/$name" "$@")
}

convert "$PARAKEET_SOURCE" "$PARAKEET_Q8" --revision "$PARAKEET_REVISION" --outtype q8_0
convert "$PARAKEET_SOURCE" "$PARAKEET_F16" --revision "$PARAKEET_REVISION" --outtype fp16
convert silero "$SILERO" --silero-version "$SILERO_VERSION"

for name in "$PARAKEET_Q8" "$PARAKEET_F16" "$SILERO"; do
    mv -f "$stage/$name" "$out/$name"
done
(cd "$out" && shasum -a 256 "$PARAKEET_Q8" "$PARAKEET_F16" "$SILERO" >SHA256SUMS)

log "Sizes and SHA-256"
for name in "$PARAKEET_Q8" "$PARAKEET_F16" "$SILERO"; do
    printf '  %-34s %11s bytes  %s\n' "$name" "$(wc -c <"$out/$name" | tr -d ' ')" \
        "$(awk -v n="$name" '$2 == n { print $1 }' "$out/SHA256SUMS")" >&2
done

log "Checking against ${SUMS#"$ROOT"/}"
(cd "$out" && shasum -a 256 -c "$SUMS") >&2 ||
    die "the GGUFs differ from the pinned SHA-256; see docs/models/gguf.md#reproducibility"

[ "$publish" = 1 ] || exit 0

HF="$VENV/bin/hf"
"$HF" auth whoami >/dev/null 2>&1 || die "not logged in to Hugging Face: run '$HF auth login' or set HF_TOKEN"

upload() { # upload REPO CARD_DIR FILE...
    local repo="$NAMESPACE/$1" card="$2" dir="$stage/$1" name
    shift 2
    # The card lists each file's SHA-256; refuse to publish a stale one.
    for name in "$@"; do
        grep -q "$(awk -v n="$name" '$2 == n { print $1 }' "$out/SHA256SUMS")" "$card/README.md" ||
            die "${card#"$ROOT"/}/README.md does not list the SHA-256 of $name"
    done
    mkdir -p "$dir"
    cp "$card"/* "$dir/"
    for name in "$@"; do ln "$out/$name" "$dir/$name" 2>/dev/null || cp "$out/$name" "$dir/$name"; done
    log "Uploading to https://huggingface.co/$repo"
    "$HF" upload "$repo" "$dir" . --no-private \
        --commit-message "Convert with NeMo-Speech.cpp ${commit:0:12} (Goscriber scripts/convert-models.sh)"
    printf '%s revision: %s\n' "$repo" \
        "$("$PY" -c 'import sys; from huggingface_hub import HfApi; print(HfApi().model_info(sys.argv[1]).sha)' "$repo")"
}

upload "$PARAKEET_REPO" "$CARDS/$PARAKEET_REPO" "$PARAKEET_Q8" "$PARAKEET_F16"
upload "$SILERO_REPO" "$CARDS/$SILERO_REPO" "$SILERO"
