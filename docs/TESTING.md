# Testing Coverage Goals

## Target Coverage

| Module | Target | Current | Status |
|--------|--------|---------|--------|
| agentpool | 90% | 81.7% | 🟡 Approaching |
| fastpath | 85% | 48.5% | 🔴 Needs Work |
| runtime | 75% | 32.7% | 🔴 Needs Work |
| janitor | 80% | 6.8% | 🔴 Needs Work |
| api | 85% | 88.1% | ✅ Pass |

**Note:** Runtime and Janitor coverage is lower because these modules require integration tests with containerd and Kubernetes. Many edge cases are tested, but the overall statement coverage is lower due to:
- Containerd client integration (requires actual containerd socket)
- Kubernetes API client interactions (require test cluster or extensive mocking)
- Async cleanup operations (difficult to unit test)

## Running Tests

### All Tests
```bash
go test ./... -v
```

### With Coverage
```bash
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out -o coverage.html
```

### Module-Specific Tests
```bash
# Registry (agent allocation logic)
go test ./internal/controller/agentpool/ -v

# FastPath (gRPC server)
go test ./internal/controller/fastpath/ -v

# Runtime (containerd operations)
go test ./internal/agent/runtime/ -v

# Janitor (orphan cleanup)
go test ./internal/janitor/ -v

# API (HTTP client)
go test ./internal/api/ -v
```

### Race Detection
```bash
go test ./... -race
```

### Skip Integration Tests
```bash
go test ./... -short
```

## Test Files

| Module | Test File | Tests |
|--------|-----------|-------|
| agentpool | `internal/controller/agentpool/registry_test.go` | 30 |
| fastpath | `internal/controller/fastpath/server_test.go` | 19 |
| runtime | `internal/agent/runtime/containerd_runtime_test.go` | 18 |
| runtime | `internal/agent/runtime/sandbox_manager_test.go` | 26 |
| janitor | `internal/janitor/cleanup_test.go` | 4 |
| janitor | `internal/janitor/scanner_test.go` | 9 |
| api | `internal/api/agent_client_test.go` | 18 |

**Total: 124 unit tests**

## Benchmark Tests

Registry allocation benchmarks are available:

```bash
go test ./internal/controller/agentpool/ -bench=. -benchmem
```

| Benchmark | ns/op | Description |
|-----------|-------|-------------|
| BenchmarkRegistryAllocate | 1312 | Standard allocation (100 agents) |
| BenchmarkRegistryAllocateWithPorts | 1469 | With port constraints |
| BenchmarkRegistryAllocateLargePool | 14613 | Large pool (1000 agents) |

## Performance Profiling

CPU profiling is available via pprof:

```bash
# Start controller with profiling
./bin/controller

# Capture 30-second profile
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30 > /tmp/cpu.prof

# View profile
go tool pprof -http=:8080 /tmp/cpu.prof
```

See `docs/PERFORMANCE.md` for detailed performance analysis.
