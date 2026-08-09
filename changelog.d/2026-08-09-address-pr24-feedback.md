## Address PR #24 review feedback

### `pkg/jobs/jobs.go`
- **Timeouts**: All `gh api` calls now use `exec.CommandContext` with a 30-second timeout via `runGHCommand` helper
- **Stderr capture**: `runGHCommand` captures stderr into error messages, making API errors visible
- **Structured logging**: `slog.Error/Debug` replaces silent `continue` error swallowing
- **Numeric ID sorting**: `jobSortKey` uses `parseNumericID` for proper numeric ordering (#9 < #123)
- **Deduplication**: Extracted `buildJobItemFromJob`, `buildJobItemFromRun`, `formatDuration`, `formatQueuedAgo`, `extractPRInfo`, `parseDuration`, `jobStatusFromGH` — ~60 lines of duplication eliminated
- **Sort consistency**: `PollStep` now uses the same `jobSortKey` comparator as `FilterAndSortJobQueue`
- **Dead code removed**: Replaced `"COMPLETED"`/`"SUCCESS"` string comparisons with `JobCancelled` constant
- **Job name matching**: `FetchJobLogs` tries exact name match before falling back to `strings.Contains`
- **API contract**: `FetchOrgJobQueue` now returns actual errors (with per-repo details) when fetch failures occur
- **API contract**: `FetchOrgRunners` simplified to `(org string)` — returns `nil, err` on failure; `MergeRunners` exported for caller-side merging

### `pkg/tui/tui.go`
- **Main thread blocking** (CRITICAL): Moved `git.FetchOrgRepos` call inside `loadJobQueueCmd`'s closure — was executing synchronously in command constructor
- **Data race**: Documented race between `PollStep` mutations and background command reads; runner fetch now uses fresh API results rather than passing stale slices
- **Bounds validation**: `SelectedJobIndex` and `SelectedRunnerIndex` clamped after every queue/runners update in `processJobQueueUpdate`
- **Misleading job association**: Removed step 4 from `findJobForRunner` (was returning ANY running job, not the runner's job)
- **Synthetic job Repo field**: Changed from `targetOrg` (org name) to empty string — URL construction already guarded by `RunID==0`
- **Toast priority**: `setToast` with priority levels — error toasts (priority 2) survive info toasts (priority 1)
- **Regex compilation**: Moved all 4 regex patterns to package-level `regexp.MustCompile` variables
- **Error toasts**: `loadedRunnersMsg` and `loadedJobQueueMsg` show error toasts when fetches fail
- **Log fetch errors**: `loadedJobLogsMsg` shows error message in job logs on fetch failure
- **Mouse click regions**: Now uses box-height calculations from `View()` layout instead of content lengths
- **Footer**: Updated with all active keybindings (r/b/p/d/X/j/k/scroll)

### `.github/workflows/ci.yml`
- Added `permissions: contents: read`
- Added `timeout-minutes: 10`
- Added `concurrency` group with `cancel-in-progress: true`

### `pkg/git/git.go`
- Log messages now reflect actual commands: `git pull --no-rebase origin <branch>` instead of generic `git pull`
- Dirty default branch path now checks and logs `git pull` errors instead of silently swallowing
