# pupptyeer - build, install, test. See README.md and CLAUDE.md.

BIN   := bin
# go install honours GOBIN; mirror its resolution for the mcp binary,
# which needs an explicit -o (its main package dir would install as "mcp").
GOBIN := $(or $(shell go env GOBIN),$(shell go env GOPATH)/bin)

.PHONY: build install test conformance clean

build:
	go build -o $(BIN)/pupptyeer ./cmd/pupptyeer
	go build -C mcp -o ../$(BIN)/pupptyeer-mcp .

install:
	go install ./cmd/pupptyeer
	go build -C mcp -o $(GOBIN)/pupptyeer-mcp .

test:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt: files need formatting"; exit 1; }
	go vet ./...
	go -C mcp vet ./...
	go test ./...

conformance: build
	bash conformance/run.sh

clean:
	rm -rf $(BIN)
