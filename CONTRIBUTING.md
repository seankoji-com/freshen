# Contributing to freshen

Thank you for your interest in contributing to freshen! This guide covers the development workflow, build process, and debugging tips.

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
go build -o freshen main.go

# Test
go test ./...

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

The `scripts/` directory contains utilities used in the CI/CD pipeline:

### `hunk-validate.py`

**Purpose**: Validates that review comments fall within PR diff hunks.

This script reads review comments from stdin and compares them against diff hunks from a JSON file. It classifies comments as either in-hunk (within the diff) or out-of-hunk (outside the modified lines). Out-of-hunk comments are typically demoted to the review summary section.

**Usage**:
```bash
cat comments.json | python3 scripts/hunk-validate.py hunks.json
```

**Output**: JSON with `in_hunk` and `out_of_hunk` arrays.

**CI Consumer**: Called by `scripts/persona-review-post.sh` to validate OCR (Open Code Review) findings before posting to GitHub.

### `persona-review-post.sh`

**Purpose**: Posts Open Code Review (OCR) findings as a persona-based GitHub App.

This script reads OCR results, deduplicates findings against existing PR comments, validates comment positions against diff hunks (using `hunk-validate.py`), and posts the review under a specific persona's GitHub App identity. It supports batched inline comments and summary-only reviews, with fail-closed error handling for API failures.

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

**CI Consumer**: Called by reusable workflow in the `.github/workflows/` and the org-wide persona review pipeline to post persona-specific code reviews on pull requests.

---

## Questions?

If you run into issues or have questions, feel free to open an issue in the repository or reach out to the maintainers.
