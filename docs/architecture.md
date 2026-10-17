# Architecture

`main.go` selects interactive Bubble Tea or non-interactive batch mode. Runtime
GitHub operations use the authenticated `gh` CLI; repository operations use `git`.

| File | Responsibility |
|---|---|
| `pkg/git/git.go` | Repository discovery, clone/sync, branches, worktrees and PR operations |
| `pkg/jobs/jobs.go` | Typed workflow runs/jobs/steps, polling, result mapping and logs |
| `pkg/proc/` | Starts every `git` and `gh` command: `WaitDelay`, own process group, group kill on cancel and after exit, a bounded wait for commands that cannot be started or reaped |
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
exponential backoff after failures. `gh` reads, log requests, repository
branch reads and repository changes (clone, switch, push, prune) run under
deadlines. An open
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
ends the request. Every repository branch-state read also bounds itself with
`git.branchReadTimeout` (30 seconds): the checkout check (`IsGitRepo`),
`GetOriginalBranch`, the default-branch lookup, `FetchExistingPRURL`, the merge
and dirty checks and `GetRepoBranchDetails`. The checkout check's filesystem
probe (`statBounded`) returns at that deadline even if the filesystem never
answers; a probe that outlives it stays registered by path, and later checks
of that path wait on it rather than start another, so a hung mount holds at
most one goroutine per path. The check reports "not a checkout" only for a
missing path or a directory with no `.git` entry that git rejects; any other
git or filesystem failure (permissions, ownership, corruption, a hang) is an
error carrying git's diagnostic. A sync treats its preflight reads (checkout,
current branch, default branch, unfinished merge, dirty state) as required: if
one fails or times out, the sync ends with status `ERROR` / "Read Err" and the
reason before any clone, checkout, stash or pull, instead of acting on a
placeholder `HEAD`, cloning over an unreadable checkout or assuming a clean
tree. Changes are bounded too: a sync of an existing checkout by `syncTimeout`
(10 minutes), a first clone by `cloneTimeout` (30 minutes, which reports that a
partial clone may remain), and a switch by `checkoutTimeout` (2 minutes). The
TUI runs switch, push, prune and the archived-repository delete under one
`repoActionTimeout` (5 minutes); a stopped action reports "stopped after", and
steps that finished before it (a push that landed, branches already deleted)
stay in the repository log. Quitting cancels all of them.

Every `git` and `gh` command in `pkg/git` and `pkg/jobs` starts through
`proc.Command`. It sets `WaitDelay` (5 seconds), so a hook or helper that
keeps stdout or stderr open after the command exits cannot hold `Wait`;
`proc.Run`, `proc.Output` and `proc.CombinedOutput` then let the exit status
decide success, because the command's own output was already read. On Unix the
command runs in its own process group. Cancelling kills the whole group,
including the `git` a `gh repo clone` starts, and waits up to
`proc.ReapTimeout` (5 seconds) for it to empty. When the command exits on its
own, successfully or not, `proc.Run` kills whatever is left of its group the
same way before returning, so no process a command started outlives it: a `gh
repo clone` that fails while its `git` child keeps writing has that child
stopped before the `git clone` fallback runs. `proc.Run` also stops waiting
`ReapTimeout`+`WaitDelay`+1 second after the context ends, even if the command
has not been reaped (a process blocked in the kernel on a hung mount ignores
SIGKILL, and `exec.Cmd.Wait` would block on it) or its start has not returned
(Go watches the context only once the process exists, and on Unix the start
waits for the child to exec, which a hung mount can block); output written
after that is discarded, so the caller reads its buffers safely. In both
cases, a command that cannot be stopped, or a leftover that survives the
kill, returns a `*proc.StillRunningError` (`errors.Is(err, proc.ErrSurvived)`)
whose `Done` channel closes once the start has returned, the command is reaped
and its group is empty. A command whose start returns late is killed by
exec's context watcher if it is still running; one that has already exited
can be reaped without the watcher cancelling (Go installs it only after the
start returns), so `proc.Run` then kills whatever is left of its group, as
after any exit, before `Done` closes. `proc.Command` likewise stops waiting for the PATH lookup of the
executable when the context ends; that command fails to start with the
context's error, and the abandoned lookup starts nothing. freshen only stops
waiting: Go cannot interrupt either call, so the goroutine running a stuck
lookup or start stays blocked until the kernel returns. On Windows, cancelling kills the command itself and
`WaitDelay` still applies.

`pkg/git` runs commands through `runCmd`/`outputCmd`/`combinedCmd`, which
know the path a command acts on (`git -C <path>`, a clone's destination, or
the working directory). A `StillRunningError` holds that path
(`ErrUnfinishedChange`) until `Done` closes, and no command starts on a held
path, not even a read. So a sync, switch, push or prune whose command could not
be stopped returns at its deadline, clears `IsSyncing`/`BusyAction`, and its
log says the command "may still be running"; until the process exits, sync,
switch, push, prune, clone, delete and branch reads on that path (or any path
inside or above it) refuse with "may still be running" rather than act
alongside it.

Filesystem changes have deadlines too. Creating a clone's parent directory
shares the clone's `cloneTimeout`; the delete validates and removes the path
under the action's deadline; prune's worktree probes each get
`branchReadTimeout`. Go cannot interrupt a blocked filesystem call, so a change
that outlives its deadline keeps running in the background and its path is
held the same way until it ends. A change only starts while its context is
live: `changeBounded` refuses a context that has already ended, and a change
calls a start gate immediately before its first write. The caller's deadline
and the gate decide once which comes first, so the archived-repository delete
never starts `os.RemoveAll` after the action has reported it stopped: a
cancelled delete, or one whose path validation finishes after the deadline,
reports "nothing was changed" and removes nothing (the path stays held only
while validation is still reading).

A sync re-reads branches, worktrees and changed files after its last change, so
its final snapshot describes the checkout it left (for example `main` checked
out after switching from a clean feature branch); a re-read that fails marks
its sections failed, and the detail labels the entries it keeps as earlier
data. A hung `GetRepoBranchDetails` only marks its sections failed; the sync
still finishes and clears `IsSyncing`. An action re-reads branch state under
one `repoDetailTimeout` deadline after its mutation: a hung read still delivers
`actionResultMsg` and clears `BusyAction`, keeps the mutation's outcome and the
last known branch, and logs the failed read. Each sync or action snapshot, and
each refresh that finds a repository on a different branch, bumps the
repository's `BranchDetailsRev`; a detail read records the revision when it
starts and is discarded if its branch state was replaced first, so an older
read cannot restore pruned branches, a previous checkout's branches or a stale
working-tree status. A refresh that finds the checkout moved marks the details
`Unread` instead of empty: the detail says "Not read since the checkout moved"
rather than "none found" or "working tree is clean", and an open detail reads
them again (a read already running is discarded on arrival and restarted).
Snapshots also bump `RepoStateRev`, which `startOrgRefresh` copies into
`OrgRefreshRevs`; when the refresh answers, a repository whose revision moved
in the meantime keeps the snapshot's branch, status, details and logs and
raises no "branch changed" toast, so a refresh that read the checkout before a
switch or sync cannot undo it or retarget the next action. Snapshots carry
`IsNew`, so a finished clone stops offering "Clone repository" at once and an
older refresh cannot set it back. If the branch reads after a clone fail, the
clone is kept, the unread branches stay empty (unresolved) rather than `HEAD`,
the status reads "Branch unread" and the log asks for a refresh; prune and
publish refuse an unresolved default branch.

An organization refresh probes each checkout with `git.DirExists` under its own
deadline, then reads an existing one with `git.ReadLocalCheckout` (checkout
check, current branch and local default branch, each under
`branchReadTimeout`). A missing path means "not cloned" and runs no `git`; a
probe or checkout check that fails or gets no answer does not mean absent, and
a branch read that fails never blanks a known branch: the repository keeps the
previous branch, is marked stale, raises no "branch changed" toast, and the
failure is reported as incomplete local metadata. The local-only refresh (no
organization) does the same, so an ordinary git failure no longer drops a known
repository from the list; an unreadable directory with no earlier record is
listed as "Read Err". The repository keeps its earlier record, marked stale, or, with
none, is listed as "Read Err" with `CheckoutUnknown` set: an archived one is not
dropped and offers no delete, and an active one offers only a sync, which
probes again before it clones or pulls. The refresh reports the failures as
incomplete local metadata. `GetRepoBranchDetails` records each failed command in
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
