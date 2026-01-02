# Default registry and images
REGISTRY ?= fast-sandbox
AGENT_IMAGE ?= $(REGISTRY)/agent:dev
CONTROLLER_IMAGE ?= $(REGISTRY)/controller:dev

# Go settings
GO ?= go
GOFLAGS ?=

.PHONY: all build build-controller build-agent build-agent-linux build-controller-linux test tidy e2e e2e-prepare docker-agent docker-controller kind-load-agent kind-load-controller help

all: build

help:
	@echo "Common targets:"
	@echo "  make build                  - build controller and agent binaries"
	@echo "  make build-agent-linux      - build agent binary for linux/amd64"
	@echo "  make build-controller-linux - build controller binary for linux/amd64"
	@echo "  make test                   - run unit tests (go test ./...)"
	@echo "  make e2e                    - run Ginkgo e2e tests (fully automated)"
	@echo "  make e2e-shell              - run legacy shell-based e2e test"
	@echo "  make docker-agent           - build agent container image"
	@echo "  make docker-controller      - build controller container image"
	@echo "  make kind-load-agent        - load agent image into kind cluster 'fast-sandbox'"
	@echo "  make kind-load-controller   - load controller image into kind cluster 'fast-sandbox'"

build: build-controller build-agent

build-controller:
	$(GO) build $(GOFLAGS) -o bin/controller ./cmd/controller

build-agent:
	$(GO) build $(GOFLAGS) -o bin/agent ./cmd/agent

# Cross-compile agent for linux/amd64 (for docker image)
build-agent-linux:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o bin/agent ./cmd/agent

# Cross-compile controller for linux/amd64 (for docker image)
build-controller-linux:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o bin/controller ./cmd/controller

test:
	$(GO) test $(GOFLAGS) ./...

tidy:
	$(GO) mod tidy

# Build the agent image (requires bin/agent to be built for linux/amd64)
docker-agent: build-agent-linux
	docker build -t $(AGENT_IMAGE) -f build/Dockerfile.agent .

# Build the controller image (requires bin/controller to be built for linux/amd64)
docker-controller: build-controller-linux
	docker build -t $(CONTROLLER_IMAGE) -f build/Dockerfile.controller .

# Load the agent image into the local kind cluster for testing
kind-load-agent:
	kind load docker-image $(AGENT_IMAGE) --name fast-sandbox || echo "kind cluster 'fast-sandbox' not found or kind not installed"

# Load the controller image into the local kind cluster for testing
kind-load-controller:
	kind load docker-image $(CONTROLLER_IMAGE) --name fast-sandbox || echo "kind cluster 'fast-sandbox' not found or kind not installed"

# Prepare e2e test environment: build and load images to KIND cluster
e2e-prepare:
	@echo "Preparing e2e test environment..."
	@echo "Building and loading Agent image..."
	@$(MAKE) docker-agent AGENT_IMAGE=fast-sandbox-agent:dev
	@$(MAKE) kind-load-agent AGENT_IMAGE=fast-sandbox-agent:dev
	@echo "Building and loading Controller image..."
	@$(MAKE) docker-controller CONTROLLER_IMAGE=fast-sandbox/controller:dev
	@$(MAKE) kind-load-controller CONTROLLER_IMAGE=fast-sandbox/controller:dev
	@echo "E2E test environment prepared successfully"

# Run Ginkgo e2e tests (automatically builds and loads images first)
e2e: e2e-prepare
	@echo "Running Ginkgo e2e tests..."
	go test -v ./test/e2e/... -ginkgo.v

# 旧的 shell 脚本测试（保留用于参考）
e2e-shell:
	@echo "Running shell-based e2e test..."
	@chmod +x test/e2e/test_sandboxclaim_scheduling.sh
	@test/e2e/test_sandboxclaim_scheduling.sh
