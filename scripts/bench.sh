#!/usr/bin/env bash
# Benchmarks `goscriber transcribe` with both models, on Metal (or the GPU) and
# on the CPU, over WAV files, and prints a Markdown table: load time, decode
# time, real-time factor, how long the final transcript takes after the end of
# the audio, and the word error rate against FILE.txt when there is one.
#
#   make bench                       # builds ./goscriber, then runs this over testdata/audio
#   scripts/bench.sh [FILE.wav...]
#
# Needs the engine (make deps) and the models (goscriber models pull ...).
# GOSCRIBER_BIN picks another binary. Each run loads the model once and warms
# it up with an untimed decode of the first file (transcribe --bench).
set -euo pipefail

cd "$(dirname "$0")/.."
bin=${GOSCRIBER_BIN:-./goscriber}
if [[ $# -gt 0 ]]; then files=("$@"); else files=(testdata/audio/*.wav); fi
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT

# model, extra flags
runs=(
  "nemotron-3.5-asr-streaming-0.6b|"
  "nemotron-3.5-asr-streaming-0.6b|--stream"
  "parakeet-tdt-0.6b-v2|"
)
n=0
for run in "${runs[@]}"; do
  model=${run%%|*}
  flags=${run#*|}
  for backend in gpu cpu; do
    n=$((n + 1))
    args=(transcribe --bench --json --model "$model")
    [[ -n $flags ]] && args+=("$flags")
    [[ $backend == cpu ]] && args+=(--cpu)
    echo "bench: ${args[*]} ${files[*]}" >&2
    if ! "$bin" "${args[@]}" "${files[@]}" >"$out/$n.jsonl" 2>"$out/$n.log"; then
      grep -v '^\[asr\]' "$out/$n.log" >&2
      exit 1
    fi
  done
done

python3 -I - "$out" <<'PY'
import glob, json, os, re, sys

def words(text):
    return re.sub(r"[^\w\s']", " ", text.lower()).split()

def wer(ref, hyp):
    r, h = words(ref), words(hyp)
    d = list(range(len(h) + 1))
    for i, rw in enumerate(r, 1):
        prev, d[0] = d[0], i
        for j, hw in enumerate(h, 1):
            prev, d[j] = d[j], min(d[j] + 1, d[j - 1] + 1, prev + (rw != hw))
    return d[len(h)] / max(len(r), 1)

rows = []
for path in sorted(glob.glob(os.path.join(sys.argv[1], "*.jsonl")), key=lambda p: int(os.path.basename(p).split(".")[0])):
    for line in open(path):
        r = json.loads(line)
        ref_path = os.path.splitext(r["file"])[0] + ".txt"
        score = f"{100 * wer(open(ref_path).read(), r['text']):.1f} %" if os.path.exists(ref_path) else "n/a"
        rows.append([
            r["model"] + " " + r["quant"],
            "stream" if r["streamed"] else "offline",
            {"metal": "Metal", "cpu": "CPU", "gpu": "GPU"}.get(r["backend"], r["backend"]),
            os.path.basename(r["file"]),
            f"{r['audio_duration']:.1f} s",
            f"{r['load_time']:.2f} s",
            f"{r['decode_time'] * 1000:.0f} ms",
            f"{r['real_time_factor']:.4f}",
            f"{r['finalize_latency'] * 1000:.0f} ms",
            score,
        ])
head = ["Model", "Mode", "Backend", "File", "Audio", "Load", "Decode", "RTF", "Final after end", "WER"]
print("| " + " | ".join(head) + " |")
print("| " + " | ".join("---" for _ in head) + " |")
for row in rows:
    print("| " + " | ".join(row) + " |")
PY
