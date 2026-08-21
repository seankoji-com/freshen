# Contributing to freshen

Thank you for your interest in contributing to freshen! This guide covers the development workflow, build process, and debugging tips.
(AGENTS.md is the agent-shaped handbook; this file is for humans.)

---

## Prerequisites

Before you start, ensure you have:

- **Go 1.26 or later** — [install from golang.org](https://golang.org/dl)
- **git** — for version control
- **GitHub CLI (`gh`)** — [install from github.com/cli/cli](https://cli.github.com)
  - Authenticate with your GitHub account: `gh auth login`

Verify prerequisites:
```bash
go version
git --version
gh auth status
```

---

## Build & Test

### Using Makefile Targets

The project includes a Makefile with convenient build and test targets:

```bash
# Build the binary
make build

# Run tests
make test

# Run Go's static analysis
make vet

# Run tests with coverage report
make coverage

# Clean build artifacts
make clean

# Install system-wide
make install
```

### Using Go Commands Directly

If you prefer to run commands directly:

```bash
# Build
go build -o freshen .

# Test
go test -race ./...

# Static analysis
go vet ./...

# Test with coverage
go test ./... -cover
```

---

## Development Workflow

### Branch Naming

Use conventional branch names that reflect the type of work:

- **Features**: `feat/short-description`
- **Bug fixes**: `fix/short-description`
- **Documentation**: `docs/short-description`
- **Refactoring**: `refactor/short-description`

Example:
```bash
git checkout -b feat/add-filtering-to-repos-panel
```

### Commit Message Style

Follow conventional commit format. Refer to the project's git history for style examples:

```bash
git log --oneline
```

Keep commits focused and atomic. Use present tense in the summary:

```
feat: add filtering to repos panel
fix: correct panic in concurrent sync
docs: update README installation instructions
```

### Opening a Pull Request

1. Push your branch to the remote repository.
2. Open a PR using the GitHub CLI or web interface.
3. Add a clear description of what the change does and why.
4. Link any related issues: `Closes #123`
5. Ensure CI checks pass (tests, vet, formatting).

Example:
```bash
git push -u origin feat/add-filtering-to-repos-panel
gh pr create --title "feat: add filtering to repos panel" \
  --body "Adds ability to filter repositories by name or status"
```

---

## Debugging Tips

### GitHub CLI Authentication Errors

If you see `freshen requires an authenticated GitHub CLI. Run: gh auth login.`:

```bash
# Check your current authentication status
gh auth status

# Re-authenticate
gh auth login

# Verify with a test API call
gh api /user
```

### Common Issues

- **`Permission denied` on `gh` commands**: Ensure the token has appropriate permissions (repo, read:org). Re-run `gh auth login` and select the right scopes.
- **Tests fail due to missing dependencies**: Run `go mod tidy` to ensure all dependencies are available.

---

---

## Questions?

If you run into issues or have questions, feel free to open an issue in the repository or reach out to the maintainers.
