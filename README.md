# freshen 🍃

**freshen** is a high-performance, interactive TUI (Terminal User Interface) application built with [Go](https://go.dev) and [Bubble Tea](https://github.com/charmbracelet/bubbletea) to manage, synchronize, stash, pull, and clean up multi-repository setups across GitHub organizations.

> **Contributing?** See [CONTRIBUTING.md](CONTRIBUTING.md) for prerequisites, build/test commands, and the CI-critical `scripts/` directory.

---

## 🌟 Key Features

- **Concurrent Parallel Syncing**: Syncs 20+ repositories simultaneously in seconds using Go worker pools.
- **Split-Pane TUI Dashboard**: Real-time status indicators on the left, live git/gh execution logs and PR links on the right.
- **GitHub Organization Sync (`seankoji-com`)**: Automatically clones missing active org repositories and highlights archived repositories.
- **Alias Mappings**: Hardcoded mapping support for custom repo folder names (e.g. `.github` ➔ `github`, `careynas.net` ➔ `wiki.robot.house`).
- **GitHub Actions Monitoring**: Live-polled runner and job-queue panels showing self-hosted runner status and in-flight/queued CI jobs, with per-job log streaming.
- **Interactive Controls**:
  - Re-sync / retry individual repositories.
  - Safely confirm deletion of archived repositories (`rm -rf`).
- **Non-Interactive Batch Mode**: Run with `-y` or `--non-interactive` for silent CLI scripting.

---

## 🚀 Installation & Building

```bash
cd ~/repos/freshen

# Build the binary
go build -o freshen main.go

# (Optional) Install system-wide
go install .
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
# Specify custom target directory and GitHub org
./freshen -dir ~/repos -org seankoji-com

# Non-interactive CLI batch mode
./freshen -y

# Display version
./freshen -v
```

### Logs

In TUI mode nothing is printed to the terminal — stray writes corrupt the alternate screen — so diagnostics go to a file under the user cache dir: `~/Library/Caches/freshen/freshen.log` on macOS, `~/.cache/freshen/freshen.log` on Linux. Set `FRESHEN_LOG_LEVEL` to `debug`, `info` (default), `warn`, or `error`.

```bash
tail -f ~/Library/Caches/freshen/freshen.log
```

Batch mode (`-y`) leaves logging on stderr.

---

## 🛠️ Built With

- **Language**: Go 1.26+
- **TUI Framework**: [Bubble Tea](https://github.com/charmbracelet/bubbletea)
- **Styling & Widgets**: [Lip Gloss](https://github.com/charmbracelet/lipgloss) & [Bubbles](https://github.com/charmbracelet/bubbles)
- **Integrations**: `git` & GitHub CLI (`gh`)

---

## 📝 Contributing

Interested in contributing? See [CONTRIBUTING.md](CONTRIBUTING.md) for details on setting up your development environment, build & test workflow, commit conventions, and debugging tips.
