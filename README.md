# freshen 🍃

<p align="center">
  <img width="1728" height="1045" alt="screenshot of freshen TUI" src="https://github.com/user-attachments/assets/40e6c704-3f34-4d2f-a4ed-b898f799a529" />
</p>

**freshen** is an interactive TUI for managing sibling Git repositories. Connect an optional GitHub user or organization to discover repositories and monitor GitHub Actions.

> **Contributing?** See [CONTRIBUTING.md](CONTRIBUTING.md) for prerequisites, build/test commands, and the CI-critical `scripts/` directory.

---

## 🌟 Key Features

- **Concurrent Parallel Syncing**: Syncs 20+ repositories simultaneously in seconds using Go worker pools.
- **Split-Pane TUI Dashboard**: Real-time status indicators on the left, live git/gh execution logs and PR links on the right.
- **Optional GitHub owner sync**: Automatically clones missing active repositories and highlights archived repositories.
- **Alias Mappings**: Hardcoded mapping support for custom repo folder names (e.g. `.github` ➔ `github`, `careynas.net` ➔ `wiki.robot.house`).
- **GitHub Actions Monitoring**: Live-polled runner and job-queue panels showing self-hosted runner status and in-flight/queued CI jobs, with per-job log streaming.
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

**Global**

| Key | Action |
|---|---|
| <kbd>w</kbd> | Cycle panel focus: Repos → Runners → Jobs |
| <kbd>1</kbd> / <kbd>2</kbd> / <kbd>3</kbd> | Jump directly to the Repos / Runners / Jobs panel |
| <kbd>Tab</kbd> / <kbd>Shift+Tab</kbd> | Cycle forward/back (repo tabs, runner tag filter, or panel focus, depending on context) |
| <kbd>↑</kbd> / <kbd>↓</kbd> | Move selection within the focused panel |
| <kbd>j</kbd> / <kbd>k</kbd> | Scroll the detail viewport (right pane) down/up |
| <kbd>a</kbd> / <kbd>s</kbd> | Sync All — start a parallel sync across every loaded non-archived repository |
| <kbd>c</kbd> / <kbd>y</kbd> | Copy the selected item's path/PR URL/ID to clipboard |
| <kbd>q</kbd> or <kbd>Ctrl+C</kbd> | Quit application |

**Repos panel**

| Key | Action |
|---|---|
| <kbd>←</kbd> / <kbd>→</kbd> / <kbd>h</kbd> / <kbd>l</kbd> | Cycle detail tabs (Logs / Branches / Issues / PRs) |
| <kbd>4</kbd> | Jump straight to the PRs tab |
| <kbd>r</kbd> | Re-sync / retry the selected repository |
| <kbd>b</kbd> | Switch between the original and default branch |
| <kbd>p</kbd> | Commit, push, open a PR, and switch to the default branch |
| <kbd>X</kbd> | Prune remote refs, worktrees, and merged local branches |
| <kbd>d</kbd> <kbd>d</kbd> | Delete the selected archived repository from disk (press twice: first press arms a confirmation, second press while still selected deletes) |

**Runners panel**

| Key | Action |
|---|---|
| <kbd>←</kbd> / <kbd>→</kbd> / <kbd>h</kbd> / <kbd>l</kbd> | Cycle the runner tag filter |

**Jobs panel**

| Key | Action |
|---|---|
| <kbd>Enter</kbd> | Focus/unfocus the selected job's live run logs |
| <kbd>Esc</kbd> | Unfocus the currently focused run |

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
