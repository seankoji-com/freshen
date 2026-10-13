# Architecture

`main.go` selects interactive Bubble Tea or non-interactive batch mode. Runtime
GitHub operations use the authenticated `gh` CLI; repository operations use `git`.

| File | Responsibility |
|---|---|
| `pkg/git/git.go` | Repository discovery, clone/sync, branches, worktrees and PR operations |
| `pkg/jobs/jobs.go` | Typed workflow runs/jobs/steps, polling, result mapping and logs |
| `pkg/tui/model.go` | Model, messages and polling constants |
| `pkg/tui/theme.go` | The only palette and style definitions; status kinds, glyphs and labels; `NO_COLOR` |
| `pkg/tui/keymap.go` | Every key binding, enabled per context; footer hints and help-overlay groups |
| `pkg/tui/chrome.go` | Header, tab row geometry, breadcrumb, status row, footer and help overlay |
| `pkg/tui/screens.go` | Screen entries, identity, list layout, progress and detail content |
| `pkg/tui/screen_keys.go` | Key and mouse dispatch, navigation, filtering and run-detail requests |
| `pkg/tui/actions.go` | Action menus, captured confirmation targets and background results |
| `pkg/tui/keys.go` | Quit and bulk-sync helpers |
| `pkg/tui/commands.go` | Background commands, streaming sync snapshots and API budget |
| `pkg/tui/update.go` | Message dispatch and reconciliation |
| `pkg/tui/view.go` | Repository detail content, status badges, runner matching and tag helpers |

## State and navigation

Overview, Repositories, Actions, Runners and Concerns are separate screens.
Repository details use Logs/Branches/Issues/PRs tabs. Actions has run and job-queue views; a run opens its
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

`keyMap` in `keymap.go` defines every binding once. `Model.keys()` enables
the bindings valid in the current context; the key handler matches only those,
and the footer and `?` overlay render from the same value, so the help cannot
list a key the handler ignores. Global bindings (`0`–`4`, Tab, Esc, `/`, `r`,
`?`, `q`) never share a key with a screen binding and stay active on every
screen and over the help overlay. The filter input, action menu and
confirmations are modal: they enable only their own keys plus `Ctrl+C`.

The frame is a header (owner and data age), tab row, breadcrumb row, body,
status row and key-hint row; `bodyHeight` is the terminal height minus those
five rows. Every row is clipped to the terminal width. The tab row tries full
names with count badges, then shorter labels; mouse hit testing uses the same
spans. On the breadcrumb row the control is sized first: the filter input gets
the cells left after a breadcrumb capped at a third of the row (`sizeSearch`
re-seats its cursor on resize), and repository detail tabs fall back to short
labels or the active tab alone. `fitCrumbs` then shortens ancestors, collapses
them to `…`, and only last truncates the current crumb. Detail viewports and
the help overlay wrap to the body width, so nothing is reachable only past the
right edge; a resize clamps the help offset to the rewrapped rows. The action
menu budgets rendered rows, cutting its heading to one row and dropping detail
first, so the selected action stays visible. When the header cannot fit the
data age, a refresh in flight keeps a compact spinner (`⣾ refreshing`, then the
spinner alone), so cached rows never pass for idle ones. Below 30 × 10 a resize
notice replaces the frame; its quit hint comes from the keymap, so it offers
`q` only where `q` works and `ctrl+c` inside the filter, menu or a confirmation.

Toasts carry a sequence number and expire through `toastExpiredMsg`; a stale
expiry cannot clear a newer toast, and rendering also hides a toast past its
lifetime in case the message is dropped.

## Polling and failure states

Startup reads metadata. Repository metadata refreshes every five minutes. Runner
polling uses a ten-second baseline while Overview or Runners is open. Other
screens do not render runner data, so the poll pauses there; entering Overview or
Runners refreshes runners when the last attempt is older than the backed-off
interval. Organisation Actions polling starts at twenty seconds and scales with
repository/active-run count to budget API calls. Pollers use independent
exponential backoff after failures. `gh` reads, log requests and repository
branch reads run under deadlines. An open
running job's log refreshes with one request when its job ID is known; the jobs
list is fetched only to resolve a name or to build the step-status fallback.
One log request runs at a time (`LogLoading`); runner ticks and queue
completions skip while it is in flight. The download and any jobs-list pages
share one context with a `logFetchTimeout` deadline, so the request answers
within it even when each `gh` call is slow. A request still unanswered after
`logFetchStuckAfter` is cancelled before a replacement starts, and `LogSeq`
discards its late response. Opening another job replaces a request in flight
for the job left the same way, so the open job's log loads at once. Every log response re-renders the detail, which
states only the latest outcome (`LogErrors`, `EmptyLogs`); a tail kept from an
earlier fetch is labelled, never left as "Loading logs…".

Repository detail starts its requests before rendering (`loadRepoDetail`), so
the first frame shows them loading. Branch reads run one at a time; opening
another repository while one is in flight queues it (`RepoDetailQueued`) and the
completion starts it if that detail is still open. One `repoDetailTimeout`
deadline covers a read's branch, worktree and status commands, so a hung `git`
ends the request. `GetRepoBranchDetails` records each failed command in
`BranchesErr`, `WorktreesErr` or `StatusErr`; the detail states the failure with
an `r` retry hint instead of "none found" or "working tree is clean", and keeps
entries from an earlier read under a "From an earlier read" label. Spinner ticks re-render an
open detail only while it is waiting on a request.

A runner refresh failure leaves a status-row warning on Overview and Runners
with the age of the cached runners and a retry hint, after the error toast
expires; Runners' summary adds "last refresh failed".

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
