.PHONY: build test vet install coverage clean

build:
	go build -ldflags "-X main.Version=$$(git describe --tags --always 2>/dev/null || echo 1.0.0)" -o freshen .

test:
	go test -race ./...

vet:
	go vet ./...

install:
	go install -ldflags "-X main.Version=$$(git describe --tags --always 2>/dev/null || echo 1.0.0)"

coverage:
	go test ./... -cover

clean:
	rm -f freshen coverage.out
