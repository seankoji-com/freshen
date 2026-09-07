# freshen 🍃

**freshen** is an interactive TUI for managing sibling Git repositories. Connect an optional GitHub user or organization to discover repositories and monitor GitHub Actions.

> **Contributing?** See [CONTRIBUTING.md](CONTRIBUTING.md) for prerequisites, build/test commands, and the CI-critical `scripts/` directory.

---

## 🌟 Key Features

- **Concurrent Parallel Syncing**: Syncs 20+ repositories simultaneously in seconds using Go worker pools.
- **Dedicated screens**: Repositories, Actions and Runners, with searchable lists and details opened on demand.
- **Explicit repository sync**: Startup loads metadata. Sync or clone only when requested; archived repositories are highlighted.
- **Alias Mappings**: Hardcoded mapping support for custom repo folder names (e.g. `.github` ➔ `github`, `careynas.net` ➔ `wiki.robot.house`).
- **GitHub Actions monitoring**: Workflow runs, a separate job queue, completed-job and step counts, runner assignments, and a log tail for the selected job.
- **Interactive Controls**:
  - Re-sync / retry individual repositories.
  - Safely confirm deletion of archived repositories (`rm -rf`).
- **Non-Interactive Batch Mode**: Run with `-y` or `--non-interactive` for silent CLI scripting.

---

## 🚀 Installation & Distribution

### macOS & Linux (Homebrew / Linuxbrew)

Install via the official [seankoji-com Homebrew tap](https://github.com/seankoji-com/homebrew-tap):

```bash
brew install seankoji-com/tap/freshen
```

*(Or tap first: `brew tap seankoji-com/tap && brew install freshen`)*

### Windows (winget)

Install via the Windows Package Manager:

```powershell
# Install from repository manifest
winget install --manifest manifests/SeanKoji.Freshen.yaml

# Or once indexed in winget-pkgs
winget install SeanKoji.Freshen
```

### Prebuilt Binaries & Direct Download

Prebuilt release archives for **macOS (ARM64 / Intel)**, **Linux (ARM64 / x86_64)**, and **Windows (ARM64 / x64)** are published on [GitHub Releases](https://github.com/seankoji-com/freshen/releases).

### Build from Source

```bash
git clone https://github.com/seankoji-com/freshen.git
cd freshen
go build -o freshen main.go
```

---

## ⌨️ Usage & Keybindings

### Interactive TUI Mode
Launch the TUI interface:
```bash
./freshen
```

| Key | Action |
|---|---|
| `1` / `2` / `3` | Repositories / Actions / Runners |
| `Tab` / `Shift+Tab` | Next / previous screen |
| `↑↓` or `j/k` | Move within a list, or scroll open details |
| `Enter` / `→` | Open details; Actions opens run → jobs → steps and log tail |
| `Esc` / `←` | Back; clear a list filter when at the top level |
| `/` | Filter the current list; Enter applies, Esc clears |
| `Home` / `End`, `PgUp` / `PgDn` | First/last item or page through the current view |
| `Space` | Contextual action menu |
| `r` | Refresh data, without syncing repositories |
| `v` | Toggle Actions workflow runs / job queue |
| `f` | Filter runs: Active / Needs attention / Recent |
| `[` / `]` | Repository detail tabs: Logs / Branches / Issues / PRs |
| `o` / `y` | Open GitHub / copy link or runner ID (local path without an owner) |
| `s` / `a` | Sync selected repository / confirm sync all |
| `b` | Switch selected repository between original and default branch |
| `p` | Confirm commit all changes, push, create PR and switch to default |
| `X` | Confirm force-removing worktrees and deleting non-default branches |
| `d` | Confirm deleting an archived local clone |
| `?` | Scrollable help |
| `q` / `Ctrl+C` | Quit (`Ctrl+C` works inside search and confirmations) |

Repository actions run in the background with busy and result feedback. Destructive
confirmations retain the exact target across refreshes. Navigation never moves to
another screen merely because you reach the end of a list.

Actions retains all job results. Bars count completed jobs or steps, including
skipped/cancelled steps; they do not estimate remaining time. The recent view covers
30 runs per repository. Active runs are additionally queried when that window fills,
and job lists are paginated. Missing job details and partial repository failures are
labelled. Fleet access failures show observed assignments with unknown availability.
The organisation poll starts at 20 seconds and slows with workspace size to budget
API requests; the footer shows the interval. Selected running-job logs refresh with
the runner tick. GitHub may provide only step status while logs are unavailable.

### Command Line Options

```bash
# Specify a workspace and GitHub user or organization
freshen -dir ~/repos -owner octocat

# Non-interactive sync; archived repositories are never deleted by default
freshen -y

# Display version
freshen -v
```

### Logs

In TUI mode nothing is printed to the terminal — stray writes corrupt the alternate screen — so diagnostics go to a file under the user cache dir: `~/Library/Caches/freshen/freshen.log` on macOS, `~/.cache/freshen/freshen.log` on Linux. Set `FRESHEN_LOG_LEVEL` to `debug`, `info` (default), `warn`, or `error`.

```bash
tail -f ~/Library/Caches/freshen/freshen.log
```

Batch mode (`-y`) leaves logging on stderr.

### First run, configuration, and GitHub access

On first interactive launch, freshen asks for a sibling-repository workspace and an optional GitHub owner. Configuration is stored as `freshen/config.json` in the platform config directory; it contains no credentials. Flags override config, then `FRESHEN_OWNER` (or legacy `FRESHEN_ORG`) can supply an owner.

Workspace-only mode requires `git` and never calls GitHub. GitHub features require an authenticated [GitHub CLI](https://cli.github.com/) (`gh auth login`) or `GH_TOKEN`. Use a token with access to the target repositories; organization runner visibility may require organization-admin or runner permissions.

`d d` deletes an archived repository and `X` force-removes secondary worktrees and non-default branches. Review the selected workspace carefully. Batch deletion requires `--delete-archived`.

### Releases

Release binaries are published for macOS, Linux, and Windows with `checksums.txt`. Homebrew/Linuxbrew and winget manifests are generated from tagged releases; their upstream publication may require a maintainer submission. Verify checksums before installing binaries manually.

To publish a release, push a version tag such as `v1.0.0`. The release workflow
builds the supported OS and architecture combinations, uploads archives and
checksums to GitHub Releases, and updates the configured Homebrew tap when the
repository token has write access to that tap. Configure a dedicated
`HOMEBREW_TAP_GITHUB_TOKEN` repository secret with write access to
`seankoji-com/homebrew-tap`; the default `GITHUB_TOKEN` cannot write to that
separate repository. Winget publication is a separate
upstream submission: update the version, URLs, installer hashes, and license in
`manifests/SeanKoji.Freshen.yaml`, validate it with `winget validate`, then open
the corresponding pull request in `microsoft/winget-pkgs`.

---

## 🛠️ Built With

- **Language**: Go 1.26+
- **TUI Framework**: [Bubble Tea](https://github.com/charmbracelet/bubbletea)
- **Styling & Widgets**: [Lip Gloss](https://github.com/charmbracelet/lipgloss) & [Bubbles](https://github.com/charmbracelet/bubbles)
- **Integrations**: `git` & GitHub CLI (`gh`)

---

## 📝 Contributing

Interested in contributing? See [CONTRIBUTING.md](CONTRIBUTING.md) for details on setting up your development environment, build & test workflow, commit conventions, and debugging tips.
