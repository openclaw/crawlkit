.PHONY: test vet tidy check release-artifacts

test:
	GOWORK=off go test ./...

vet:
	GOWORK=off go vet ./...

tidy:
	GOWORK=off go mod tidy

check: tidy vet test
	git diff --exit-code -- go.mod go.sum

release-artifacts:
	@test -n "$(VERSION)" || (echo "usage: make release-artifacts VERSION=vX.Y.Z" >&2; exit 2)
	@./scripts/package-crawlctl-release.sh "$(VERSION)"
