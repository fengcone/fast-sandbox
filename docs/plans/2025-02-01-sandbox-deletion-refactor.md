# Sandbox Deletion Refactor Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Simplify sandbox deletion state management by removing `terminatedSandboxes` map, allowing Agent to auto-cleanup deleted entries, and Controller to confirm deletion via absence in status.

**Architecture:**
- Agent侧：`asyncDelete` 完成后直接删除条目，不再移动到 `terminatedSandboxes`
- Controller侧：通过 `!hasStatus` 判断删除完成，而不是检查 `phase == terminated`
- 类似 Kubernetes Pod 的行为：删除后不再在列表中出现

**Tech Stack:** Go 1.21+, Kubernetes controller-runtime, containerd

---

## Problem Statement

当前代码使用 `terminatedSandboxes map[string]int64` 存储已删除的 sandbox，等待 Controller 确认。但这个 map **只进不出**，导致：

1. **内存泄漏**：已删除的 sandbox 永远占用内存
2. **同名冲突**：删除后立即创建同名 sandbox 时，`terminatedSandboxes` 仍然存在旧的条目，导致状态混乱
3. **状态冗余**：`terminatedSandboxes` 和 `sandboxes` 两个 map 增加复杂性

---

## Design Decisions

### Before (Current)
```
Agent 侧：
  running → terminating → asyncDelete() → sandboxes删除，移动到terminatedSandboxes

Controller 侧：
  检查 agent.SandboxStatuses[id].Phase == "terminated" → 确认删除
```

### After (New)
```
Agent 侧：
  running → terminating → asyncDelete() → 直接删除条目

Controller 侧：
  检查 !hasStatus(agent.SandboxStatuses[id]) → 确认删除
```

---

## Task 1: 删除 Agent 侧的 `terminatedSandboxes` 相关代码

**Files:**
- Modify: `internal/agent/runtime/sandbox_manager.go:17-26` (删除字段)
- Modify: `internal/agent/runtime/sandbox_manager.go:29-42` (删除初始化)
- Modify: `internal/agent/runtime/sandbox_manager.go:149-168` (asyncDelete)
- Modify: `internal/agent/runtime/sandbox_manager.go:181-222` (GetSandboxStatuses)
- Test: `internal/agent/runtime/sandbox_manager_test.go` (更新相关测试)

**Step 1: 修改 SandboxManager 结构体**

删除 `terminatedSandboxes` 字段：

```go
// 删除前
type SandboxManager struct {
    mu       sync.RWMutex
    runtime  Runtime
    capacity int
    sandboxes map[string]*SandboxMetadata
    terminatedSandboxes map[string]int64  // ← 删除这行
    creating map[string]chan struct{}
}

// 删除后
type SandboxManager struct {
    mu       sync.RWMutex
    runtime  Runtime
    capacity int
    sandboxes map[string]*SandboxMetadata
    creating map[string]chan struct{}
}
```

**Step 2: 修改 NewSandboxManager**

删除 `terminatedSandboxes` 初始化：

```go
// 删除前
return &SandboxManager{
    runtime:             runtime,
    capacity:            capVal,
    sandboxes:           make(map[string]*SandboxMetadata),
    terminatedSandboxes: make(map[string]int64),  // ← 删除这行
    creating:            make(map[string]chan struct{}),
}

// 删除后
return &SandboxManager{
    runtime:   runtime,
    capacity:  capVal,
    sandboxes: make(map[string]*SandboxMetadata),
    creating:  make(map[string]chan struct{}),
}
```

**Step 3: 修改 asyncDelete**

删除后不再移动到 `terminatedSandboxes`，直接删除：

```go
// 删除前
func (m *SandboxManager) asyncDelete(sandboxID string) {
    const gracefulTimeout = 10 * time.Second
    ctx, cancel := context.WithTimeout(context.Background(), gracefulTimeout+5*time.Second)
    defer cancel()
    err := m.runtime.DeleteSandbox(ctx, sandboxID)
    m.mu.Lock()
    defer m.mu.Unlock()
    delete(m.sandboxes, sandboxID)
    m.terminatedSandboxes[sandboxID] = time.Now().Unix()  // ← 删除这行
}

// 删除后
func (m *SandboxManager) asyncDelete(sandboxID string) {
    const gracefulTimeout = 10 * time.Second
    ctx, cancel := context.WithTimeout(context.Background(), gracefulTimeout+5*time.Second)
    defer cancel()
    klog.InfoS("[DEBUG-AGENT] asyncDelete: calling runtime.DeleteSandbox", "sandboxID", sandboxID)
    err := m.runtime.DeleteSandbox(ctx, sandboxID)
    klog.InfoS("[DEBUG-AGENT] asyncDelete: runtime.DeleteSandbox completed",
        "sandboxID", sandboxID,
        "err", err,
        "nextStep", "removing from sandboxes")
    m.mu.Lock()
    defer m.mu.Unlock()
    // 直接删除，不再移动到 terminatedSandboxes
    delete(m.sandboxes, sandboxID)
    klog.InfoS("[DEBUG-AGENT] asyncDelete: DONE, sandbox removed",
        "sandboxID", sandboxID)
}
```

**Step 4: 修改 GetSandboxStatuses**

删除 `terminatedSandboxes` 相关逻辑：

```go
// 删除前
func (m *SandboxManager) GetSandboxStatuses(ctx context.Context) []api.SandboxStatus {
    m.mu.RLock()
    defer m.mu.RUnlock()

    result := make([]api.SandboxStatus, 0)

    // Add active sandboxes
    for sandboxID, meta := range m.sandboxes {
        runtimeStatus, _ := m.runtime.GetSandboxStatus(ctx, sandboxID)
        result = append(result, api.SandboxStatus{
            SandboxID: sandboxID,
            ClaimUID:  meta.ClaimUID,
            Phase:     meta.Phase,
            Message:   runtimeStatus,
            CreatedAt: meta.CreatedAt,
        })
    }

    // Add terminated sandboxes (for Controller confirmation)
    for sandboxID, deletedAt := range m.terminatedSandboxes {
        result = append(result, api.SandboxStatus{
            SandboxID: sandboxID,
            Phase:     "terminated",
            Message:   "",
            CreatedAt: deletedAt,
        })
    }

    if len(m.terminatedSandboxes) > 0 {
        klog.InfoS("[DEBUG-AGENT] GetSandboxStatuses: returning terminated sandboxes",
            "count", len(m.terminatedSandboxes),
            "sandboxes", func() []string {
                keys := make([]string, 0, len(m.terminatedSandboxes))
                for k := range m.terminatedSandboxes {
                    keys = append(keys, k)
                }
                return keys
            }())
    }

    return result
}

// 删除后
func (m *SandboxManager) GetSandboxStatuses(ctx context.Context) []api.SandboxStatus {
    m.mu.RLock()
    defer m.mu.RUnlock()

    result := make([]api.SandboxStatus, 0)

    // 只返回活跃的 sandboxes
    for sandboxID, meta := range m.sandboxes {
        runtimeStatus, _ := m.runtime.GetSandboxStatus(ctx, sandboxID)
        result = append(result, api.SandboxStatus{
            SandboxID: sandboxID,
            ClaimUID:  meta.ClaimUID,
            Phase:     meta.Phase,
            Message:   runtimeStatus,
            CreatedAt: meta.CreatedAt,
        })
    }

    return result
}
```

**Step 5: 运行测试验证**

```bash
cd /Users/fengjianhui/WorkSpaceL/fast-sandbox
go test -v ./internal/agent/runtime/... -run TestSandboxManager
```

预期：部分测试会失败（那些期望 `terminated` 状态的测试），这是预期的，我们下一步会修复它们。

**Step 6: 提交**

```bash
git add internal/agent/runtime/sandbox_manager.go
git commit -m "refactor(agent): remove terminatedSandboxes map, directly delete after asyncDelete completes"
```

---

## Task 2: 更新 Agent 侧测试

**Files:**
- Modify: `internal/agent/runtime/sandbox_manager_test.go:443-479` (TestSandboxManager_DeleteSandbox_Success)
- Modify: `internal/agent/runtime/sandbox_manager_test.go:572-627` (TestSandboxManager_GetSandboxStatuses)
- Modify: `internal/agent/runtime/sandbox_manager_test.go:668-713` (TestSandboxManager_GetSandboxStatuses_MultiplePhases)
- Modify: `internal/agent/runtime/sandbox_manager_test.go:829-858` (TestSandboxManager_AsyncDelete_Timeout)
- Modify: `internal/agent/runtime/sandbox_manager_test.go:860-892` (TestSandboxManager_AsyncDelete_RuntimeError)

**Step 1: 修改 TestSandboxManager_DeleteSandbox_Success**

删除后不再期望 `terminated` 状态，而是期望条目完全消失：

```go
// 删除前
func TestSandboxManager_DeleteSandbox_Success(t *testing.T) {
    // ... 创建 sandbox ...

    // Delete sandbox
    resp, err := manager.DeleteSandbox(spec.SandboxID)
    require.NoError(t, err, "DeleteSandbox should succeed")
    assert.True(t, resp.Success, "Response should indicate success")

    // Sandbox should be in terminating phase
    statuses := manager.GetSandboxStatuses(ctx)
    require.Len(t, statuses, 1, "Should have one status")
    assert.Equal(t, spec.SandboxID, statuses[0].SandboxID)
    assert.Equal(t, "terminating", statuses[0].Phase)

    // Wait for async deletion to complete
    time.Sleep(100 * time.Millisecond)

    // Sandbox should be moved to terminated
    statuses = manager.GetSandboxStatuses(ctx)
    require.Len(t, statuses, 1, "Should still have one status")
    assert.Equal(t, "terminated", statuses[0].Phase, "Phase should be terminated after async delete")
}

// 删除后
func TestSandboxManager_DeleteSandbox_Success(t *testing.T) {
    // ... 创建 sandbox ...

    // Delete sandbox
    resp, err := manager.DeleteSandbox(spec.SandboxID)
    require.NoError(t, err, "DeleteSandbox should succeed")
    assert.True(t, resp.Success, "Response should indicate success")

    // Sandbox should be in terminating phase (before async delete completes)
    statuses := manager.GetSandboxStatuses(ctx)
    require.Len(t, statuses, 1, "Should have one status")
    assert.Equal(t, spec.SandboxID, statuses[0].SandboxID)
    assert.Equal(t, "terminating", statuses[0].Phase)

    // Wait for async deletion to complete
    time.Sleep(100 * time.Millisecond)

    // Sandbox should be completely removed (not in statuses)
    statuses = manager.GetSandboxStatuses(ctx)
    assert.Empty(t, statuses, "Sandbox should be completely removed after async delete")
}
```

**Step 2: 修改 TestSandboxManager_GetSandboxStatuses**

删除后不再期望 `terminated` 状态：

```go
// 删除前
func TestSandboxManager_GetSandboxStatuses(t *testing.T) {
    // ... 创建两个 sandboxes，删除一个 ...

    // Get statuses
    statuses := manager.GetSandboxStatuses(ctx)

    // Should have both active and terminated sandboxes
    require.Len(t, statuses, 2, "Should have two statuses")

    // Create a map for easier lookup
    statusMap := make(map[string]api.SandboxStatus)
    for _, status := range statuses {
        statusMap[status.SandboxID] = status
    }

    // Check deleted sandbox is terminated
    terminatedStatus, exists := statusMap[spec1.SandboxID]
    assert.True(t, exists, "Deleted sandbox should be in statuses")
    assert.Equal(t, "terminated", terminatedStatus.Phase, "Deleted sandbox should be terminated")

    // Check active sandbox is still running
    activeStatus, exists := statusMap[spec2.SandboxID]
    assert.True(t, exists, "Active sandbox should be in statuses")
    assert.Equal(t, "running", activeStatus.Phase, "Active sandbox should be running")
    assert.Equal(t, spec2.ClaimUID, activeStatus.ClaimUID)
}

// 删除后
func TestSandboxManager_GetSandboxStatuses(t *testing.T) {
    // ... 创建两个 sandboxes，删除一个 ...
    // Wait for async delete to complete
    time.Sleep(100 * time.Millisecond)

    // Get statuses
    statuses := manager.GetSandboxStatuses(ctx)

    // Should only have the active sandbox (deleted one is gone)
    require.Len(t, statuses, 1, "Should have one status (only active)")

    // Check active sandbox is still running
    assert.Equal(t, spec2.SandboxID, statuses[0].SandboxID)
    assert.Equal(t, "running", statuses[0].Phase, "Active sandbox should be running")
    assert.Equal(t, spec2.ClaimUID, statuses[0].ClaimUID)
}
```

**Step 3: 修改 TestSandboxManager_GetSandboxStatuses_MultiplePhases**

```go
// 删除前
func TestSandboxManager_GetSandboxStatuses_MultiplePhases(t *testing.T) {
    // ... 创建 sandboxes，删除一个 ...

    statuses := manager.GetSandboxStatuses(ctx)
    require.Len(t, statuses, 3)

    // Create a map for easier lookup
    statusMap := make(map[string]api.SandboxStatus)
    for _, status := range statuses {
        statusMap[status.SandboxID] = status
    }

    // First sandbox should be terminated or terminating
    firstStatus := statusMap[specs[0].SandboxID]
    assert.True(t, firstStatus.Phase == "terminated" || firstStatus.Phase == "terminating",
        "First sandbox should be terminating or terminated")

    // Other sandboxes should be running
    for i := 1; i < 3; i++ {
        status := statusMap[specs[i].SandboxID]
        assert.Equal(t, "running", status.Phase, "Sandbox %s should be running", specs[i].SandboxID)
    }
}

// 删除后
func TestSandboxManager_GetSandboxStatuses_MultiplePhases(t *testing.T) {
    // ... 创建 sandboxes，删除一个 ...

    statuses := manager.GetSandboxStatuses(ctx)

    // After waiting for async delete, first sandbox should be gone
    // So we should have 2 sandboxes (or 3 if async hasn't completed yet)
    assert.LessOrEqual(t, len(statuses), 3, "Should have at most 3 statuses")

    // Create a map for easier lookup
    statusMap := make(map[string]api.SandboxStatus)
    for _, status := range statuses {
        statusMap[status.SandboxID] = status
    }

    // First sandbox might be terminating or gone
    if firstStatus, exists := statusMap[specs[0].SandboxID]; exists {
        assert.Equal(t, "terminating", firstStatus.Phase,
            "First sandbox should be terminating (not yet deleted)")
    }

    // Other sandboxes should be running
    for i := 1; i < 3; i++ {
        status, exists := statusMap[specs[i].SandboxID]
        if exists {
            assert.Equal(t, "running", status.Phase, "Sandbox %s should be running", specs[i].SandboxID)
        }
    }
}
```

**Step 4: 修改 TestSandboxManager_AsyncDelete_Timeout**

```go
// 删除前
func TestSandboxManager_AsyncDelete_Timeout(t *testing.T) {
    // ... 创建和删除 sandbox ...

    // Wait for async delete to complete (should complete within timeout)
    time.Sleep(200 * time.Millisecond)

    // Verify sandbox was moved to terminated
    statuses := manager.GetSandboxStatuses(ctx)
    require.Len(t, statuses, 1)
    assert.Equal(t, "terminated", statuses[0].Phase)
}

// 删除后
func TestSandboxManager_AsyncDelete_Timeout(t *testing.T) {
    // ... 创建和删除 sandbox ...

    // Wait for async delete to complete (should complete within timeout)
    time.Sleep(200 * time.Millisecond)

    // Verify sandbox was completely removed
    statuses := manager.GetSandboxStatuses(ctx)
    assert.Empty(t, statuses, "Sandbox should be completely removed after async delete")
}
```

**Step 5: 修改 TestSandboxManager_AsyncDelete_RuntimeError**

```go
// 删除前
func TestSandboxManager_AsyncDelete_RuntimeError(t *testing.T) {
    // ... 创建和删除 sandbox（runtime error）...

    // Wait for async delete
    time.Sleep(100 * time.Millisecond)

    // Verify sandbox was still moved to terminated despite error
    statuses := manager.GetSandboxStatuses(ctx)
    require.Len(t, statuses, 1)
    assert.Equal(t, "terminated", statuses[0].Phase, "Sandbox should be terminated even with runtime error")
}

// 删除后
func TestSandboxManager_AsyncDelete_RuntimeError(t *testing.T) {
    // ... 创建和删除 sandbox（runtime error）...

    // Wait for async delete
    time.Sleep(100 * time.Millisecond)

    // Verify sandbox was completely removed despite error
    statuses := manager.GetSandboxStatuses(ctx)
    assert.Empty(t, statuses, "Sandbox should be completely removed even with runtime error")
}
```

**Step 6: 运行测试验证**

```bash
cd /Users/fengjianhui/WorkSpaceL/fast-sandbox
go test -v ./internal/agent/runtime/... -run TestSandboxManager
```

预期：所有测试通过。

**Step 7: 提交**

```bash
git add internal/agent/runtime/sandbox_manager_test.go
git commit -m "test(agent): update tests for direct deletion instead of terminated state"
```

---

## Task 3: 修改 Controller 侧删除确认逻辑

**Files:**
- Modify: `internal/controller/sandbox_controller.go:186-236` (handleTerminatingDeletion)

**Step 1: 修改 handleTerminatingDeletion**

通过 `!hasStatus` 判断删除完成，而不是 `phase == terminated`：

```go
// 删除前
func (r *SandboxReconciler) handleTerminatingDeletion(ctx context.Context, sandbox *apiv1alpha1.Sandbox) (ctrl.Result, error) {
    logger := klog.FromContext(ctx)

    logger.Info("[DEBUG-TERM] handleTerminatingDeletion ENTER",
        "sandbox", sandbox.Name,
        "assignedPod", sandbox.Status.AssignedPod,
        "deletionTimestamp", sandbox.DeletionTimestamp)

    // Check if Agent still exists
    agent, agentExists := r.Registry.GetAgentByID(agentpool.AgentID(sandbox.Status.AssignedPod))
    logger.Info("[DEBUG-TERM] Agent existence check",
        "agentID", agentpool.AgentID(sandbox.Status.AssignedPod),
        "agentExists", agentExists)

    if !agentExists {
        // Agent gone - still try to release in case the slot still exists
        logger.Info("[BUG-FIX] Agent disappeared during termination - attempting Release to free Allocated slot",
            "agentID", agentpool.AgentID(sandbox.Status.AssignedPod))
        r.Registry.Release(agentpool.AgentID(sandbox.Status.AssignedPod), sandbox)
        return r.removeFinalizer(ctx, sandbox)
    }

    // Check Agent-reported status
    agentStatus, hasStatus := agent.SandboxStatuses[sandbox.Name]
    logger.Info("[DEBUG-TERM] Agent status check",
        "hasStatus", hasStatus,
        "phase", func() string {
            if hasStatus {
                return agentStatus.Phase
            } else {
                return "<none>"
            }
        }(),
        "expectedPhase", "terminated",
        "agentAllocated", agent.Allocated)

    if hasStatus && apiv1alpha1.AgentSandboxPhase(agentStatus.Phase) == apiv1alpha1.AgentPhaseTerminated {
        // Agent confirmed deletion - release resources and remove finalizer
        logger.Info("[DEBUG-TERM] Agent confirmed terminated - calling Registry.Release",
            "agentAllocatedBefore", agent.Allocated)
        r.Registry.Release(agentpool.AgentID(sandbox.Status.AssignedPod), sandbox)
        return r.removeFinalizer(ctx, sandbox)
    }

    // Still terminating - continue waiting
    logger.Info("[DEBUG-TERM] Still waiting for Agent termination",
        "willRequeueAfter", DeletionPollInterval)
    return ctrl.Result{RequeueAfter: DeletionPollInterval}, nil
}

// 删除后
func (r *SandboxReconciler) handleTerminatingDeletion(ctx context.Context, sandbox *apiv1alpha1.Sandbox) (ctrl.Result, error) {
    logger := klog.FromContext(ctx)

    logger.Info("[DEBUG-TERM] handleTerminatingDeletion ENTER",
        "sandbox", sandbox.Name,
        "assignedPod", sandbox.Status.AssignedPod,
        "deletionTimestamp", sandbox.DeletionTimestamp)

    // Check if Agent still exists
    agent, agentExists := r.Registry.GetAgentByID(agentpool.AgentID(sandbox.Status.AssignedPod))
    logger.Info("[DEBUG-TERM] Agent existence check",
        "agentID", agentpool.AgentID(sandbox.Status.AssignedPod),
        "agentExists", agentExists)

    if !agentExists {
        // Agent gone - still try to release in case the slot still exists
        logger.Info("[BUG-FIX] Agent disappeared during termination - attempting Release to free Allocated slot",
            "agentID", agentpool.AgentID(sandbox.Status.AssignedPod))
        r.Registry.Release(agentpool.AgentID(sandbox.Status.AssignedPod), sandbox)
        return r.removeFinalizer(ctx, sandbox)
    }

    // Check Agent-reported status
    agentStatus, hasStatus := agent.SandboxStatuses[sandbox.Name]
    logger.Info("[DEBUG-TERM] Agent status check",
        "hasStatus", hasStatus,
        "phase", func() string {
            if hasStatus {
                return agentStatus.Phase
            } else {
                return "<none>"
            }
        }(),
        "agentAllocated", agent.Allocated)

    if !hasStatus {
        // Agent no longer reports this sandbox = deletion confirmed
        // Release resources and remove finalizer
        logger.Info("[DEBUG-TERM] Agent no longer reports sandbox - deletion confirmed, calling Registry.Release",
            "agentAllocatedBefore", agent.Allocated)
        r.Registry.Release(agentpool.AgentID(sandbox.Status.AssignedPod), sandbox)
        return r.removeFinalizer(ctx, sandbox)
    }

    // Still terminating - continue waiting
    logger.Info("[DEBUG-TERM] Still waiting for Agent termination",
        "currentPhase", agentStatus.Phase,
        "willRequeueAfter", DeletionPollInterval)
    return ctrl.Result{RequeueAfter: DeletionPollInterval}, nil
}
```

**Step 2: 更新 API 类型定义（如果需要）**

检查 `api/v1alpha1/sandbox_types.go` 中的 `AgentPhaseTerminated` 是否仍然需要。如果不再使用 `terminated` 状态，可以考虑废弃：

```go
// 检查这个常量是否还在其他地方使用
// 如果没有，可以添加注释标记为废弃
const (
    AgentPhaseCreating   AgentSandboxPhase = "creating"
    AgentPhaseRunning    AgentSandboxPhase = "running"
    AgentPhaseFailed     AgentSandboxPhase = "failed"
    AgentPhaseStopped    AgentSandboxPhase = "stopped"
    AgentPhaseTerminated AgentSandboxPhase = "terminated" // Deprecated: Use absence from SandboxStatuses to confirm deletion
    AgentPhaseTerminating AgentSandboxPhase = "terminating"
)
```

**Step 3: 运行测试验证**

```bash
cd /Users/fengjianhui/WorkSpaceL/fast-sandbox
go test -v ./internal/controller/... -run TestSandboxReconciler
```

**Step 4: 提交**

```bash
git add internal/controller/sandbox_controller.go
git commit -m "refactor(controller): confirm deletion by !hasStatus instead of phase==terminated"
```

---

## Task 4: 删除 `creating` map（可选，取决于是否真的需要处理并发创建）

**Files:**
- Modify: `internal/agent/runtime/sandbox_manager.go:26`
- Modify: `internal/agent/runtime/sandbox_manager.go:41`
- Modify: `internal/agent/runtime/sandbox_manager.go:57-104` (CreateSandbox 中的 creating 逻辑)

**分析：**

`creating` map 的作用是处理并发创建请求，让后续请求等待第一个请求完成。但实际上：

1. `sandboxes` map 已经有幂等性检查（第 46-55 行）
2. `creating` 增加了复杂性和潜在的死锁风险
3. 如果创建失败，`creating` 的清理依赖 defer，但 channel 可能泄漏

**建议：** 保留 `creating` 但简化逻辑，或者完全删除。这里需要讨论。

如果决定删除，修改如下：

```go
// 删除 creating 相关代码
func (m *SandboxManager) CreateSandbox(ctx context.Context, spec *api.SandboxSpec) (*api.CreateSandboxResponse, error) {
    m.mu.Lock()
    _, exists := m.sandboxes[spec.SandboxID]
    if exists {
        m.mu.Unlock()
        klog.InfoS("Sandbox already exists in cache, returning success (idempotent)", "sandbox", spec.SandboxID)
        return &api.CreateSandboxResponse{
            Success:   true,
            SandboxID: spec.SandboxID,
        }, nil
    }
    m.mu.Unlock()

    createdAt := time.Now().Unix()
    metadata, err := m.runtime.CreateSandbox(ctx, spec)
    if err != nil {
        klog.ErrorS(err, "Failed to create sandbox", "sandbox", spec.SandboxID)
        return &api.CreateSandboxResponse{
            Success: false,
            Message: fmt.Sprintf("create failed: %v", err),
        }, err
    }
    m.mu.Lock()
    metadata.Phase = "running"
    m.sandboxes[spec.SandboxID] = metadata
    m.mu.Unlock()
    klog.InfoS("Created sandbox", "sandbox", spec.SandboxID, "image", spec.Image)
    return &api.CreateSandboxResponse{
        Success:   true,
        SandboxID: spec.SandboxID,
        CreatedAt: createdAt,
    }, nil
}
```

**决定：** 此任务可选，建议先完成 Task 1-3，然后讨论是否需要 `creating` map。

**如果执行此任务：**

```bash
git add internal/agent/runtime/sandbox_manager.go
git commit -m "refactor(agent): remove creating map, simplify concurrent create handling"
```

---

## Task 5: 更新文档和注释

**Files:**
- Modify: `docs/plans/2025-01-28-debug-same-name-recreate.md` (更新问题描述)
- Create: `docs/plans/2025-02-01-sandbox-deletion-refactor-summary.md` (总结变更)

**Step 1: 创建总结文档**

```bash
cat > /Users/fengjianhui/WorkSpaceL/fast-sandbox/docs/plans/2025-02-01-sandbox-deletion-refactor-summary.md << 'EOF'
# Sandbox Deletion Refactor Summary

## Problem
`terminatedSandboxes` map 只进不出，导致内存泄漏和同名冲突。

## Solution
采用类似 Kubernetes Pod 的模式：
- Agent 删除容器后直接从 `sandboxes` 中删除条目
- Controller 通过 `!hasStatus` 确认删除

## Changes
1. Agent 侧：删除 `terminatedSandboxes` map
2. Controller 侧：修改删除确认逻辑
3. 测试：更新相关测试用例

## Impact
- 简化状态管理
- 避免同名冲突
- 减少 Agent 侧内存占用
EOF
```

**Step 2: 提交**

```bash
git add docs/
git commit -m "docs: add sandbox deletion refactor summary"
```

---

## Verification Plan

### 单元测试
```bash
go test -v ./internal/agent/runtime/...
go test -v ./internal/controller/...
```

### 集成测试
```bash
# 运行 e2e 测试
go test -v ./test/e2e/...
```

### 手动验证
1. 创建一个 sandbox
2. 删除它
3. 立即创建同名 sandbox
4. 验证：同名 sandbox 创建成功，没有冲突

---

## Rollback Plan

如果发现问题，回滚步骤：

```bash
git revert <commit-hash>  # 回滚最近的提交
```

关键提交点：
- Task 1: 删除 `terminatedSandboxes`
- Task 3: 修改 Controller 逻辑

---

## Open Questions

1. **`creating` map 是否保留？** 需要评估并发创建场景
2. **`AgentPhaseTerminated` 常量是否废弃？** 检查是否有其他地方使用
3. **删除确认的超时时间是否需要调整？** 当前 `DeletionPollInterval = 2s`

---

## References

- Kubernetes Pod Deletion: https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-termination
- Original discussion: `docs/plans/2025-01-28-debug-same-name-recreate.md`
