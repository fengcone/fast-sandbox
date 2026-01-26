# Fast Sandbox Performance Analysis

## Critical Path Latency Breakdown

Target: <50ms end-to-end for FastPath mode

### CLI -> Controller (FastPath gRPC)
- Expected: 1-5ms
- Measured: TBD

### Registry.Allocate
- Expected: <1ms
- Measured: ~1.3ms (from benchmarks)
- Sub-operations (100 agents):
  - Candidate filtering: <0.5ms
  - Scoring: <0.5ms
  - Selection: <0.5ms
- Large pool (1000 agents): ~14ms

### Agent.CreateSandbox RPC
- Expected: 5-20ms
- Measured: TBD

### containerd Runtime
- Expected: 10-30ms
- Measured: TBD
- Sub-operations:
  - Image pull (cached): 0ms
  - Container create: TBD
  - Container start: TBD

## Benchmark Results

### Registry Allocation (Baseline)

| Benchmark | ns/op | B/op | allocs/op |
|-----------|-------|------|-----------|
| Allocate (100 agents) | 1319 | 993 | 4 |
| AllocateWithPorts | 1329 | 993 | 4 |
| AllocateNoImageMatch | 1253 | 993 | 4 |
| AllocateLargePool (1000) | 14148 | 8298 | 4 |
| RegisterOrUpdate | 125.4 | 91 | 4 |
| GetAllAgents (100) | 4864 | 19328 | 2 |
| GetAllAgentsLargePool (1000) | 55731 | 188419 | 2 |
| GetAgentByID | 16.66 | 0 | 0 |
| Release | 14.84 | 0 | 0 |
| CleanupStaleAgents | 38.63 | 0 | 0 |

Run benchmarks with:
```bash
go test ./internal/controller/agentpool/ -bench=. -benchmem
```

## Profiling

### CPU Profiling

Start controller with profiling:
```bash
./scripts/profile.sh
```

Capture 30-second profile:
```bash
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30 > /tmp/controller_cpu.prof
```

View profile:
```bash
go tool pprof -http=:8080 /tmp/controller_cpu.prof
```

### Flamegraph

Generate flamegraph from captured profile:
```bash
./scripts/flamegraph.sh
```

## Metrics

Prometheus metrics are available for FastPath operations:

- `fastpath_create_sandbox_duration_seconds` - Histogram of CreateSandbox RPC duration
  - Labels: `mode` (fast/strong), `success` (true/false)
  - Buckets: 1ms, 5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 500ms, 1s

## Timing Logs

Enable detailed timing logs with verbosity level 2:

```bash
# Controller
./bin/controller -v=2

# Agent
./bin/agent -v=2
```

This will show:
- Registry allocation timing breakdown
- Agent RPC call timing
- containerd Runtime timing breakdown

## Optimization Targets

1. [ ] Registry allocation - minimize lock contention (currently ~1.3ms for 100 agents)
2. [ ] Agent RPC - consider connection pooling (gRPC connection reuse)
3. [ ] containerd - ensure image cache hit (zero-pull goal)
4. [ ] Controller reconcile - optimize periodic sync interval
5. [ ] FastPath gRPC server - measure actual gRPC call overhead

## Performance Goals

| Operation | Target | Current | Status |
|-----------|--------|---------|--------|
| FastPath CreateSandbox (e2e) | <50ms | TBD | 🔍 To Measure |
| Registry.Allocate (100 agents) | <2ms | ~1.3ms | ✅ Pass |
| Registry.Allocate (1000 agents) | <20ms | ~14ms | ✅ Pass |
| Agent.CreateSandbox RPC | <20ms | TBD | 🔍 To Measure |
| containerd container start | <30ms | TBD | 🔍 To Measure |

## Debugging Performance Issues

If performance degrades:

1. **Run benchmarks** to detect regression:
   ```bash
   go test ./internal/controller/agentpool/ -bench=. -benchmem
   ```

2. **Capture CPU profile** to identify hotspots:
   ```bash
   go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30 > /tmp/cpu.prof
   go tool pprof -list Allocate /tmp/cpu.prof
   ```

3. **Check logs** with `-v=2` to see timing breakdown

4. **Check metrics** at `:9091/metrics` for Prometheus histograms
