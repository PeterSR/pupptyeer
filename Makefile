# pupptyeer - build, install, test. See README.md and CLAUDE.md.

BIN   := bin
# go install honours GOBIN; mirror its resolution for the mcp binary,
# which needs an explicit -o (its main package dir would install as "mcp").
GOBIN := $(or $(shell go env GOBIN),$(shell go env GOPATH)/bin)

.PHONY: build install test conformance clean docker

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
	go test -race ./...

conformance: build
	bash conformance/run.sh

clean:
	rm -rf $(BIN)

# Local image, same Dockerfile as the release. Builds the linux binaries for
# the host arch (override DOCKER_ARCH) into a context laid out like goreleaser's
# dockers_v2 context (<os>/<arch>/<binary>, docker/pupptyeer-entrypoint.sh),
# then tags only pupptyeer:local (never the ghcr name: a local tag would
# shadow the published image for any FROM or COPY --from naming it).
DOCKER_ARCH ?= $(shell go env GOARCH)
DOCKER_CTX  := $(BIN)/docker-ctx
DOCKER_VER  := $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null || echo dev))
DOCKER_REV  := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)

docker:
	rm -rf $(DOCKER_CTX)
	mkdir -p $(DOCKER_CTX)/linux/$(DOCKER_ARCH) $(DOCKER_CTX)/docker
	CGO_ENABLED=0 GOOS=linux GOARCH=$(DOCKER_ARCH) go build -trimpath -ldflags "-s -w -X main.version=$(DOCKER_VER)" -o $(DOCKER_CTX)/linux/$(DOCKER_ARCH)/pupptyeer ./cmd/pupptyeer
	CGO_ENABLED=0 GOOS=linux GOARCH=$(DOCKER_ARCH) go build -C mcp -trimpath -ldflags "-s -w -X main.version=$(DOCKER_VER)" -o ../$(DOCKER_CTX)/linux/$(DOCKER_ARCH)/pupptyeer-mcp .
	cp docker/pupptyeer-entrypoint.sh $(DOCKER_CTX)/docker/pupptyeer-entrypoint.sh
	docker build --platform linux/$(DOCKER_ARCH) -f docker/Dockerfile \
		--build-arg PUPPTYEER_VERSION=$(DOCKER_VER) --build-arg REVISION=$(DOCKER_REV) \
		-t pupptyeer:local $(DOCKER_CTX)
