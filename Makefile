.PHONY: build test vet lint lint-shadow install coverage clean

# Pinned via `go run` (not PATH or a curl|sh installer) so `make lint`, CI, and
# `make lint-shadow` all resolve the exact same golangci-lint binary through
# the Go module proxy's checksum-verified fetch — no local install step, no
# version drift between a developer's machine and CI.
GOLANGCI_LINT_VERSION := v2.13.2
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

build:
	go build -ldflags "-X main.Version=$$(git describe --tags --always 2>/dev/null || echo 1.0.0)" -o freshen .

test:
	go test -race ./...

vet:
	go vet ./...

# Report-only: `fmt --diff` prints the exact formatting diff and exits
# non-zero without touching any file, so this target never mutates the
# working tree (a target named `lint` doing an in-place rewrite surprised a
# reviewer, and rightly so).
lint:
	$(GOLANGCI_LINT) run ./...
	$(GOLANGCI_LINT) fmt --diff ./... || { echo "^ golangci-lint fmt would make the changes shown above — run '$(GOLANGCI_LINT) fmt ./...' locally and commit the result" >&2; exit 1; }

lint-shadow:
	$(GOLANGCI_LINT) run -c .golangci-shadow.yml ./...

install:
	go install -ldflags "-X main.Version=$$(git describe --tags --always 2>/dev/null || echo 1.0.0)"

coverage:
	go test ./... -cover

clean:
	rm -f freshen coverage.out
