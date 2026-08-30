.PHONY: build test vet lint lint-shadow install coverage clean

build:
	go build -ldflags "-X main.Version=$$(git describe --tags --always 2>/dev/null || echo 1.0.0)" -o freshen .

test:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...
	golangci-lint fmt ./...
	git diff --exit-code

lint-shadow:
	golangci-lint run -c .golangci-shadow.yml ./...

install:
	go install -ldflags "-X main.Version=$$(git describe --tags --always 2>/dev/null || echo 1.0.0)"

coverage:
	go test ./... -cover

clean:
	rm -f freshen coverage.out
