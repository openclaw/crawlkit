BINARY := crawlctl

.DEFAULT_GOAL := help

.PHONY: help build test test-race vet tidy tidy-check fmt lint check clean

help:
	@printf '%s\n' \
		'Available targets:' \
		'  help              Print available targets (default).' \
		'  build             Build the CLI into bin/$(BINARY).' \
		'  test              Run the full Go test suite.' \
		'  fmt               Check Go formatting.' \
		'  lint              Run vet, dead-code, and vulnerability checks.' \
		'  check             Run every local gate enforced by CI.' \
		'  test-race         Run the Go test suite with the race detector.' \
		'  vet               Run go vet (compatibility target).' \
		'  tidy              Apply go.mod and go.sum tidying.' \
		'  tidy-check        Verify go.mod and go.sum are tidy.' \
		'  clean             Remove local build output.'

build:
	mkdir -p bin
	GOWORK=off go build -o bin/$(BINARY) ./cmd/crawlctl

test:
	GOWORK=off go test ./...

test-race:
	GOWORK=off go test -race ./...

vet:
	GOWORK=off go vet ./...

tidy:
	GOWORK=off go mod tidy

tidy-check: tidy
	git diff --exit-code -- go.mod go.sum

fmt:
	@set -e; \
	unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then echo "$$unformatted"; exit 1; fi

lint: vet
	@set -e; \
	output_file="$$(mktemp)"; \
	trap 'rm -f "$$output_file"' 0; \
	GOWORK=off go run golang.org/x/tools/cmd/deadcode@v0.46.0 -test ./... > "$$output_file"; \
	if [ -s "$$output_file" ]; then cat "$$output_file"; exit 1; fi
	GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@v1.4.0 ./...

check: tidy-check fmt lint test test-race

clean:
	rm -rf bin
