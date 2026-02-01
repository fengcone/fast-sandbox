# SandboxID Generation Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement proper sandboxID generation strategy - md5 hash for Fast mode, UID for Strong mode/CRD creation.

**Architecture:**
- Fast mode: Generate md5(name + namespace + UnixNano) before calling Agent, store timestamp in annotation
- Strong mode/CRD: Use K8s UID as sandboxID after CRD creation
- Controller: Prefer status.sandboxID over name when calling Agent API

**Tech Stack:** Go 1.21+, crypto/md5, K8s controller-runtime

---

## Task 1: Add ID Generation Utility Package

**Files:**
- Create: `pkg/util/idgen/idgen.go`
- Test: `pkg/util/idgen/idgen_test.go`

**Step 1: Create the package directory**

```bash
mkdir -p pkg/util/idgen
```

**Step 2: Write the idgen.go file**

```go
package idgen

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
)

// GenerateHashID creates a sandboxID from name, namespace, and timestamp
// Returns a 32-character md5 hex string
func GenerateHashID(name, namespace string, timestamp int64) string {
	data := fmt.Sprintf("%s:%s:%d", name, namespace, timestamp)
	hash := md5.Sum([]byte(data))
	return hex.EncodeToString(hash[:])
}
```

**Step 3: Write the failing test**

```go
package idgen

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateHashID(t *testing.T) {
	name := "test-sb"
	namespace := "default"
	timestamp := int64(1234567890123456789)

	id := GenerateHashID(name, namespace, timestamp)

	// Should be 32 character hex string (md5)
	assert.Len(t, id, 32, "MD5 hash should be 32 characters")

	// Same inputs should produce same output
	id2 := GenerateHashID(name, namespace, timestamp)
	assert.Equal(t, id, id2, "Same inputs should produce same hash")

	// Different timestamp should produce different output
	id3 := GenerateHashID(name, namespace, timestamp+1)
	assert.NotEqual(t, id, id3, "Different timestamp should produce different hash")

	// Different namespace should produce different output
	id4 := GenerateHashID(name, "other", timestamp)
	assert.NotEqual(t, id, id4, "Different namespace should produce different hash")
}
```

**Step 4: Run test to verify it passes**

```bash
cd /Users/fengjianhui/WorkSpaceL/fast-sandbox
go test -v ./pkg/util/idgen/...
```

Expected: PASS

**Step 5: Commit**

```bash
git add pkg/util/idgen/
git commit -m "feat(pkg): add idgen utility for sandboxID generation"
```

---

## Task 2: Add CreateTimestamp Annotation Constant

**Files:**
- Modify: `internal/controller/common/annotations.go`

**Step 1: Add the annotation constant**

Add after line 10:

```go
const (
	// AnnotationAllocation 临时存储 FastPath 的分配信息，Controller 会搬运到 status 后删除
	AnnotationAllocation = "sandbox.fast.io/allocation"
	// AnnotationCreateTimestamp 存储 Fast 模式创建时的时间戳，用于幂等性检查
	AnnotationCreateTimestamp = "sandbox.fast.io/createTimestamp"
)
```

**Step 2: Run build to verify**

```bash
go build ./internal/controller/common/...
```

Expected: No errors

**Step 3: Commit**

```bash
git add internal/controller/common/annotations.go
git commit -m "feat(common): add AnnotationCreateTimestamp constant"
```

---

## Task 3: Update FastPath Server - Fast Mode

**Files:**
- Modify: `internal/controller/fastpath/server.go`

**Step 1: Add import for idgen package**

Add to imports (around line 19):
```go
import (
    // ... existing imports ...
    "fast-sandbox/pkg/util/idgen"
)
```

**Step 2: Modify createFast function**

Replace lines 90-135 with:

```go
func (s *Server) createFast(tempSB *apiv1alpha1.Sandbox, agent *agentpool.AgentInfo, req *fastpathv1.CreateRequest) (*fastpathv1.CreateResponse, error) {
	start := time.Now()
	var err error
	defer func() {
		duration := time.Since(start).Seconds()
		success := "true"
		if err != nil {
			success = "false"
			klog.ErrorS(err, "Fast mode sandbox creation failed", "name", tempSB.Name, "namespace", tempSB.Namespace, "duration", duration)
		} else {
			klog.InfoS("Fast mode sandbox creation completed", "name", tempSB.Name, "namespace", tempSB.Namespace, "duration", duration)
		}
		createSandboxDuration.WithLabelValues("fast", success).Observe(duration)
	}()

	// Generate sandboxID using md5 hash
	createTimestamp := time.Now().UnixNano()
	sandboxID := idgen.GenerateHashID(tempSB.Name, tempSB.Namespace, createTimestamp)

	klog.InfoS("Creating sandbox via agent (fast mode)", "name", tempSB.Name, "namespace", tempSB.Namespace,
		"agentPodIP", agent.PodIP, "agentPod", agent.PodName, "sandboxID", sandboxID)

	_, err = s.AgentClient.CreateSandbox(agent.PodIP, &api.CreateSandboxRequest{
		Sandbox: api.SandboxSpec{
			SandboxID:  sandboxID,
			ClaimName:  tempSB.Name,
			Image:      tempSB.Spec.Image,
			Command:    tempSB.Spec.Command,
			Args:       tempSB.Spec.Args,
			Env:        req.Envs,
			WorkingDir: tempSB.WorkingDir,
		},
	})
	if err != nil {
		klog.ErrorS(err, "Failed to create sandbox on agent", "name", tempSB.Name, "namespace", tempSB.Namespace, "agentPodIP", agent.PodIP)
		s.Registry.Release(agent.ID, tempSB)
		return nil, err
	}

	klog.InfoS("Sandbox created on agent, setting allocation annotation", "name", tempSB.Name, "namespace", tempSB.Namespace,
		"agentPod", agent.PodName, "node", agent.NodeName, "sandboxID", sandboxID)

	// 设置 allocation annotation，Controller 会搬运到 status 后删除
	tempSB.SetAnnotations(map[string]string{
		common.AnnotationAllocation:      common.BuildAllocationJSON(agent.PodName, agent.NodeName),
		common.AnnotationCreateTimestamp: strconv.FormatInt(createTimestamp, 10),
	})
	// 设置 status.sandboxID
	tempSB.Status.SandboxID = sandboxID
	// Status 其他字段留空，由 Controller 从 annotation 同步

	asyncCtx, _ := context.WithTimeout(context.Background(), 30*time.Second)
	go s.asyncCreateCRDWithRetry(asyncCtx, tempSB)
	return &fastpathv1.CreateResponse{SandboxId: tempSB.Name, AgentPod: agent.PodName, Endpoints: s.getEndpoints(agent.PodIP, tempSB)}, nil
}
```

**Step 3: Add strconv import if not present**

Add to imports:
```go
"strconv"
```

**Step 4: Run build to verify**

```bash
go build ./internal/controller/fastpath/...
```

Expected: No errors

**Step 5: Commit**

```bash
git add internal/controller/fastpath/server.go
git commit -m "feat(fastpath): use md5 hash for sandboxID in fast mode"
```

---

## Task 4: Update FastPath Server - Strong Mode

**Files:**
- Modify: `internal/controller/fastpath/server.go`

**Step 1: Modify createStrong function**

Replace lines 137-190 with:

```go
func (s *Server) createStrong(ctx context.Context, tempSB *apiv1alpha1.Sandbox, agent *agentpool.AgentInfo, req *fastpathv1.CreateRequest) (*fastpathv1.CreateResponse, error) {
	start := time.Now()
	var err error
	defer func() {
		duration := time.Since(start).Seconds()
		success := "true"
		if err != nil {
			success = "false"
			klog.ErrorS(err, "Strong mode sandbox creation failed", "name", tempSB.Name, "namespace", tempSB.Namespace, "duration", duration)
		} else {
			klog.InfoS("Strong mode sandbox creation completed", "name", tempSB.Name, "namespace", tempSB.Namespace, "duration", duration)
		}
		createSandboxDuration.WithLabelValues("strong", success).Observe(duration)
	}()

	klog.InfoS("Creating sandbox CRD first (strong mode)", "name", tempSB.Name, "namespace", tempSB.Namespace, "agentPod", agent.PodName, "node", agent.NodeName)

	// 设置 allocation annotation，与 CRD 创建同步
	tempSB.SetAnnotations(map[string]string{
		common.AnnotationAllocation: common.BuildAllocationJSON(agent.PodName, agent.NodeName),
	})

	if err = s.K8sClient.Create(ctx, tempSB); err != nil {
		klog.ErrorS(err, "Failed to create sandbox CRD", "name", tempSB.Name, "namespace", tempSB.Namespace)
		s.Registry.Release(agent.ID, tempSB)
		return nil, err
	}

	// Use UID as sandboxID
	sandboxID := string(tempSB.UID)
	tempSB.Status.SandboxID = sandboxID

	klog.InfoS("Sandbox CRD created, proceeding to create on agent", "name", tempSB.Name, "namespace", tempSB.Namespace,
		"uid", tempSB.UID, "sandboxID", sandboxID)

	_, err = s.AgentClient.CreateSandbox(agent.PodIP, &api.CreateSandboxRequest{
		Sandbox: api.SandboxSpec{
			SandboxID:  sandboxID,
			ClaimUID:   string(tempSB.UID),
			ClaimName:  tempSB.Name,
			Image:      tempSB.Spec.Image,
			Command:    tempSB.Spec.Command,
			Args:       tempSB.Spec.Args,
			Env:        req.Envs,
			WorkingDir: tempSB.WorkingDir,
		},
	})
	if err != nil {
		klog.ErrorS(err, "Failed to create sandbox on agent, rolling back CRD", "name", tempSB.Name, "namespace", tempSB.Namespace, "agentPodIP", agent.PodIP)
		s.K8sClient.Delete(ctx, tempSB)
		s.Registry.Release(agent.ID, tempSB)
		return nil, err
	}

	klog.InfoS("Sandbox created on agent, Controller will sync allocation from annotation to status",
		"name", tempSB.Name, "namespace", tempSB.Namespace, "assignedPod", agent.PodName, "nodeName", agent.NodeName)

	return &fastpathv1.CreateResponse{SandboxId: tempSB.Name, AgentPod: agent.PodName, Endpoints: s.getEndpoints(agent.PodIP, tempSB)}, nil
}
```

**Step 2: Update CRD with sandboxID**

Need to update the CRD status with sandboxID after Agent creation:

```go
// After Agent call succeeds, update CRD status
if err := s.K8sClient.Status().Update(ctx, tempSB); err != nil {
    klog.ErrorS(err, "Failed to update CRD status with sandboxID", "name", tempSB.Name)
    // Non-fatal error, continue
}
```

Add this before the return statement in createStrong.

**Step 3: Run build to verify**

```bash
go build ./internal/controller/fastpath/...
```

**Step 4: Commit**

```bash
git add internal/controller/fastpath/server.go
git commit -m "feat(fastpath): use UID as sandboxID in strong mode"
```

---

## Task 5: Update Controller to Use status.sandboxID

**Files:**
- Modify: `internal/controller/sandbox_controller.go`

**Step 1: Add helper function to get sandboxID**

Add after line 100 (or in a helper section):

```go
// getSandboxID returns the sandboxID to use when calling Agent API.
// Prefers status.sandboxID if set, otherwise falls back to name (legacy).
func (r *SandboxReconciler) getSandboxID(sandbox *apiv1alpha1.Sandbox) string {
	if sandbox.Status.SandboxID != "" {
		return sandbox.Status.SandboxID
	}
	// Legacy fallback for CRDs created before sandboxID was set
	return sandbox.Name
}
```

**Step 2: Update handleActiveDeletion to use helper**

Find line 620 and replace:
```go
SandboxID:  sandbox.Name,
```
with:
```go
SandboxID:  r.getSandboxID(sandbox),
```

Find line 654 and replace:
```go
SandboxID: sandbox.Name,
```
with:
```go
SandboxID: r.getSandboxID(sandbox),
```

**Step 3: Run tests**

```bash
go test -v ./internal/controller/... -run TestSandboxReconciler
```

Expected: PASS

**Step 4: Commit**

```bash
git add internal/controller/sandbox_controller.go
git commit -m "refactor(controller): prefer status.sandboxID over name for Agent API calls"
```

---

## Task 6: Update FastPath Tests

**Files:**
- Modify: `internal/controller/fastpath/server_test.go`

**Step 1: Update test expectations**

Search for tests that expect `SandboxID: req.Sandbox.SandboxID` to equal `req.Sandbox.Name` and update them to expect the hash format.

For Fast mode tests:
- Expected sandboxID should be 32 character md5 hash

For Strong mode tests:
- Expected sandboxID should be the UID (mock or use actual)

**Step 2: Run tests**

```bash
go test -v ./internal/controller/fastpath/...
```

**Step 3: Commit**

```bash
git add internal/controller/fastpath/server_test.go
git commit -m "test(fastpath): update tests for new sandboxID generation"
```

---

## Task 7: Update Agent Client Tests

**Files:**
- Modify: `internal/api/agent_client_test.go`

**Step 1: Update test sandboxID values**

Tests currently use simple names like "test-sb", "test-sb-123". These should be updated to use hash format for realistic testing, or document that the API accepts any string format.

Since the API accepts any string, tests can continue using simple strings. Add a comment:

```go
// Note: SandboxID can be any string format (md5 hash, UID, or legacy name)
// Tests use simple strings for readability
```

**Step 2: Run tests**

```bash
go test -v ./internal/api/...
```

**Step 3: Commit (if changes made)**

```bash
git add internal/api/agent_client_test.go
git commit -m "test(api): add note about sandboxID format flexibility"
```

---

## Verification Plan

### Unit Tests
```bash
go test -v ./pkg/util/idgen/...
go test -v ./internal/controller/fastpath/...
go test -v ./internal/controller/...
go test -v ./internal/api/...
```

### Integration Tests
```bash
# Start local kind cluster with Agent Pods
# Run FastPath create/delete operations
# Verify:
#  1. Fast mode creates container with md5 hash name
#  2. Strong mode creates container with UID name
#  3. status.sandboxID is correctly populated
#  4. CLI operations still work (using name)
```

### Manual Verification
```bash
# Fast mode
fsb-ctl create test-sb --pool my-pool --mode fast
# Check container name on agent pod (should be 32-char md5)
kubectl exec -it <agent-pod> -- ctr c ls | grep test-sb

# Strong mode
fsb-ctl create test-sb2 --pool my-pool --mode strong
# Check container name (should be 36-char UID)
kubectl exec -it <agent-pod> -- ctr c ls | grep test-sb2

# Verify CLI still works
fsb-ctl get test-sb  # Uses name, internally finds sandboxID
fsb-ctl delete test-sb
```

---

## Rollback Plan

If issues occur:
```bash
git revert <commit-hash>  # Revert in reverse order
git revert <commit-hash>
git revert <commit-hash>
```

Key commits to revert (in order):
1. Controller changes
2. FastPath Server changes
3. Annotation constant
4. ID generation utility

---

## Notes

- Container names will change from user-friendly to hash/UID
- This affects log file paths: `/var/log/fast-sandbox/{sandboxID}.log`
- Agent metrics may need updates if they use sandbox name as label
- Existing CRDs without `status.sandboxID` will fallback to using `name`
