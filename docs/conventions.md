# Conventions

## Language & build

- **Go 1.26+** (module: `github.com/seankoji-com/freshen`)
- Build: `go build -o freshen .`
- Run tests: `go test -race ./...`
- Makefile with build/test/vet/install/coverage/clean targets (see Makefile). CI/CD runs through the public workflows in `.github/workflows/`.

## Project organization

- Single module with three internal packages under `pkg/`
- Entry point in `main.go` at repo root
- No third-party Go SDK for GitHub — all GitHub interaction via `gh` CLI binary (`os/exec`)
- All types for a package live in a single file (e.g., `jobs.go` has all types for the `jobs` package)

## Code patterns

### GitHub CLI invocation

```go
cmd := exec.Command("gh", "api", "/orgs/ORG/actions/runners?per_page=100")
var out bytes.Buffer
cmd.Stdout = &out
if err := cmd.Run(); err != nil { ... }
json.Unmarshal(out.Bytes(), &resp)
```

Timeouts use `context.WithTimeout` + `exec.CommandContext` (6s for issues/PRs).

### Bubble Tea patterns

- **Commands**: Return `tea.Cmd` from closures, not named functions. Example: `return func() tea.Msg { ... }`
- **Messages**: Custom struct types (e.g., `loadedRunnersMsg`, `orgSyncedMsg`) carrying data+error
- **Update**: Single `switch msg.(type)` block handling all message types and key events
- **View**: Builds full terminal output as string from `chrome.go` rows and screen bodies; styles come from `theme.go`
- **Sub-models**: Spinner, ProgressBar, Viewport managed as fields on the main Model
- **Concurrency**: Long-running git operations run in goroutines within command closures

### Concurrency

- Repo sync uses a semaphore channel (`make(chan struct{}, concurrency)`) with `sync.WaitGroup`
- Current concurrency is 4
- TUI model changes only in `Update`; workers use private snapshots and return messages

### Error handling

- Errors are generally not fatal; logged via `item.Logs` or returned in message structs
- Git operations silently continue on error (logged, not failing the app)
- API failures (runner fetch, job queue fetch) return existing stale data + error

### Naming

- **Constants**: PascalCase for enums (`StatusPending`, `JobRunning`, `FocusRepos`)
- **Variables**: camelCase (`iconLeaf`, `colorPrimary`, `selectedRowStyle`)
- **Functions**: PascalCase for exported, camelCase for unexported
- **Types**: PascalCase (`RepoItem`, `JobItem`, `RunnerItem`)
- **Files**: lowercase, single word or hyphenated (`git.go`, `jobs.go`, `tui.go`)
- **Status glyphs**: The TUI uses one-cell Unicode glyphs from `statusGlyphs` in `pkg/tui/theme.go`, always followed by a word. Nerd Font glyphs remain only in sync log lines produced by `pkg/git`.

### Style/Lip Gloss

- The palette and every style live in the `theme` value `th` in `pkg/tui/theme.go`; render code reads `th.<style>` instead of calling `lipgloss.NewStyle()` with colours
- Statuses map to a `statusKind` (`jobKind`, `runnerKind`, `repoKind`); render them with `th.status(kind, label)` so colour, glyph and word stay together
- `NO_COLOR` builds a colourless theme; the selection and active tab use reverse video instead
- Column widths use explicit `.Width(N)` for table alignment
- All dynamic sizing uses `m.Width`/`m.Height` from `tea.WindowSizeMsg`; measure styled text with `ansi.StringWidth`

### Keys

- Add a binding to `keyMap` in `pkg/tui/keymap.go`, enable it in `Model.keys()` for the contexts where it acts, and match it with `key.Matches` in the handler. The footer and `?` overlay then list it automatically
- Never reuse a global key (`0`–`4`, Tab, Shift+Tab, Esc/←/h, `/`, `r`, `?`, `q`, Ctrl+C) for a screen binding; `TestScreenKeysDoNotShadowGlobalKeys` enforces this

### Logging

- No structured logging library. Log output is string slices (`[]string`) stored on model items
- Log entries use timestamp format `[HH:MM:SS]` with NerdFont glyphs as prefixes
- Log lines are syntax-highlighted via regex in `highlightLogLine()`

### Test conventions

- Tests use table-driven patterns sparingly; most are focused single-scenario tests
- Tests create a `NewModel("/tmp/test", "test-org")` and set fields directly
- View assertions use `strings.Contains` on rendered output
- No mocking of `gh` CLI — tests that need API data use empty/default constructors
- Test files: `*_test.go` alongside the source file they test
