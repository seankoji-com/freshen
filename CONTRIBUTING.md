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

## Scripts

The `scripts/` directory contains utilities used by the CI/CD pipeline. They
are referenced by CI but are not part of the Go build — a contributor can
break the persona review pipeline by touching them unknowingly, so treat them
as production code.

### `hunk-validate.py`

**Purpose**: Validates that review comments fall within PR diff hunks.

Reads comment JSON from stdin and hunks JSON from the first positional
argument; classifies each comment as in-hunk (line range overlaps a diff
hunk, clamped to it) or out-of-hunk (demoted to the review summary).

**Usage**:
```bash
cat comments.json | python3 scripts/hunk-validate.py hunks.json
```

**Output**: JSON with `in_hunk` and `out_of_hunk` arrays.

**CI Consumer**: Called by `scripts/persona-review-post.sh` to validate OCR (Open Code Review) findings before posting to GitHub.

### `persona-review-post.sh`

**Purpose**: Posts Open Code Review (OCR) findings as a specific persona's
GitHub App identity, with marker-based dedup to prevent reposting on
subsequent pushes.

Reads OCR results (default `/tmp/ocr-result.json`), deduplicates against
existing PR comments (exact match on path+line for single-line, IoU > 0.95
for multi-line), validates positions against diff hunks via
`hunk-validate.py`, and posts the review under the persona's own identity.
Replaces alibaba/open-code-review's built-in posting, which cannot use
per-persona App tokens.

**Usage**:
```bash
export PERSONA_ID="grumpy-engineer"
export OWNER_REPO="seankoji-com/freshen"
export PR_NUMBER="42"
export HEAD_SHA="abc123..."
export PERSONA_TOKEN="ghu_..."
./scripts/persona-review-post.sh
```

**Environment Variables**:
- `PERSONA_ID` — persona identifier (e.g., "sre", "grumpy-engineer")
- `OWNER_REPO` — owner/repo (e.g., "seankoji-com/freshen")
- `PR_NUMBER` — pull request number
- `HEAD_SHA` — PR head commit SHA
- `PERSONA_TOKEN` — GitHub App installation token
- `OCR_RESULT_PATH` — path to OCR JSON output (default: `/tmp/ocr-result.json`)

**CI Consumer**: Not invoked by any workflow in this repo. Its caller is the
org-level `seankoji-com/.github` reusable workflow (see
[docs/persona-review-setup.md](docs/persona-review-setup.md)).

---

## Questions?

If you run into issues or have questions, feel free to open an issue in the repository or reach out to the maintainers.
