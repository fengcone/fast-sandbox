# Bug 复现计划：同名 Sandbox 重建失败

> **目标**: 通过日志定位 `fsb-ctl run → delete → run` 同名重建失败的根因

## 复现场景

```bash
# 1. 创建 poolMax=1, poolMin=1 的 pool
kubectl apply -f - <<EOF
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: test-pool
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "fast-sandbox/agent:dev" }]
EOF

# 2. 创建 sandbox
fsb-ctl run fsb-test --image alpine --pool test-pool /bin/sleep 3600

# 3. 删除 sandbox
fsb-ctl delete fsb-test

# 4. 立即重建同名 sandbox（预期失败）
fsb-ctl run fsb-test --image alpine --pool test-pool /bin/sleep 3600
```

## 需要添加的调试日志

### 1. Controller 侧 - `sandbox_controller.go`

在 `handleTerminatingDeletion` 中添加详细日志：

```go
func (r *SandboxReconciler) handleTerminatingDeletion(...) {
    logger.Info("[DEBUG] handleTerminatingDeletion ENTER",
        "sandbox", sandbox.Name,
        "assignedPod", sandbox.Status.AssignedPod)

    agent, agentExists := r.Registry.GetAgentByID(agentpool.AgentID(sandbox.Status.AssignedPod))
    logger.Info("[DEBUG] Agent check",
        "agentExists", agentExists,
        "sandboxInStatuses", agent.SandboxStatuses[sandbox.Name])

    if !agentExists {
        logger.Info("[BUG] Agent disappeared during termination - WILL NOT RELEASE REGISTRY")
        return r.removeFinalizer(ctx, sandbox)
    }

    agentStatus, hasStatus := agent.SandboxStatuses[sandbox.Name]
    logger.Info("[DEBUG] Agent status check",
        "hasStatus", hasStatus,
        "phase", agentStatus.Phase,
        "expectedPhase", "terminated")

    if hasStatus && apiv1alpha1.AgentSandboxPhase(agentStatus.Phase) == apiv1alpha1.AgentPhaseTerminated {
        logger.Info("[DEBUG] Agent confirmed terminated - calling Release")
        r.Registry.Release(agentpool.AgentID(sandbox.Status.AssignedPod), sandbox)
        return r.removeFinalizer(ctx, sandbox)
    }

    logger.Info("[DEBUG] Still waiting for termination")
    return ctrl.Result{RequeueAfter: DeletionPollInterval}, nil
}
```

### 2. Registry 侧 - `registry.go`

在 `Release` 中添加日志：

```go
func (r *InMemoryRegistry) Release(id AgentID, sb *apiv1alpha1.Sandbox) {
    klog.Info("[DEBUG] Registry.Release ENTER",
        "agentID", id,
        "sandbox", sb.Name,
        "ports", sb.Spec.ExposedPorts)

    r.mu.RLock()
    slot, ok := r.agents[id]
    r.mu.RUnlock()

    if !ok {
        klog.Warning("[DEBUG] Release: slot not found for agent", "agentID", id)
        return
    }

    slot.mu.Lock()
    defer slot.mu.Unlock()

    klog.Info("[DEBUG] Release: before release",
        "allocated", slot.info.Allocated,
        "sandboxInStatuses", slot.info.SandboxStatuses[sb.Name])

    // ... release logic

    klog.Info("[DEBUG] Release: after release",
        "allocated", slot.info.Allocated)
}
```

### 3. Agent 侧 - `sandbox_manager.go`

在 `DeleteSandbox` 和 `asyncDelete` 中添加日志：

```go
func (m *SandboxManager) DeleteSandbox(sandboxID string) {
    klog.Info("[DEBUG-AGENT] DeleteSandbox ENTER", "sandboxID", sandboxID)
    // ... existing code
    klog.Info("[DEBUG-AGENT] DeleteSandbox: marked terminating, starting asyncDelete")
}

func (m *SandboxManager) asyncDelete(sandboxID string) {
    klog.Info("[DEBUG-AGENT] asyncDelete ENTER", "sandboxID", sandboxID)
    // ... delete logic
    klog.Info("[DEBUG-AGENT] asyncDelete: moving to terminatedSandboxes", "sandboxID", sandboxID)
}
```

## 验证步骤

### Step 1: 添加调试日志

修改以下文件：
- `internal/controller/sandbox_controller.go`
- `internal/controller/agentpool/registry.go`
- `internal/agent/runtime/sandbox_manager.go`

### Step 2: 编译并加载镜像

```bash
# 编译 controller
make build-controller-linux

# 编译 agent
make build-agent-linux

# 加载到 KIND
kind load docker-image fast-sandbox/controller:dev
kind load docker-image fast-sandbox/agent:dev

# 重启 pods
kubectl rollout restart deployment fast-sandbox-controller -n fast-sandbox-system
kubectl delete pod -l app=sandbox-agent -n <namespace>
```

### Step 3: 执行复现

```bash
# 运行测试脚本
bash test/e2e/03-lifecycle/basic-lifecycle.sh
```

### Step 4: 收集日志

```bash
# Controller 日志
kubectl logs -l app=fast-sandbox-controller -n fast-sandbox-system --tail=200 > controller.log

# Agent 日志
kubectl logs -l app=sandbox-agent -n <namespace> --tail=200 > agent.log
```

## 预期结果分析

### 场景 A: Agent 短暂消失导致没释放

```
[DEBUG] handleTerminatingDeletion ENTER
[DEBUG] Agent check agentExists=false
[BUG] Agent disappeared during termination - WILL NOT RELEASE REGISTRY
```
→ **根因**: Agent 心跳超时，Controller 认为 Agent 消失，直接移除 finalizer 但没调用 Release

### 场景 B: terminated 状态没正确同步

```
[DEBUG] handleTerminatingDeletion ENTER
[DEBUG] Agent check agentExists=true
[DEBUG] Agent status check hasStatus=true, phase=terminating
[DEBUG] Still waiting for termination
... (重复多次)
```
→ **根因**: Agent 状态卡在 terminating，从未变成 terminated

### 场景 C: Release 被调用但 Allocated 没减少

```
[DEBUG] Agent status check hasStatus=true, phase=terminated
[DEBUG] Agent confirmed terminated - calling Release
[DEBUG] Registry.Release ENTER
[DEBUG] Release: before release allocated=1
[DEBUG] Release: after release allocated=1  ← 没变化！
```
→ **根因**: Release 逻辑有 bug

## 下一步

根据日志结果，确定修复方案：
- 场景 A: 在 Agent 消失分支也调用 Release
- 场景 B: 修复 Agent 状态转换或心跳逻辑
- 场景 C: 修复 Release 逻辑
