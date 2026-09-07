# TUI audit and redesign

Audited on 7 September 2026 against `f779f11`.

| Finding | Implemented change |
|---|---|
| Up/down crosses panel boundaries, left/right changes meaning, keys also scroll the viewport | One overview and four dedicated screens; one owner for input; Enter opens and Esc returns |
| Repositories, runners, queue and details compete for half-screen areas | Full-height lists and separate detail screens; search and page navigation |
| PR/branch headers, run headers and jobs share one cursor | Run identity is explicit; Actions opens runs, then jobs, then steps/logs; `v` opens the cross-repository job queue |
| Passed/skipped jobs are discarded; elapsed run averages imply job progress | Retain terminal results; bars count observed completed jobs/steps; elapsed duration is separate |
| A disappearing job produces a success notification | Only a confirmed status transition reports success |
| First-page job lists hide large matrices; recent runs can hide an old queued run | Paginate jobs and query active states beyond a full recent window |
| Failed repositories disappear without warning | Partial snapshots and unavailable run details stay visible |
| Navigation can retarget actions during refresh | Stable selection keys and captured confirmation targets |
| Branch/prune/delete/copy calls can block input | Background commands with busy/result messages and mutation serialization |
| Starting the TUI runs sync | Startup reads metadata; sync is explicit |
| Runner observations imply live fleet capacity | Unknown availability is labelled when fleet permissions are missing |
| Missing details can read as empty successful results | Retry messages, loading indicators and explicit unknown states |

The interface uses the existing Charm stack: Bubble Tea, Lip Gloss, and Bubbles
text input, help, viewport, spinner and progress components. It avoids a framework
migration while replacing the old panel layout and navigation code.

## Verification

Regression tests cover run/job drill-down, queue counts, step progress, filtering,
scrolled mouse selection, frame dimensions, stable selection, confirmation targets,
late responses and background-action guards. Existing repository sync/concurrency,
API and configuration tests remain. API fixtures cover result mapping, pagination,
partial failures and exact log identity. A disposable terminal fixture exercises
screens and confirmations without contacting GitHub or mutating real repositories.

## Limits

Recent history is bounded to 30 runs per repository. The active-status endpoints
have GitHub's own search limits. Queue order is an observation order, not a promised
GitHub scheduling order. GitHub does not expose a universal queue position or reliable
percentage of wall time remaining; neither is fabricated. Logs are a 200-line tail,
with an explicit step-status fallback when GitHub withholds the download. Workflow
configuration editing and Actions cancellation/rerun are outside this change.

Run the fixture with `go test -c -o /tmp/freshen-ui.test ./pkg/tui`, then
`FRESHEN_DEMO=1 /tmp/freshen-ui.test -test.run TestTerminalDemo`. It drops external
commands and uses seeded data; it cannot exercise real API or git operations.
