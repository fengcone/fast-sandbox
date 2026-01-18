# 沙箱状态自愈与受控恢复设计方案

## 1. 背景
在 AI Agent 或长期运行的计算任务中，盲目的自动重调度会导致：
1. **非幂等性问题**: 任务重复执行导致数据损坏。
2. **状态丢失**: 内存与临时文件在重调度后无法找回（直到 2.3 状态快照实现）。
3. **网络抖动误判**: 频繁的重调度造成系统震荡。

本方案旨在提供一种“用户可控、策略驱动”的恢复机制。

## 2. 核心字段定义

### 2.1 Sandbox Spec
- **`failurePolicy`**: 
  - `Manual` (默认): 发现 Agent 失联仅在 Status 中告警，不自动动作。
  - `AutoRecreate`: 失联超过阈值后自动重调度。
- **`recoveryTimeoutSeconds`**: 判定 Agent 彻底失效的观察期（默认 60s）。
- **`resetRevision`**: (`metav1.Time`) 手动重置标记。用户通过更新此字段触发沙箱重启。

### 2.2 Sandbox Status
- **`acceptedResetRevision`**: 标识控制器已受理的重置请求版本。
- **`conditions`**: 包含 `AgentReady` 状态，描述与宿主机的实时连通性。

## 3. 控制器逻辑

### 3.1 连通性监控
`SandboxController` 每次 Reconcile 时检查关联 Agent 的 `LastSeen` 时间：
- `dt = now - LastSeen`
- 若 `dt > 10s`: 设置 `Condition: AgentReady = False, Reason: HeartbeatTimeout`。
- 若 `dt > RecoveryTimeoutSeconds` 且 `Policy == AutoRecreate`: 触发重调度流程。

### 3.2 重置触发逻辑
满足以下任一条件，沙箱将清空 `assignedPod` 并重新进入 `Pending` 流程：
1. **手动触发**: `Spec.ResetRevision` > `Status.AcceptedResetRevision`。
2. **自动触发**: `AutoRecreate` 观察期满。

### 3.3 逻辑隔离 (Epoch Guard)
每次重置时，沙箱的 `ClaimUID` 保持不变，但调度后会产生新的 `SandboxID`。
Janitor 负责根据 Pod 的实际存在情况清理宿主机上的陈旧容器，确保新旧实例不会在物理层冲突。

### 3.4 联通性 Gap 与用户决策
由于 Controller 与 Agent 失联不代表用户与 Sandbox 失联，系统遵循以下准则：
1. **风险透明**: 失联时仅通过 `Condition: AgentReady = False` 告知用户风险，不主动中断业务。
2. **延迟容忍**: 自动重调度仅在 `RecoveryTimeoutSeconds` 后发生，以过滤网络波动。
3. **最终裁决**: 将“重置”定义为一等公民能力。对于要求强幂等性的任务，用户应使用 `Manual` 策略，并在业务侧确认安全后，通过更新 `Spec.ResetRevision` 发起受控恢复。

## 4. 实施计划
1. **Step 1**: 修改 `Sandbox` API 增加上述字段。
2. **Step 2**: 更新 `Registry` 维护 `LastSeen` 时间戳。
3. **Step 3**: 在 `SandboxController` 中实现双版本对比的重置逻辑。
4. **Step 4**: 实现基于时间的自动重调度逻辑。
