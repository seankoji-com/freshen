#!/usr/bin/env bash
# Capture a freshen TUI screen as HTML and PNG without contacting GitHub or git.
#
#   scripts/design-loop/capture.sh OUT_PREFIX WIDTH HEIGHT [KEYS...]
#
# OUT_PREFIX.html and OUT_PREFIX.png are written. KEYS are tmux send-keys
# arguments sent in order before the screen is captured ("2", "Enter", "Down",
# "?", "/"). Set NO_COLOR=1 to capture the colourless theme.
#
# The demo fixture runs the real Bubble Tea model with seeded data and drops
# external commands, so captures are deterministic and safe.
set -euo pipefail

if [ "$#" -lt 3 ]; then
  echo "usage: $0 OUT_PREFIX WIDTH HEIGHT [KEYS...]" >&2
  exit 2
fi

out=$1
width=$2
height=$3
shift 3

root=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
sock="freshen-design-$$"
cleanup() {
  tmux -L "$sock" kill-server 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT

go test -c -o "$tmp/freshen-ui.test" "$root/pkg/tui"

tmux -L "$sock" new-session -d -s cap -x "$width" -y "$height" \
  "FRESHEN_DEMO=1 '$tmp/freshen-ui.test' -test.run TestTerminalDemo"
sleep 1.6

for key in "$@"; do
  tmux -L "$sock" send-keys -t cap "$key"
  sleep 0.35
done
sleep 0.5

tmux -L "$sock" capture-pane -e -p -t cap >"$tmp/ansi.txt"

mkdir -p "$(dirname "$out")"
python3 "$root/scripts/design-loop/ansi_to_html.py" <"$tmp/ansi.txt" >"${out}.html" 2>/dev/null

chrome=$(command -v google-chrome || command -v chromium || command -v chromium-browser || true)
if [ -n "$chrome" ]; then
  px_w=$((width * 9 + 48))
  px_h=$((height * 19 + 48))
  "$chrome" --headless=new --no-sandbox --disable-gpu --hide-scrollbars \
    --force-device-scale-factor=2 --user-data-dir="$tmp/chrome" \
    --window-size="${px_w},${px_h}" \
    --screenshot="${out}.png" "file://$(cd "$(dirname "$out")" && pwd)/$(basename "$out").html" \
    >/dev/null 2>&1
fi

echo "${out}.html"
[ -f "${out}.png" ] && echo "${out}.png"
