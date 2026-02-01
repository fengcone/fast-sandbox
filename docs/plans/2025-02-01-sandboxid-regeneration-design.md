# SandboxID Regeneration Design

> **Status:** Approved - Implementing

## Problem Statement

Previous design attempted to persist `status.sandboxID` in Fast mode, but K8s `Create` operation does not persist status subresource. This caused CRD to get stuck in "Bound" phase because Controller couldn't find the Agent status (keyed by sandboxID hash).

## Solution: Deterministic Regeneration

**Core insight:** sandboxID doesn't need to be persisted - it can be deterministically regenerated from stored metadata.

## Architecture

### Fast Mode (FastPath Fast Consistency)

```
FastPath Server                          Agent
     |                                      |
     | 1. Generate sandboxID = md5(name+ns+ts)
     | 2. Call Agent.CreateSandbox(sandboxID) ──> Container created
     | 3. Set label: sandbox.fast.io/created-by=fastpath-fast
     | 4. Set annotation: sandbox.fast.io/createTimestamp=<ts>
     | 5. Set annotation: sandbox.fast.io/allocation={...}
     | 6. Async create CRD                     |
     |                                      |
     v                                      v
  CRD created (no status.sandboxID)    Agent stores status by sandboxID

Controller Reconciliation:
  1. Read CRD (status.sandboxID is empty)
  2. Check label: created-by=fastpath-fast
  3. Read annotation: createTimestamp
  4. Regenerate: sandboxID = md5(name+ns+timestamp)
  5. Lookup Agent status by regenerated sandboxID
  6. Sync status to CRD
```

### Strong Mode (FastPath Strong Consistency)

```
FastPath Server
     |
     | 1. Create CRD first → K8s assigns UID
     | 2. Set status.sandboxID = UID
     | 3. Call Agent.CreateSandbox(UID) ──> Agent
     | 4. Controller updates status
```

## Label/Annotation Schema

| Key | Value | Mode | Purpose |
|-----|-------|------|---------|
| `sandbox.fast.io/created-by` | `fastpath-fast` | Fast | Identifies Fast mode creation |
| `sandbox.fast.io/createTimestamp` | UnixNano timestamp | Fast | For sandboxID regeneration |
| `sandbox.fast.io/allocation` | JSON {pod, node} | Both | Agent allocation info |

## Component Changes

### 1. FastPath Server (`internal/controller/fastpath/server.go`)

**createFast:**
```go
// Generate sandboxID
createTimestamp := time.Now().UnixNano()
sandboxID := idgen.GenerateHashID(tempSB.Name, tempSB.Namespace, createTimestamp)

// Call Agent with sandboxID
s.AgentClient.CreateSandbox(agent.PodIP, &api.CreateSandboxRequest{
    Sandbox: api.SandboxSpec{SandboxID: sandboxID, ...},
})

// Set label and annotations (NO status.sandboxID)
tempSB.SetLabels(map[string]string{
    common.LabelCreatedBy: common.CreatedByFastPathFast,
})
tempSB.SetAnnotations(map[string]string{
    common.AnnotationAllocation:      common.BuildAllocationJSON(...),
    common.AnnotationCreateTimestamp: strconv.FormatInt(createTimestamp, 10),
})

// Async create CRD
go s.asyncCreateCRDWithRetry(asyncCtx, tempSB)
```

**createStrong:**
- Keep existing design: write `status.sandboxID = UID`

### 2. Common Constants (`internal/controller/common/annotations.go`)

```go
const (
    // Labels
    LabelCreatedBy         = "sandbox.fast.io/created-by"
    CreatedByFastPathFast  = "fastpath-fast"

    // Annotations
    AnnotationAllocation      = "sandbox.fast.io/allocation"
    AnnotationCreateTimestamp = "sandbox.fast.io/createTimestamp"
)
```

### 3. Controller (`internal/controller/sandbox_controller.go`)

**getSandboxID helper:**
```go
func (r *SandboxReconciler) getSandboxID(sandbox *apiv1alpha1.Sandbox) string {
    // 1. Status already has sandboxID (Strong mode or already synced)
    if sandbox.Status.SandboxID != "" {
        return sandbox.Status.SandboxID
    }

    // 2. Fast mode: regenerate from label + annotation
    if sandbox.Labels[common.LabelCreatedBy] == common.CreatedByFastPathFast {
        if tsStr, ok := sandbox.Annotations[common.AnnotationCreateTimestamp]; ok {
            if timestamp, err := strconv.ParseInt(tsStr, 10, 64); err == nil {
                return idgen.GenerateHashID(sandbox.Name, sandbox.Namespace, timestamp)
            }
        }
    }

    // 3. Legacy fallback
    return sandbox.Name
}
```

## Data Flow Example

### Fast Mode Creation
```
Request: Create sandbox "test-sb" in namespace "default"

1. FastPath generates:
   - timestamp: 1706789123456789001
   - sandboxID: md5("test-sb:default:1706789123456789001") = "a1b2c3d4e5f6..."

2. Agent creates container with name = "a1b2c3d4e5f6..."
   Agent.SandboxStatuses["a1b2c3d4e5f6..."] = {Phase: "running", ...}

3. CRD created:
   metadata.labels.created-by: "fastpath-fast"
   metadata.annotations.createTimestamp: "1706789123456789001"
   metadata.annotations.allocation: "{...}"
   status.sandboxID: "" (empty!)

4. Controller reconciliation:
   - getSandboxID(sandbox)
   - Sees label "fastpath-fast"
   - Reads timestamp "1706789123456789001"
   - Regenerates: md5("test-sb:default:1706789123456789001") = "a1b2c3d4e5f6..."
   - Looks up: agent.SandboxStatuses["a1b2c3d4e5f6..."] ✓ Found!
   - Syncs status to CRD
```

## Implementation Tasks

1. [ ] Add Label constants to common/annotations.go
2. [ ] Update createFast to set label, remove status.sandboxID assignment
3. [ ] Revert asyncCreateCRDWithRetry to original (no status update)
4. [ ] Update getSandboxID() with regeneration logic
5. [ ] Add strconv import to sandbox_controller.go
6. [ ] Test Fast mode creation and reconciliation

## References

- `pkg/util/idgen/idgen.go` - `GenerateHashID(name, namespace, timestamp) string`
- Agent status storage: `agent.SandboxStatuses[sandboxID]`
- K8s Create operation: does NOT persist status subresource
