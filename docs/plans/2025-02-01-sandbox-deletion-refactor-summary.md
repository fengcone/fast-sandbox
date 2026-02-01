# Sandbox Deletion Refactor Summary

## Problem

The original code used `terminatedSandboxes map[string]int64` to store deleted sandboxes, waiting for Controller confirmation. This map was **write-only** (never cleaned up), causing:

1. **Memory leak** - Deleted sandboxes permanently occupied memory
2. **Name conflict** - Creating a sandbox with the same name immediately after deletion would show conflicting states (both "running" and "terminated" in status)
3. **State redundancy** - Two maps (`sandboxes` and `terminatedSandboxes`) increased complexity

Additionally, the `creating map[string]chan struct{}` was over-engineered for handling concurrent creates.

## Solution

Adopted a Kubernetes Pod-like deletion pattern:

### Agent Side
- **Before**: `running → terminating → asyncDelete() → move to terminatedSandboxes`
- **After**: `running → terminating → asyncDelete() → directly delete from sandboxes`

### Controller Side
- **Before**: Check `agent.SandboxStatuses[id].Phase == "terminated"` to confirm deletion
- **After**: Check `!hasStatus(agent.SandboxStatuses[id])` to confirm deletion

### Concurrent Create Handling
- **Before**: Use `creating map[string]chan struct{}` with channel signaling
- **After**: Use "creating" phase placeholder with direct locking:
  1. Lock → check exists → add "creating" placeholder → Unlock
  2. Call runtime.CreateSandbox (slow operation, outside lock)
  3. Lock → update metadata / delete placeholder on failure → Unlock

## Changes

### Agent Side (`internal/agent/runtime/sandbox_manager.go`)
1. Removed `terminatedSandboxes map[string]int64` field
2. Removed `creating map[string]chan struct{}` field
3. `asyncDelete` now directly deletes from `sandboxes` map
4. `GetSandboxStatuses` only returns active sandboxes
5. `CreateSandbox` simplified with lock-based concurrent control

### Controller Side (`internal/controller/sandbox_controller.go`)
1. `handleTerminatingDeletion` now checks `!hasStatus` instead of `phase == "terminated"`

### Tests
1. Updated Agent tests to expect direct deletion (sandbox removed, not "terminated")
2. Updated Controller tests to verify `!hasStatus` confirmation logic

## Impact

- ✅ Simplified state management - one map instead of two
- ✅ No memory leak from `terminatedSandboxes`
- ✅ No name conflict when recreating same-named sandbox
- ✅ Smaller lock granularity for concurrent creates
- ✅ Removed complex channel-based synchronization

## Commits

1. `adef74f` - refactor(agent): remove terminatedSandboxes map, directly delete after asyncDelete completes
2. `321dc21` - test(agent): update tests for direct deletion instead of terminated state
3. (controller commit) - refactor(controller): confirm deletion by !hasStatus instead of phase==terminated
4. `fd616ae` - fix(agent): add lock protection when updating metadata, clean up placeholder on create failure

## Verification

```bash
# Unit tests
go test -v ./internal/agent/runtime/...
go test -v ./internal/controller/...

# Manual test
# 1. Create a sandbox
# 2. Delete it
# 3. Immediately create sandbox with same name
# 4. Verify: Same-named sandbox creates successfully, no conflicts
```

## References

- Kubernetes Pod Deletion: https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-termination
- Original discussion: `docs/plans/2025-01-28-debug-same-name-recreate.md`
