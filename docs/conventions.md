# Conventions

## Language & build

- **Go 1.26+** (module: `github.com/seankoji-com/freshen`)
- Build: `go build -o freshen main.go`
- Run tests: `go test ./...`
- No build system beyond Go's native toolchain (no Makefile, no CI config in this repo)

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
- **View**: Builds full terminal output as string; Lip Gloss styles applied inline
- **Sub-models**: Spinner, ProgressBar, Viewport managed as fields on the main Model
- **Concurrency**: Long-running git operations run in goroutines within command closures

### Concurrency

- Repo sync uses a semaphore channel (`make(chan struct{}, concurrency)`) with `sync.WaitGroup`
- Current concurrency is 4
- TUI model uses `sync.Mutex` (`mu`) for thread safety on shared state

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
- **NerdFont icons**: Used extensively — `iconLeaf`, `iconGithub`, `iconBranch`, `iconPR`, `iconSuccess`, `iconError`, etc.

### Style/Lip Gloss

- Color palette defined as constants: Electric Purple primary, Bright Mint secondary, Emerald green, Amber yellow, Coral red, Bright blue, Muted slate
- Styles defined as package-level `var` blocks at the top of `tui.go`
- Column widths use explicit `.Width(N)` for table alignment
- All dynamic sizing uses `m.Width`/`m.Height` from `tea.WindowSizeMsg`

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
