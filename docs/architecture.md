# Architecture

## Entry point

`main.go` — parses flags (`-dir`, `-org`, `-y/-non-interactive`, `-v/-version`), then either:
- Non-interactive: calls `runNonInteractive()` which does sequential clone/sync
- Interactive: launches `tui.NewModel(targetDir, orgFlag)` via `tea.NewProgram()`

## Package layout

```
main.go                   # CLI entry, flag parsing, non-interactive path
pkg/
  git/git.go              # Git operations, GitHub API (gh CLI wrapper), repo model
  jobs/jobs.go            # GitHub Actions runner & job queue polling, data types
  jobs/jobs_test.go       # Unit tests for filtering, sorting, merging
  tui/tui.go              # Bubble Tea Model, Update, View, viewport rendering
  tui/tui_test.go         # TUI rendering & behavior tests
```

## Package `pkg/git` — Repository sync engine

**Core types:**
- `RepoItem` — the main model for a local repo. Fields: Name, GHRepoName, Path, CurrentBranch, DefaultBranch, Status, Logs, BranchDetails, IssuesList, PRsList, etc.
- `RepoStatus` — enum: PENDING, SYNCING, UP_TO_DATE, UPDATED, STASHED_APPLIED, SWITCHED_DEFAULT, REBASED, REBASE_CONFLICT, PR_CREATED, CLONED, ARCHIVED, ERROR
- `GHRepoInfo`, `RepoCounts`, `IssueItem`, `PRItem`, `BranchWorktreeDetails`

**Key functions:**
- `FetchOrgRepos(org)` — shells out to `gh repo list` (JSON), returns all org repos
- `FetchOrgRepoCounts(org)` — GraphQL query for open issues/PRs per repo
- `SyncRepository(item)` — the core workflow. Determines default branch, checks dirty state, then:
  - On default, clean: `git pull`
  - On default, dirty: `git stash && git pull && git stash apply`
  - On feature, clean: `git checkout default && git pull`
  - On feature, dirty: `git fetch && git rebase origin/default`
- `CommitPushPRAndSwitchDefault(item)` — commits, pushes, creates/updates PR via `gh pr create`
- `PruneBranchesAndWorktrees(path, defaultBranch)` — fetch prune + delete non-default branches + remove worktrees
- `GetLocalDirName` / `GetGHRepoName` — alias mapping (`.github` ↔ `github`, `careynas.net` ↔ `wiki.robot.house`)

**GitHub integration pattern:** All API calls go through the `gh` CLI binary (not the Go SDK). Commands are built via `exec.Command("gh", ...)` with JSON output parsing.

## Package `pkg/jobs` — CI runner & job monitoring

**Core types:**
- `RunnerItem` — a self-hosted GitHub Actions runner. Fields: ID, Name, Platform, Status, Tags, CurrentJob, OutputLogs, StepCount, LastHeartbeat
- `RunnerStatus` — IDLE, RUNNING, OFFLINE, MAINTENANCE
- `JobItem` — a workflow job. Fields: ID, Name, Repo, Branch, Event, PRNumber, PRTitle, PRURL, Status, RunnerName, Duration, Logs, RunID, GHJobID, IsRunHeader, StartedAt
- `JobStatus` — QUEUED, RUNNING, PASSED, FAILED, CANCELLED
- `GHRunnerLabel`, `GHRunnerInfo`, `GHWorkflowRun`, `GHWorkflowRunsResponse`, `GHJobInfo`, `GHJobsResponse`

**Key functions:**
- `FetchOrgRunners(org)` — queries `/orgs/{org}/actions/runners` and returns runners
- `MergeRunners(newRunners, existing, jobQueue)` — merges with existing data preserving log history, cross-references with job queue
- `FetchOrgJobQueue(org, repos)` — iterates over repos, queries `/repos/{org}/{repo}/actions/runs` and `/repos/{org}/{repo}/actions/runs/{id}/jobs`
- `FetchJobLogs(org, repo, runID, targetGHJobID, targetJobName, maxLines)` — fetches raw log text for a specific job; falls back through 4 matching strategies
- `FilterAndSortJobQueue(queue)` — removes passed/completed jobs; sorts running first, then groups by run ID, then matrix children
- `PollStep(runners, jobQueue)` — updates running job durations and sorts
- `mergeRunners(newRunners, existing, jobQueue)` — preserves logs, cross-references active jobs

## Package `pkg/tui` — Bubble Tea UI

**Model struct:** All app state — repos, runners, jobQueue, focus state, tab state, spinner, progress bar, viewport.

**Focus model:** Three-column left pane with focusable panels:
- `FocusRepos` (0) — repository list with status badges
- `FocusRunners` (1) — tag-filtered runner grid
- `FocusJobs` (2) — overall job queue with tree hierarchy

**Tab model:** Right-side detail view has 4 tabs: Logs, Branches, Issues, PRs.

**Message types:** `orgSyncedMsg`, `loadedRunnersMsg`, `loadedJobQueueMsg`, `loadedJobLogsMsg`, `loadedIssuesMsg`, `loadedPRsMsg`, `repoTickMsg`, `runnerJobTickMsg`, `syncFinishedMsg`

**Key behaviors:**
- `Init()` fires 5 commands in batch: spinner tick, org repo load, runners load, job queue load, and two periodic ticks
- Repo tick: every 5 minutes
- Runner/job tick: every 10 seconds
- Job queue polling fetches real GitHub API data; no mock simulation
- Job log fetching targets running jobs via 4-step fallback matching
- Toast notifications for job status transitions (QUEUED→RUNNING, etc.)
- `FocusedRunID` enables drill-down into a workflow run's matrix jobs (Enter to focus, Esc/Enter again to unfocus)

**Rendering:** `View()` builds a split layout — left column has 3 stacked bordered panes (repos, runners, jobs), right column has a viewport for detail content. Heavy use of Lip Gloss styles and OSC 8 hyperlinks.

**Helper functions in tui.go:**
- `Hyperlink(text, url)` — OSC 8 terminal hyperlinks
- `truncateString(str, maxLen)` — ellipsis truncation
- `parseJobHierarchy(fullName, repo)` — splits `"repo / workflow / job-name"` into (runName, jobName)
- `findJobForRunner(runner, queue)` — 4-step matching: exact name, case-insensitive, any assigned, any running
- `reconcileRunnerJobs(runners, queue, targetOrg)` — ensures running runners have corresponding queue entries
- `highlightLogLine(line)` — regex-based syntax highlighting for log output
- `getAvailableTags()` — collects unique runner tags for filter navigation

## Data flow

1. On start: `Init()` → parallel `loadOrgReposCmd`, `loadRunnersCmd`, `loadJobQueueCmd`
2. Org sync completes → `startParallelSyncCmd` (4 concurrent workers) → `SyncRepository` per repo
3. Periodic ticks refresh org repos (5min) and runners+jobs (10s)
4. Job log fetching is on-demand: triggered when selecting a running job; refreshed on each poll tick
5. Left pane selection drives right-pane viewport content via `updateViewport()`

## Testing

Tests in `pkg/jobs/jobs_test.go` test: default constructors, `FilterAndSortJobQueue`, `PollStep`, `MergeRunners` cross-referencing.

Tests in `pkg/tui/tui_test.go` test: view height constraints, header stickiness, viewport initial offset, `loadJobQueueCmd` fallback, runner rendering (no "Busy" word, hyperlink presence), table ordering, toast notifications, hyperlink OSC 8 syntax, focused run viewport, Enter/Esc focus toggle.
