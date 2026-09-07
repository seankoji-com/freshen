# Architecture

`main.go` selects interactive Bubble Tea or non-interactive batch mode. Runtime
GitHub operations use the authenticated `gh` CLI; repository operations use `git`.

| File | Responsibility |
|---|---|
| `pkg/git/git.go` | Repository discovery, clone/sync, branches, worktrees and PR operations |
| `pkg/jobs/jobs.go` | Typed workflow runs/jobs/steps, polling, result mapping and logs |
| `pkg/tui/model.go` | Model, messages, polling constants and shared styles |
| `pkg/tui/screens.go` | Screen entries, identity, layout, progress and detail content |
| `pkg/tui/screen_keys.go` | Navigation, search, mouse input and run-detail requests |
| `pkg/tui/actions.go` | Action menus, captured confirmation targets and background results |
| `pkg/tui/keys.go` | Key dispatch, quit and bulk sync |
| `pkg/tui/commands.go` | Background commands, streaming sync snapshots and API budget |
| `pkg/tui/update.go` | Message dispatch and reconciliation |
| `pkg/tui/view.go` | Repository details, status styles, runner matching and tag helpers |

## State and navigation

Repositories, Actions and Runners are separate screens. Repository details use
Logs/Branches/Issues/PRs tabs. Actions has run and job-queue views; a run opens its
jobs, and a job opens steps and log tail. Stable keys drive selection and mouse
hit testing. A response for a previously opened run cannot replace another run.

`RunItem` owns the workflow invocation's ID, attempt, workflow name, display title,
trigger and status. `JobItem` owns job ID, status, runner, labels, steps and timing.
The queue transport includes explicit `IsRunHeader` records; renderers count only
actual jobs. Completed runs load jobs on demand. Retained job details belong to the
same completed attempt; a new attempt never inherits old jobs or logs.

All model mutation occurs in `Update`. Background operations use private repository
snapshots and return messages. Repository sync uses a semaphore and streams snapshots.
Destructive confirmation captures the repository before the command starts. A busy
operation prevents overlapping repository mutations while navigation stays available.

## Polling and failure states

Startup reads metadata. Repository metadata refreshes every five minutes. Runner
polling uses a ten-second baseline; organisation Actions polling starts at twenty
seconds and scales with repository/active-run count to budget API calls. Pollers use
independent exponential backoff after failures. Read commands have bounded execution.

Recent runs are bounded to thirty per repository. A full recent page triggers
separate active-state queries; job lists are paginated. Partial repository failures
are surfaced. Failed job-detail requests retain run metadata and an error marker.
Fleet permission failures fall back to observed runner assignments, with unknown
availability. A disappearing job never implies success.

Bubbles supplies text input, help, progress, spinner and viewport components. Lip
Gloss supplies screen tabs, selection and borders. Progress counts completed jobs
or steps, never elapsed-time estimates. Rendering and input share row geometry.

See [the UX audit](ux-audit.md) for findings, verification and limits, and
[conventions](conventions.md) for package conventions.
