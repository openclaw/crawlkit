.PHONY: test vet tidy check

test:
	GOWORK=off go test ./...

vet:
	GOWORK=off go vet ./...

tidy:
	GOWORK=off go mod tidy

check: tidy vet test
	git diff --exit-code -- go.mod go.sum
