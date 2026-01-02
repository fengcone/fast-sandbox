# Default registry and images
REGISTRY ?= fast-sandbox
AGENT_IMAGE ?= $(REGISTRY)/agent:dev

# Go settings
GO ?= go
GOFLAGS ?=

.PHONY: all build build-controller build-agent build-agent-linux test tidy e2e docker-agent kind-load-agent help

all: build

help:
	@echo "Common targets:"
	@echo "  make build             - build controller and agent binaries"
	@echo "  make build-agent-linux - build agent binary for linux/amd64"
	@echo "  make test              - run unit tests (go test ./...)"
	@echo "  make e2e               - run Ginkgo e2e tests"
	@echo "  make e2e-shell         - run legacy shell-based e2e test"
	@echo "  make docker-agent      - build agent container image (requires build-agent-linux)"
	@echo "  make kind-load-agent   - load agent image into kind cluster 'fast-sandbox'"

build: build-controller build-agent

build-controller:
	$(GO) build $(GOFLAGS) -o bin/controller ./cmd/controller

build-agent:
	$(GO) build $(GOFLAGS) -o bin/agent ./cmd/agent

# Cross-compile agent for linux/amd64 (for docker image)
build-agent-linux:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o bin/agent ./cmd/agent

test:
	$(GO) test $(GOFLAGS) ./...

tidy:
	$(GO) mod tidy

# Build the agent image (requires bin/agent to be built for linux/amd64)
# Usage:
#   make build-agent-linux docker-agent AGENT_IMAGE=my-registry/fast-sandbox-agent:dev

docker-agent: build-agent-linux
	docker build -t $(AGENT_IMAGE) -f build/Dockerfile.agent .

# Load the agent image into the local kind cluster for testing
kind-load-agent:
	kind load docker-image $(AGENT_IMAGE) --name fast-sandbox || echo "kind cluster 'fast-sandbox' not found or kind not installed"

# Placeholder for end-to-end tests; to be implemented later
# Can be wired to run controller in cluster, apply CRDs, and validate behavior.
e2e:
	@echo "Running Ginkgo e2e tests..."
	go test -v ./test/e2e/... -ginkgo.v

# 旧的 shell 脚本测试（保留用于参考）
e2e-shell:
	@echo "Running shell-based e2e test..."
	@chmod +x test/e2e/test_sandboxclaim_scheduling.sh
	@test/e2e/test_sandboxclaim_scheduling.sh
