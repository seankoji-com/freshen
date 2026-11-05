# Visual refresh: priority-led dashboard and adaptive lists

Reviewed 5 November 2026 against `689c0c3`.

## Outcome

The five screens now share one hierarchy and one visual language: a priority-led
Overview, dense scan-friendly tables on wide terminals, a read-only preview of
the selection, and honest empty, loading and degraded states. The keyboard model,
screen semantics, data-honesty rules and leaf/mint identity are unchanged.

## What changed

- **Overview** — four operational tiles, grouped failures and review items, work
  in flight, repository health and recent runs. Wide terminals use the height
  for more rows and disclose overflow; narrow terminals use a 2×2 or compact
  summary. Empty workspaces get a get-started panel.
- **Lists** — one-row tables from 100 columns with fixed status, item, count and
  signal columns; a read-only preview of the selected repository, run, job or
  runner from 140. Repository rows show the newest CI run and upstream
  ahead/behind. Enter still opens the full screen.
- **Selection and contrast** — full-row selection with a left accent; the
  selection surface and muted sage meet WCAG AA for body text. A light palette
  (`FRESHEN_LIGHT=1`) has its own contrast tests.
- **Progress and status** — bars follow state (green success, red failure, blue
  running, amber queued, muted skipped); status is always glyph, word and colour;
  `NO_COLOR` keeps reverse-video selection and emits no colour.
- **States** — loading, partial, degraded, denied, empty and unread branches name
  what is missing and what to do; incomplete snapshots are review items, not
  fabricated failures; failed fetches do not look healthy.
- **Details** — short pages say `all content shown`; job logs highlight ISO
  timestamps, GitHub annotations, `FAIL`/`PASS`, failure words and success/failure
  glyphs; previews disclose clipping and cached/unavailable data.
- **Actions** — the menu and confirmation name the repository, branch, PR base,
  changed files and WIP commit message; default and unread branches refuse before
  starting work; compact confirmations ask for a resize and offer only cancel.
- **Help and navigation** — two balanced help columns from 100 columns, with the
  current screen's group first on narrow terminals; tab labels stay consistent;
  Concerns opens PRs (or Issues); empty lists stop advertising dead actions.


## Design run

- Baseline `34b8332`: primary 7.1/10.
- Final candidate `a1ebdd6`: primary 10.0, secondary 9.5 on the frozen rubric
  (target 9.5 for every judge).
- Judges: GPT-6.1 Sol (primary) and Claude Opus 5.5 (secondary); implementor
  DeepSeek V4.1 Flash.
- Scoped outcome: scan a folder of sibling repositories, see what is behind,
  dirty, failing or waiting on CI, and act without leaving the terminal.
- Reference anchors: gh-dash, k9s Pulses and btop.
- Evidence: seeded real-TUI captures at 30×10 through 200×50 in the run
  artifact store; the committed README screenshot is the final Overview.

## Known minor issues (deferred)

The accepted candidate meets the design target. These confirmed edge cases are
tracked in [#183](https://github.com/seankoji-com/freshen/issues/183) rather
than changing the accepted visual design:

- Log lines for repositories or branches named exactly `failed` or `failure`
  render red even when the line is informational.
- Recursive `make[1]: … Error 2` and `2 failures, 0 errors` lines remain neutral.
- A very long branch name can truncate to an uninformative prefix in the detail
  header at 80–160 columns.
- At 40×12 the deliberate three-line detail header can leave no log row visible
  until scrolling; a one-line header fits for ordinary names.
- The repository preview does not state an unread default branch.

## Verification

Local only; hosted CI, merge and live acceptance are separate.

- `go build ./...` — pass
- `GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 go test -race -count=1 ./...` — pass
- `GOTOOLCHAIN=go1.26.5 make lint` — 0 issues
- Frame bounds from 30×10 to 200×50 across screens, detail, help, menu, empty,
  loading, degraded and NO_COLOR — pass
- NO_COLOR: no colour SGR; reverse selection present, including an open empty filter
- Contrast: dark and light palettes measured at or above 4.5:1 for body text
- `BenchmarkOverviewView200` — about 0.5–0.7 ms/op; `BenchmarkEntriesLarge`
  (200 repositories, 400 runs) — about 1.1–1.4 ms/op after indexing runs once

The seeded demo fixture is captured with `scripts/design-loop/capture.sh`; see
that directory for reproducible frames. The README screenshot is regenerated
from the shipped layout.

## Limits

Captures use synthetic data and suppress external commands; they do not prove
live GitHub or mutation behaviour. The app inherits the terminal background; the
palette is assumed dark unless `FRESHEN_LIGHT=1` or a light `COLORFGBG` selects
the light theme. Displayed divergence is measured against the last-fetched
remote-tracking ref.
