---
name: code-review
description: Review priorities for freshen pull requests, what deserves real scrutiny versus what to skip. Use for every PR review.
---

# Review priorities

freshen is a Go Bubble Tea/Lip Gloss TUI that shells out to `git`/`gh` (no Go
GitHub SDK) to sync repos, poll CI runners/jobs, and delete archived repos
from disk. Review priorities below come from the actual pattern in this
repo's closed-PR history, not generic Go advice.

## Spend real attention here
- **Async state sync in `pkg/tui/` (`update.go`, `commands.go`).** The
  single largest recurring bug class here: panels stuck on "Fetching...",
  toasts stuck forever, job-queue corruption, click/scroll mismaps, caused by
  concurrent repo-sync/runner/job-poll results racing against model state.
  On any `Update()`/`Cmd`/message change, check that stale async results
  can't clobber newer state and that every new message variant is handled.
- **Terminal rendering math in `pkg/tui/view.go`.** Second-largest recurring
  class: Lip Gloss cell-width/alignment, table row wrapping, viewport
  scroll, footer overflow. Scrutinize width/padding arithmetic touching
  ANSI-styled or variable-width strings.
- **Untrusted `git`/`gh` output reaching the terminal or filesystem**
  (`pkg/git/`, `pkg/jobs/`). Past fixes sanitized GitHub-sourced text before
  terminal render and hardened path joins (`GetLocalDirName`,
  `filepath.Join`) against repo/branch-derived strings. Treat any new code
  building shell args, joining paths from external strings, or printing
  GitHub API text as security-sensitive.
- **Destructive-action paths**: the `dd` archived-repo delete (os.RemoveAll
  via git.DeleteLocalRepo, guarded by ValidateWorkspacePath) and `X` prune
  (removes worktrees/local branches). Changes to confirmation state or key
  handling here are high-stakes — a bypassed confirm deletes user data.

## Do not spend attention here
- `docs/*.md` (architecture.md, conventions.md, dependencies.md) and
  `docs/assets/` — reference prose and a screenshot, not logic.
- `manifests/SeanKoji.Freshen.yaml` — generated winget packaging metadata.
- `.github/workflows/*.yml` — synced from `seankoji-com/.github` (5 of the
  last 20 PRs were exactly this sync); local review gets overwritten on the
  next sync.
- Formatting already enforced by `make lint` (golangci-lint/gofmt/goimports),
  and `go.mod`/`go.sum` bumps already covered by Dependabot auto-merge.

## Comment style
- One comment per real issue, not one per file it repeats in.
- Skip restating what CI or lint already flags.
