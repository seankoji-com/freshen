# freshen 🍃

**freshen** is a high-performance, interactive TUI (Terminal User Interface) application built with [Go](https://go.dev) and [Bubble Tea](https://github.com/charmbracelet/bubbletea) to manage, synchronize, stash, pull, and clean up multi-repository setups across GitHub organizations.

> **Contributing?** See [CONTRIBUTING.md](CONTRIBUTING.md) for prerequisites, build/test commands, and the CI-critical `scripts/` directory.

---

## 🌟 Key Features

- **Concurrent Parallel Syncing**: Syncs 20+ repositories simultaneously in seconds using Go worker pools.
- **Split-Pane TUI Dashboard**: Real-time status indicators on the left, live git/gh execution logs and PR links on the right.
- **GitHub Organization Sync (`seankoji-com`)**: Automatically clones missing active org repositories and highlights archived repositories.
- **Alias Mappings**: Hardcoded mapping support for custom repo folder names (e.g. `.github` ➔ `github`, `careynas.net` ➔ `wiki.robot.house`).
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

| Key | Action |
|---|---|
| <kbd>↑</kbd> / <kbd>↓</kbd> or <kbd>k</kbd> / <kbd>j</kbd> | Navigate repository list |
| <kbd>r</kbd> | Re-sync / Retry selected repository |
| <kbd>d</kbd> | Confirm delete selected archived repository |
| <kbd>a</kbd> | Trigger parallel sync for all active repositories |
| <kbd>q</kbd> or <kbd>Ctrl+C</kbd> | Quit application |

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
