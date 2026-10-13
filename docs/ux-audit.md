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

## Navigation, visual design and feedback pass

Reviewed on 13 October 2026 against `f324ced`.

| Finding | Change |
|---|---|
| Key handling was a string switch; `?` showed a hand-written page that could drift from it | One `keyMap`; `keys()` enables bindings per context; handler, footer and `?` overlay all read it |
| `q`, `0`–`4` and Tab did nothing over the help page; `/` did nothing on Overview or in details | Global keys act everywhere outside the filter, menu and confirmation; `/` filters the nearest list |
| Esc closed an open run before clearing that run's job filter | Esc leaves the innermost layer: job, detail, filter, run, then the message |
| The context row mixed location and statistics; runner detail read "Runners / details" | A breadcrumb (`Actions › alpha / CI #42 › test`) followed by screen statistics |
| Colours and styles were built ad hoc in each render function; job states were colour plus word, repository states a Nerd Font glyph only, runner states a word only | `theme.go` owns the palette; every status is glyph, word and colour (`✓ ✗ ↻ ◌ ⊘ ? !`) |
| Bubbles' progress bar ignored `NO_COLOR` | The bar follows Lip Gloss's environment-aware profile; `NO_COLOR` builds a colourless theme with reverse-video selection |
| Toasts never expired and could stay until Esc | Info toasts expire after 6 s, errors after 15 s; a sequence number stops a stale expiry clearing a newer toast |
| Footer hints were truncated from the right, so `?` was the first hint lost on narrow terminals | Hints are fitted with the final hint (`? keys` or the modal escape) always shown |
| Data age was shown only for Actions | The header shows the current screen's data age and refresh interval |
| Runners were fetched every 10 s on every screen | The runner poll pauses on screens that do not show runners and refreshes on return |
| Each running-job log refresh listed the run's jobs before downloading the log | A known job ID downloads the log directly: one request instead of two or more |
| Runs had no timing; failed jobs gave no reason | Runs show running/waiting/took times; failed jobs name the first failing step; tabs count failures and running work |
| Empty Runners and Repositories screens said "Nothing to show"; without an owner, Actions and Runners looked empty rather than unconfigured | Empty states name what is empty and the next key to press |

## Verification

`pkg/tui/ux_test.go` renders every screen, drill-down and layer at 80 × 24,
200 × 50, 30 × 10 and other sizes and checks the frame height, row widths,
header and final footer hint. It also checks that screen keys never shadow global
keys, that the overlay lists exactly the enabled bindings, that global keys act
from every drill-down, that confirmations ignore every key but Enter and Esc,
toast expiry and stale expiry, runner-poll call counts, `NO_COLOR` output and
status glyph coverage. `pkg/jobs` tests count `gh` requests for log refreshes.

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

Run the fixture with `go test -c -o "$TMPDIR/freshen-ui.test" ./pkg/tui`, then
`FRESHEN_DEMO=1 "$TMPDIR/freshen-ui.test" -test.run TestTerminalDemo`. It drops external
commands and uses seeded data; it cannot exercise real API or git operations.
