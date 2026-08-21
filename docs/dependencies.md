# Dependencies

## Runtime requirements

- **Go** 1.26+ (compiled binary is self-contained)
- **Git** — all git operations use the local `git` binary
- **GitHub CLI (`gh`)** — all GitHub API calls go through `gh`. Must be authenticated (`gh auth status`). Used for:
  - `gh repo list` — org repository enumeration
  - `gh api graphql` — issue/PR counts
  - `gh api /orgs/{org}/actions/runners` — runner status
  - `gh api /repos/{org}/{repo}/actions/runs` — workflow runs
  - `gh api /repos/{org}/{repo}/actions/runs/{id}/jobs` — job details
  - `gh api /repos/{org}/{repo}/actions/jobs/{id}/logs` — raw log text
  - `gh issue list` / `gh pr list` — per-repo issue/PR details
  - `gh pr create` — creating pull requests
  - `gh repo view` — default branch detection
  - `gh repo clone` — cloning new repos
- **macOS**: `pbcopy` is used for clipboard operations

## Go module dependencies

```
github.com/charmbracelet/bubbles v1.0.0      # Spinner, progress bar, viewport widgets
github.com/charmbracelet/bubbletea v1.3.10   # TUI framework (Elm Architecture)
github.com/charmbracelet/lipgloss v1.1.0     # Terminal styling & layout
```

All other Go dependencies are transitive (ansi, term, cellbuf, etc.) and pulled in by the above three.

## Local scripts

## No external services

The app is entirely local — it shells out to `git` and `gh` but has no server, database, or network service of its own. No environment variables required beyond `$HOME` (for default repo directory) and standard `gh` auth.

## Build

```bash
go build -o freshen main.go    # produces a single binary
go test ./...                  # runs all tests
```

No Docker image. CI/CD via GitHub Actions in .github/workflows/.

## Go version requirement

Go 1.26+ (go.mod: 1.26.5).
