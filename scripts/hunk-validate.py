#!/usr/bin/env python3
"""Validate that review comments fall within PR diff hunks.

Reads comment JSON from stdin and hunks JSON from the first positional argument.
Outputs {"in_hunk": [...], "out_of_hunk": [...]} to stdout.

GitHub review comments must anchor on lines within diff hunks. Comments whose
line ranges are not fully contained in a hunk are classified as out_of_hunk
(typically demoted to the review summary by the calling script).
"""

import json
import re
import sys


def build_hunk_ranges(hunks_raw: list) -> dict:
    """Parse hunks JSON into filename -> [(start, end), ...] mapping."""
    hunks: dict[str, list[tuple[int, int]]] = {}
    for f in hunks_raw:
        fn = f.get("filename", "")
        patch = f.get("patch", "")
        ranges: list[tuple[int, int]] = []
        for m in re.finditer(r"@@ -(\d+)(?:,\d+)? \+(\d+)(?:,(\d+))? @@", patch):
            start = int(m.group(2))
            cnt = int(m.group(3)) if m.group(3) else 1
            ranges.append((start, start + cnt - 1))
        if ranges:
            hunks[fn] = ranges
    return hunks


def _safe_int(value) -> int:
    """Coerce a value to int, returning 0 on failure (None, string, boolean, etc.)."""
    if value is None:
        return 0
    try:
        return int(value)
    except (TypeError, ValueError):
        return 0


def in_hunk(comment: dict, hunks: dict) -> bool:
    """Check if a comment's line range is fully within any hunk for its file."""
    ds = hunks.get(comment.get("path", ""), [])
    sa = _safe_int(comment.get("start_line") or comment.get("end_line"))
    sb = _safe_int(comment.get("end_line") or sa)
    if sa < 1 or sb < 1:
        return False
    return any(sa >= rs and sb <= hunk_end for rs, hunk_end in ds)


def main():
    if len(sys.argv) < 2:
        print("Usage: hunk-validate.py <hunks.json>", file=sys.stderr)
        sys.exit(2)

    # Parse comments from stdin
    try:
        comments = json.load(sys.stdin)
    except json.JSONDecodeError as e:
        print(f"Error parsing comments JSON from stdin: {e}", file=sys.stderr)
        sys.exit(1)

    # Parse hunks from file
    try:
        with open(sys.argv[1]) as f:
            hunks_raw = json.load(f)
    except FileNotFoundError:
        print(f"Error: hunks file not found: {sys.argv[1]}", file=sys.stderr)
        sys.exit(1)
    except json.JSONDecodeError as e:
        print(f"Error: invalid JSON in hunks file: {e}", file=sys.stderr)
        sys.exit(1)
    except OSError as e:
        print(f"Error reading hunks file: {e}", file=sys.stderr)
        sys.exit(1)

    hunks = build_hunk_ranges(hunks_raw)

    in_hunk_list = []
    out_of_hunk_list = []
    for c in comments:
        if in_hunk(c, hunks):
            in_hunk_list.append(c)
        else:
            out_of_hunk_list.append(c)

    print(json.dumps({"in_hunk": in_hunk_list, "out_of_hunk": out_of_hunk_list}))


if __name__ == "__main__":
    main()
