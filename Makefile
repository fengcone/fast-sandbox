# Default registry and images
REGISTRY ?= fast-sandbox
AGENT_IMAGE ?= $(REGISTRY)/agent:dev
CONTROLLER_IMAGE ?= $(REGISTRY)/controller:dev
JANITOR_IMAGE ?= $(REGISTRY)/janitor:dev

# Go settings
GO ?= go
GOFLAGS ?= -gcflags="all=-N -l"

.PHONY: all build build-controller build-agent build-janitor build-agent-linux build-controller-linux build-janitor-linux test tidy e2e docker-agent docker-controller docker-janitor kind-load-agent kind-load-controller kind-load-janitor help

all: build

help:
	@echo "Common targets:"
	@echo "  make build                  - build all binaries"
	@echo "  make build-agent-linux      - build agent binary for linux/amd64"
	@echo "  make build-controller-linux - build controller binary for linux/amd64"
	@echo "  make build-janitor-linux    - build janitor binary for linux/amd64"
	@echo "  make test                   - run unit tests"
	@echo "  make docker-agent           - build agent image"
	@echo "  make docker-controller      - build controller image"
	@echo "  make docker-janitor         - build janitor image"

build: build-controller build-agent build-janitor build-fsb-ctl

build-controller:
	$(GO) build $(GOFLAGS) -o bin/controller ./cmd/controller

build-agent:
	$(GO) build $(GOFLAGS) -o bin/agent ./cmd/agent

build-janitor:
	$(GO) build $(GOFLAGS) -o bin/janitor ./cmd/janitor

build-fsb-ctl:
	$(GO) build $(GOFLAGS) -o bin/fsb-ctl ./cmd/fsb-ctl

# Cross-compile for linux/amd64 (for docker images)
build-agent-linux:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o bin/agent ./cmd/agent

build-controller-linux:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o bin/controller ./cmd/controller

build-janitor-linux:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o bin/janitor ./cmd/janitor

build-fsb-ctl-linux:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o bin/fsb-ctl ./cmd/fsb-ctl

test:
	$(GO) test $(GOFLAGS) ./...

tidy:
	$(GO) mod tidy

docker-agent: build-agent-linux
	docker build -t $(AGENT_IMAGE) -f build/Dockerfile.agent .

docker-controller: build-controller-linux
	docker build -t $(CONTROLLER_IMAGE) -f build/Dockerfile.controller .

docker-janitor: build-janitor-linux
	docker build -t $(JANITOR_IMAGE) -f build/Dockerfile.janitor .

kind-load-agent: docker-agent
	kind load docker-image $(AGENT_IMAGE) --name fast-sandbox

kind-load-controller: docker-controller
	kind load docker-image $(CONTROLLER_IMAGE) --name fast-sandbox

kind-load-janitor: docker-janitor
	kind load docker-image $(JANITOR_IMAGE) --name fast-sandbox