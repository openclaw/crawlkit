BINARY := crawlctl

.DEFAULT_GOAL := help

.PHONY: help build test test-race vet tidy tidy-check fmt lint test-release check snapshot release verify-release release-artifacts clean

help:
	@printf '%s\n' \
		'Available targets:' \
		'  help              Print available targets (default).' \
		'  build             Build the CLI into bin/$(BINARY).' \
		'  test              Run the full Go test suite.' \
		'  fmt               Check Go formatting.' \
		'  lint              Run vet, dead-code, and vulnerability checks.' \
		'  check             Run every local gate enforced by CI.' \
		'  snapshot          Build a credential-free local snapshot.' \
		'  release           Refuse local publishing and print the unified workflow command.' \
		'  verify-release    Verify downloaded fleet release artifacts (VERSION=X.Y.Z).' \
		'  test-race         Run the Go test suite with the race detector.' \
		'  test-release      Test the release scripts.' \
		'  vet               Run go vet (compatibility target).' \
		'  tidy              Apply go.mod and go.sum tidying.' \
		'  tidy-check        Verify go.mod and go.sum are tidy.' \
		'  release-artifacts Alias for release.' \
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

test-release:
	bash scripts/test-crawlctl-release.sh

check: tidy-check fmt lint test test-race test-release

snapshot: build

release:
	@./scripts/package-crawlctl-release.sh "$(VERSION)"

verify-release:
	@test -n "$(VERSION)" || (echo "usage: make verify-release VERSION=X.Y.Z" >&2; exit 2)
	@set -e; \
		version="$(VERSION)"; \
		version="$${version#v}"; \
		release_commit="$$(./scripts/verify-crawlctl-release-provenance.sh "v$$version")"; \
		./scripts/verify-crawlctl-release.sh "$$version" "$$release_commit" \
			"dist/crawlkit_$${version}_darwin_arm64.tar.gz" \
			"dist/crawlkit_$${version}_darwin_amd64.tar.gz" \
			"dist/crawlkit_$${version}_linux_arm64.tar.gz" \
			"dist/crawlkit_$${version}_linux_amd64.tar.gz"

release-artifacts: release

clean:
	rm -rf bin
