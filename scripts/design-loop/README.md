# Design-loop screen capture

Deterministic captures of the real Bubble Tea model for design review. The demo
fixture seeds repositories, runs, jobs and runners, and drops every external
command, so a capture never contacts GitHub or mutates a checkout.

```bash
scripts/design-loop/capture.sh /tmp/overview 120 30 0
scripts/design-loop/capture.sh /tmp/actions  120 30 2
scripts/design-loop/capture.sh /tmp/help     120 30 "?"
NO_COLOR=1 scripts/design-loop/capture.sh /tmp/nocolor 80 24 0
```

Each call writes `OUT.html` and `OUT.png`. Trailing arguments are `tmux
send-keys` names sent in order before capturing (`0`–`4`, `Tab`, `Enter`,
`Escape`, `Down`, `?`, `/`). The PNG is rendered from `tmux capture-pane -e`
through `ansi_to_html.py` and headless Chrome, so colours, reverse video and
Unicode cell widths are preserved.

Set `DESIGN_BG` and `DESIGN_FG` when capturing a themed UI on a different
terminal background, for example `DESIGN_BG=#FFFFFF DESIGN_FG=#1B2A22
FRESHEN_LIGHT=1 scripts/design-loop/capture.sh out 120 30 1`.

Requirements: Go, tmux, Python 3, and Google Chrome or Chromium. The script
builds the test binary in a self-cleaning temporary directory.

This directory is review tooling, not part of the application build.
