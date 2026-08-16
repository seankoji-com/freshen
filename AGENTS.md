# AGENTS.md — freshen

> Token-efficient handbook for Claude, OpenCode, AGY, and other coding agents.
> Read this first, then pull supplementary docs in `docs/` as needed.
> Humans: see [CONTRIBUTING.md](CONTRIBUTING.md) for the dev workflow.

## What this repo is

**freshen** is a Go TUI app (Bubble Tea + Lip Gloss) that manages GitHub org repos and monitors GitHub Actions CI runners/jobs. Entry: `main.go`. Built with `go build -o freshen main.go`.

## Quick reference

| Concern | File |
|---|---|
| Entry point | `main.go` |
| Git/repo operations | `pkg/git/git.go` |
| CI runner & job polling | `pkg/jobs/jobs.go` |
| TUI (model, view, update) | `pkg/tui/tui.go` |
| Tests | `*_test.go` alongside source |

## How to build & test

```bash
go build -o freshen main.go    # build
go test ./...                  # test
```

## External tools required at runtime (not build-time)

- **git** — all repo operations shell out to the `git` binary
- **gh** (GitHub CLI) — all API calls use `gh` via `os/exec`; must be authenticated
- **pbcopy** (macOS) — clipboard copy in TUI

## Key patterns

- **No Go GitHub SDK** — all GitHub interaction is `exec.Command("gh", ...)` + JSON parsing
- **Bubble Tea Elm Architecture** — `Init() tea.Cmd`, `Update(tea.Msg) (tea.Model, tea.Cmd)`, `View() string`
- **Enums** are string type aliases: `type RepoStatus string` with `const (StatusPending RepoStatus = "PENDING")`
- **Concurrency** — repo sync uses a semaphore channel (concurrency=4) with `sync.WaitGroup`
- **Logs** are `[]string` on model items, no structured logging library

## Deep dives (read as needed)

- `docs/architecture.md` — full package layout, data flow, type catalog, function catalog
- `docs/conventions.md` — code patterns, naming, testing style, Lip Gloss conventions
- `docs/dependencies.md` — Go modules, external binaries, build requirements
- `docs/persona-review-setup.md` — persona review CI: routine/App/secret setup, scripts, validation checklist
