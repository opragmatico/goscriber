# /// script
# requires-python = ">=3.11"
# dependencies = ["jiwer==4.0.0", "whisper-normalizer==1.0.1"]
# ///
"""Score nemo-eval results: the corpus word error rate of "pred_text" against
"text", after the Whisper English normalisation that the Hugging Face Open ASR
Leaderboard, and so NVIDIA's model cards, use. Also prints the decode speed.

    uv run scripts/wer.py results.jsonl...
"""

import json
import sys

import jiwer
from whisper_normalizer.english import EnglishTextNormalizer


def main(paths: list[str]) -> int:
    if not paths:
        print(__doc__.strip(), file=sys.stderr)
        return 2
    normalize = EnglishTextNormalizer()
    for path in paths:
        refs, hyps = [], []
        audio = decode = 0.0
        with open(path) as f:
            for line in f:
                r = json.loads(line)
                ref, hyp = normalize(r.get("text", "")), normalize(r["pred_text"])
                if ref.strip():  # the leaderboard drops empty references
                    refs.append(ref)
                    hyps.append(hyp)
                audio += r["duration"]
                decode += r["decode_ms"] / 1000
        print(
            f"{path}: WER {100 * jiwer.wer(refs, hyps):.2f}% over {len(refs)} utterances, "
            f"{audio / 3600:.2f} h of audio in {decode:.1f} s (RTFx {audio / decode:.0f})"
        )
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
