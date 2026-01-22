# Sandbox 生命周期状态机

本文档描述 Sandbox 资源在 Fast-Sandbox 系统中的完整生命周期状态流转。

## 概述

Sandbox 是 Fast-Sandbox 的核心资源，代表一个隔离的运行环境（容器）。其生命周期由 Controller 和 Agent 协同管理：

- **Controller 端**：负责调度、状态协调、过期管理
- **Agent 端**：负责实际的容器创建、运行、销毁

## 状态定义

### Controller 端状态 (Status.Phase)

| 状态 | 说明 | 进入条件 |
|------|------|----------|
| `Pending` | 已调度，等待 Agent 创建 | 新建/重调度时 |
| `Bound` | 已下发创建请求 | Agent 心跳正常，请求已发送 |
| `Running` | 容器正在运行 | Agent 上报 `running` |
| `Terminating` | 正在删除中 | 用户删除，等待 Agent 确认 |
| `Expired` | 已过期，资源已清理但 CRD 保留 | 超过 ExpireTime |
| `Failed` | 创建或运行失败 | Agent 上报 `failed` |

### Agent 端状态 (Agent 上报)

| 状态 | 说明 |
|------|------|
| `creating` | 正在创建容器 |
| `running` | 容器正在运行 |
| `stopped` | 容器已停止 |
| `failed` | 创建或运行失败 |
| `terminated` | 容器已删除并清理 |

## 状态机流转图

```mermaid
stateDiagram-v2
    [*] --> Pending: 创建 Sandbox CRD
    
    state "正常流程" as normal {
        Pending --> Bound: Agent 心跳正常<br/>发送创建请求
        Bound --> Running: Agent 上报 running
        Running --> Running: 状态同步
    }
    
    state "删除流程" as deletion {
        Running --> Terminating: 用户删除
        Bound --> Terminating: 用户删除
        Terminating --> [*]: Agent 确认 terminated
    }
    
    state "过期流程" as expiration {
        Running --> Expired: 超过 ExpireTime
        Bound --> Expired: 超过 ExpireTime
        Expired --> [*]: 用户删除 CRD
    }
    
    state "重置流程" as reset {
        Running --> Pending: ResetRevision 更新
        Bound --> Pending: ResetRevision 更新
    }
    
    state "故障恢复" as recovery {
        Running --> Pending: Agent 丢失 + AutoRecreate
        Bound --> Pending: Agent 丢失 + AutoRecreate
    }
    
    state "快速清理" as cleanup {
        Pending --> [*]: 删除 (无资源需清理)
        Failed --> [*]: 删除
    }
```

## 详细流程

### 1. 创建流程

```
用户创建 Sandbox CRD
        │
        ▼
┌───────────────────┐
│   添加 Finalizer  │
└─────────┬─────────┘
          │
          ▼
┌───────────────────┐
│   调度到 Agent    │ ◄── Registry.Allocate()
│   Phase: Pending  │     选择最优节点（镜像亲和/负载均衡）
└─────────┬─────────┘
          │
          ▼
┌───────────────────┐
│   创建容器请求    │ ◄── AgentClient.CreateSandbox()
│   Phase: Bound    │
└─────────┬─────────┘
          │
          ▼
┌───────────────────┐
│   容器运行中      │ ◄── Agent 上报 status
│   Phase: Running  │
└───────────────────┘
```

### 2. 删除流程

```
用户删除 Sandbox CRD (kubectl delete)
        │
        ▼
┌─────────────────────────────────────┐
│ DeletionTimestamp 被设置            │
│ Finalizer 阻止立即删除              │
└─────────────────┬───────────────────┘
                  │
    ┌─────────────┴─────────────┐
    │                           │
    ▼                           ▼
Phase=Expired?              Phase=Bound/Running?
    │                           │
    ▼                           ▼
┌─────────┐         ┌───────────────────────┐
│ 移除    │         │ 调用 Agent 删除       │
│Finalizer│         │ Phase → Terminating   │
└────┬────┘         └───────────┬───────────┘
     │                          │
     ▼                          ▼
   [删除]              ┌────────────────────┐
                       │ 等待 Agent 确认    │
                       │ status=terminated  │
                       └─────────┬──────────┘
                                 │
                                 ▼
                       ┌────────────────────┐
                       │ Registry.Release() │
                       │ 移除 Finalizer     │
                       └─────────┬──────────┘
                                 │
                                 ▼
                              [删除]
```

### 3. 过期流程

```
Reconcile 检查 ExpireTime
        │
        ▼
    已过期？ ─── 否 ──► 继续正常流程
        │              (如果 <30s 则调度检查)
       是
        │
        ▼
┌───────────────────────┐
│ 调用 Agent 删除容器   │ ◄── deleteFromAgent()
└─────────┬─────────────┘
          │
          ▼
┌───────────────────────┐
│ 释放 Registry 资源    │ ◄── Registry.Release()
└─────────┬─────────────┘
          │
          ▼
┌───────────────────────┐
│ Phase → Expired       │
│ AssignedPod 清空      │
│ CRD 保留（历史查询）  │
└───────────────────────┘
```

### 4. Reset 流程

Reset 允许用户手动触发 Sandbox 重新调度，常用于：
- Agent 故障恢复（Manual 模式）
- 强制迁移到其他节点
- 清理异常状态

```
用户更新 Spec.ResetRevision (时间戳递增)
        │
        ▼
┌────────────────────────────────────┐
│ 比较 ResetRevision > Accepted?    │
└─────────────────┬──────────────────┘
                  │
                 是
                  │
                  ▼
┌────────────────────────────────────┐
│ 调用 Agent 删除旧容器              │ ◄── 修复 BUG-03
└─────────────────┬──────────────────┘
                  │
                  ▼
┌────────────────────────────────────┐
│ Registry.Release() 释放资源        │
└─────────────────┬──────────────────┘
                  │
                  ▼
┌────────────────────────────────────┐
│ Phase → Pending                    │
│ AssignedPod 清空                   │
│ AcceptedResetRevision 更新         │
└─────────────────┬──────────────────┘
                  │
                  ▼
            [重新调度]
```

### 5. Agent 故障恢复

当 Agent Pod 被删除或心跳超时：

```
Agent 从 Registry 中消失
        │
        ▼
┌────────────────────────────────────┐
│ FailurePolicy = ?                  │
└─────────────────┬──────────────────┘
                  │
    ┌─────────────┴─────────────┐
    │                           │
    ▼                           ▼
AutoRecreate                  Manual
    │                           │
    ▼                           ▼
┌─────────────────┐    ┌─────────────────┐
│ Phase → Pending │    │ 保持当前状态    │
│ 自动重新调度    │    │ 等待用户 Reset  │
└─────────────────┘    └─────────────────┘
```

## 配置参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `HeartbeatTimeout` | 10s | Agent 心跳超时时间 |
| `DefaultRequeueInterval` | 5s | 默认重试间隔 |
| `DeletionPollInterval` | 2s | 删除状态轮询间隔 |
| `ExpirationCheckThreshold` | 30s | 临近过期调度检查阈值 |

## Spec 字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `image` | string | 容器镜像 |
| `command` | []string | 启动命令 |
| `args` | []string | 命令参数 |
| `envs` | []EnvVar | 环境变量 |
| `workingDir` | string | 工作目录 |
| `expireTime` | *Time | 过期时间（可选）|
| `exposedPorts` | []int32 | 暴露端口（调度考虑冲突）|
| `failurePolicy` | enum | `Manual` / `AutoRecreate` |
| `resetRevision` | *Time | 触发重置的时间戳 |
| `poolRef` | string | 关联的 SandboxPool |

## Status 字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `phase` | string | 当前状态 |
| `assignedPod` | string | 分配的 Agent Pod |
| `nodeName` | string | 运行节点 |
| `sandboxID` | string | 容器/沙箱 ID |
| `endpoints` | []string | 暴露的端点地址 |
| `acceptedResetRevision` | *Time | 已处理的 Reset 版本 |

## 最佳实践

### 1. 生产环境建议使用 Manual 模式

```yaml
spec:
  failurePolicy: Manual
```

Manual 模式下 Agent 丢失不会自动重调度，给运维人员时间排查问题。

### 2. 临时任务使用过期时间

```yaml
spec:
  expireTime: "2025-01-20T00:00:00Z"
```

过期后容器自动清理，CRD 保留供审计。

### 3. 故障恢复使用 Reset

```bash
fsb-ctl sandbox update my-sandbox --reset
```

触发 ResetRevision 更新，强制重新调度。

## 故障排查

| 现象 | 可能原因 | 排查步骤 |
|------|----------|----------|
| Phase 卡在 Pending | 无可用 Agent | 检查 Registry、SandboxPool 状态 |
| Phase 卡在 Bound | Agent 创建失败 | 检查 Agent 日志、镜像拉取 |
| Phase 卡在 Terminating | Agent 未确认删除 | 检查 Agent 状态、网络连通性 |
| 删除超时 | Finalizer 未移除 | 手动移除 Finalizer（紧急情况）|

## 相关文档

- [Fast Path 设计](./FAST_PATH_DESIGN.md)
- [Janitor 设计](./JANITOR_DESIGN.md)
- [Recovery 设计](./RECOVERY_DESIGN.md)
