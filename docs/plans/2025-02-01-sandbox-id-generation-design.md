# SandboxID Generation Strategy Design

> **Status:** Design Phase - Pending Approval

## Problem Statement

Current implementation uses `sandbox.Name` directly as `SandboxID`. This approach has several issues:

1. **Not unique across namespaces** - Same name in different namespaces would conflict
2. **Container naming** - SandboxID becomes the containerd container name, using user-provided name is unprofessional
3. **No correlation with creation** - Cannot trace when/who created the sandbox

## Proposed Solution

### Two Generation Strategies

#### Fast Mode (FastPath Fast Consistency)
```go
sandboxID = md5(name + namespace + UnixNano时间戳)
```

**Key points:**
- Generate at FastPath Server before calling Agent API
- Use `time.Now().UnixNano()` for uniqueness
- Store timestamp in annotation `fast-sandbox.io/createTimestamp`
- Write generated `sandboxID` to `status.sandboxID`

#### Strong Mode (FastPath Strong Consistency) & Direct CRD Creation
```go
sandboxID = sandbox.UID
```

**Key points:**
- Create CRD first to get K8s assigned UID
- Use UID (36 chars) as sandboxID
- Write UID to `status.sandboxID`

## Component Changes

### 1. FastPath Server (`internal/controller/fastpath/server.go`)

#### createFast Function
```go
func (s *Server) createFast(...) {
    // Generate sandboxID
    ts := time.Now().UnixNano()
    sandboxID := generateHashID(tempSB.Name, tempSB.Namespace, ts)

    // Store timestamp in annotation for idempotency
    tempSB.SetAnnotations(map[string]string{
        common.AnnotationAllocation: common.BuildAllocationJSON(agent.PodName, agent.NodeName),
        common.AnnotationCreateTimestamp: strconv.FormatInt(ts, 10),
    })

    // Call Agent with generated sandboxID
    _, err = s.AgentClient.CreateSandbox(agent.PodIP, &api.CreateSandboxRequest{
        Sandbox: api.SandboxSpec{
            SandboxID:  sandboxID,  // ← Changed from tempSB.Name
            ClaimName:  tempSB.Name,
            ClaimUID:   "", // Fast mode no UID yet
            // ...
        },
    })

    // Async create CRD
    go s.asyncCreateCRDWithRetry(asyncCtx, tempSB, sandboxID)
}
```

#### createStrong Function
```go
func (s *Server) createStrong(...) {
    // Create CRD first to get UID
    if err = s.K8sClient.Create(ctx, tempSB); err != nil {
        return nil, err
    }

    // Use UID as sandboxID
    sandboxID := string(tempSB.UID)

    // Call Agent with UID
    _, err = s.AgentClient.CreateSandbox(agent.PodIP, &api.CreateSandboxRequest{
        Sandbox: api.SandboxSpec{
            SandboxID:  sandboxID,  // ← Changed from tempSB.Name
            ClaimUID:   string(tempSB.UID),
            ClaimName:  tempSB.Name,
            // ...
        },
    })
}
```

### 2. Controller (`internal/controller/sandbox_controller.go`)

#### Sync Agent Status to CRD
```go
// When syncing from Agent status to CRD
latest.Status.SandboxID = status.SandboxID  // ← Already correct (line 687)
```

#### Call Agent API (for non-FastPath created sandboxes)
```go
// Check if sandboxID exists in status
sandboxID := sandbox.Status.SandboxID
if sandboxID == "" {
    // Legacy behavior or migrate to use name
    sandboxID = sandbox.Name
}

_, err = r.AgentClient.CreateSandbox(agent.PodIP, &api.CreateSandboxRequest{
    Sandbox: api.SandboxSpec{
        SandboxID:  sandboxID,
        ClaimUID:   string(sandbox.UID),
        ClaimName:  sandbox.Name,
        // ...
    },
})
```

### 3. CLI (`cmd/fsb-ctl/cmd/*.go`)

**No changes needed** - CLI continues to use `sandbox.Name` as user-facing identifier.

```go
// Current code remains unchanged
sandboxID := args[0]  // This is the CRD name
// FastPath server internally looks up CRD by name to get sandboxID
```

### 4. Agent Side (`internal/agent/runtime/`)

**No changes needed** - Agent already uses `SandboxID` as:
- Map key in `sandbox_manager.go`
- Container name in `containerd_runtime.go`
- Log file name

**Impact:** Container names will change from user-friendly names to hash/UID.

### 5. Common Annotations (`internal/controller/common/`)

Add new annotation constant:
```go
const AnnotationCreateTimestamp = "fast-sandbox.io/createTimestamp"
```

## Utility Functions

```go
// pkg/util/idgen/idgen.go
package idgen

import (
    "crypto/md5"
    "encoding/hex"
    "fmt"
    "strconv"
)

// GenerateHashID creates a sandboxID from name, namespace, and timestamp
func GenerateHashID(name, namespace string, timestamp int64) string {
    data := fmt.Sprintf("%s:%s:%d", name, namespace, timestamp)
    hash := md5.Sum([]byte(data))
    return hex.EncodeToString(hash[:])
}

// ParseTimestamp extracts timestamp from annotation
func ParseTimestamp(annotations map[string]string) (int64, error) {
    if annotations == nil {
        return 0, fmt.Errorf("no annotations")
    }
    tsStr, ok := annotations["fast-sandbox.io/createTimestamp"]
    if !ok {
        return 0, fmt.Errorf("no createTimestamp annotation")
    }
    return strconv.ParseInt(tsStr, 10, 64)
}
```

## Data Flow Diagrams

### Fast Mode
```
User → FastPath Server
                    ↓
        Generate md5(name + ns + UnixNano)
                    ↓
        Call Agent with sandboxID ──────→ Agent creates container (name = sandboxID)
                    ↓
        Async create CRD with:
          - annotation: createTimestamp
          - status.sandboxID: (filled by Controller sync)
```

### Strong Mode
```
User → FastPath Server
                    ↓
        Create CRD first → K8s assigns UID
                    ↓
        Call Agent with sandboxID = UID ──→ Agent creates container (name = UID)
                    ↓
        Controller fills status.sandboxID = UID
```

## Migration Strategy

1. **Phase 1:** Implement new ID generation, write to `status.sandboxID`
2. **Phase 2:** Update Controller to prefer `status.sandboxID` over `name`
3. **Phase 3:** (Optional) Legacy support - handle existing CRDs without `status.sandboxID`

## Open Questions

1. **Container name cleanup** - Old containers named by user still exist, need cleanup?
2. **Log file paths** - `/var/log/fast-sandbox/{sandboxID}.log` - new format needed
3. **Metrics labels** - Current metrics may use sandbox name, consider impact

## References

- MD5 hash: 32 character hex string
- K8s UID: 36 character UUID format
- Current FastPath code: `internal/controller/fastpath/server.go`
