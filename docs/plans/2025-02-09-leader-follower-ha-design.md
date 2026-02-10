# Leader-Follower High Availability Design

**Date**: 2025-02-09
**Status**: Design Approved
**Author**: Fast Sandbox Team

## Problem Statement

The current Fast-Path gRPC service runs on the Controller, which must be a singleton to avoid conflicts in the in-memory Registry. This limits horizontal scalability and creates a single point of failure.

**Constraints**:
- Cannot rely on users to manually shard instances
- Leader election already exists via K8s Lease
- Registry is in-memory state that needs consistency

## Solution: Read-Write Separation with Transparent Forwarding

### Architecture Overview

```
                    ┌─────────────────┐
                    │   K8s Service   │
                    │ (fast-sandbox)  │
                    └────────┬────────┘
                             │
              ┌──────────────┼──────────────┐
              │              │              │
         ┌────▼────┐   ┌────▼────┐   ┌────▼────┐
         │ Leader  │   │Follower │   │Follower │
         │ (Pod 1) │   │ (Pod 2) │   │ (Pod 3) │
         └────┬────┘   └────┬────┘   └─────────┘
              │             │
              │             │ gRPC 转发
              ▼             ▼
         ┌─────────┐   ┌─────────┐
         │Registry │   │  直接   │
         │(仅Leader)│   │访问 K8s │
         └─────────┘   └─────────┘
```

### Request Routing

| Operation      | Leader | Follower | Notes                         |
|----------------|--------|----------|-------------------------------|
| CreateSandbox  | ✅     | ⚠️ 转发   | Requires Registry.Allocate()  |
| DeleteSandbox  | ✅     | ✅       | K8s API only                  |
| UpdateSandbox  | ✅     | ✅       | CRD spec update only          |
| ListSandboxes  | ✅     | ✅       | K8s API read only             |
| GetSandbox     | ✅     | ✅       | K8s API read only             |

## Components

### 1. FastPathServer Extension

```go
type Server struct {
    fastpathv1.UnimplementedFastPathServiceServer
    K8sClient              client.Client
    Registry               agentpool.AgentRegistry  // 仅 Leader 使用
    AgentClient            *api.AgentClient
    DefaultConsistencyMode api.ConsistencyMode

    // 新增字段
    IsLeader               bool                      // 当前是否为 Leader
    LeaderIP               string                    // 缓存的 Leader IP
    LeaderClient           fastpathv1.FastPathServiceClient  // 连接 Leader 的 gRPC 客户端
    LeaseName              string                    // Lease 名称
}
```

### 2. Leader Discovery

利用现有的 K8s Lease 机制（controller-runtime leader election）：

```go
// Leader 在获得 lease 后写入自己的 IP
lease.Annotations["fast-sandbox.io/leader-ip"] = podIP

// Follower 从 Lease 读取 Leader IP
leaderIP := lease.Annotations["fast-sandbox.io/leader-ip"]
```

### 3. Registry Restore

新 Leader 启动时从 CRD Status 重建 Registry：

```go
func (r *InMemoryRegistry) Restore(ctx context.Context, c client.Reader) error {
    // 1. List all Sandbox
    var sbList apiv1alpha1.SandboxList
    c.List(ctx, &sbList)

    // 2. 重建 Registry 状态 (Allocated, UsedPorts)
    for _, sb := range sbList.Items {
        if sb.Status.AssignedPod != "" {
            id := AgentID(sb.Status.AssignedPod)
            // 创建/更新 slot，增加 Allocated 计数
        }
    }
    return nil
}
```

**Note**: `Restore()` 方法已存在于 `registry.go:370-440`，并在 `main.go:137-145` 调用。

## Failover Sequence

```
时间线 →

旧 Leader:       崩溃/分区
                          │
                          ▼
Follower:         检测到 Leader Lease 过期
                          │
                          ▼
                  尝试获取 Lease (leader election)
                          │
                          ▼
                  ┌───────────────────────┐
                  │  成为新 Leader        │
                  │  becomeLeader()      │
                  │  1. 写入 Lease IP    │
                  │  2. 调用 Restore()   │
                  │  3. 开始处理请求     │
                  └───────────────────────┘
                          │
                          ▼
其他 Follower:    Watch Lease 变化
                  更新 LeaderIP 缓存
                  继续转发 CreateSandbox
                          │
                          ▼
客户端请求:       CreateSandbox → Follower → Leader (新)
                           │              │
                           ▼              ▼
                      Delete/Update    Create with
                         直接处理      Registry.Allocate()
```

## Error Handling

### Follower Forwarding Failure

```go
func (s *Server) forwardCreateSandbox(ctx context.Context, req *fastpathv1.CreateRequest) (*fastpathv1.CreateResponse, error) {
    leaderIP, err := s.getLeaderIP(ctx)
    if err != nil {
        return nil, status.Errorf(codes.Unavailable, "leader not available: %v", err)
    }

    // 尝试转发
    resp, err := s.LeaderClient.CreateSandbox(ctx, req)
    if err != nil {
        // 返回 Unavailable，客户端重试
        return nil, status.Errorf(codes.Unavailable, "leader unavailable: %v", err)
    }
    return resp, nil
}
```

**客户端行为**: 收到 `Unavailable` 错误后重试请求

## Deployment Model

- Controller Deployment replicas = N (建议 3)
- 通过 leader election 选出 1 个 Leader
- 统一的 K8s Service 对外暴露 gRPC 端口 (9090)
- 无需客户端配置变更

## Implementation Checklist

### Phase 1: Core Infrastructure
- [ ] Add `IsLeader`, `LeaderIP`, `LeaderClient` fields to `FastPathServer`
- [ ] Implement `becomeLeader()` to write IP to Lease annotation
- [ ] Implement `stepDown()` to clean up on leadership loss
- [ ] Implement `getLeaderIP()` to read from Lease annotation

### Phase 2: Forwarding Logic
- [ ] Modify `CreateSandbox` to check `IsLeader` and forward if needed
- [ ] Implement `forwardCreateSandbox()` with gRPC client
- [ ] Add error handling for unavailable leader

### Phase 3: Leader Election Integration
- [ ] Hook into controller-runtime leader election callbacks
- [ ] Call `Restore()` on `becomeLeader()`
- [ ] Add metrics for leader/follower status

### Phase 4: Testing
- [ ] Unit tests for forwarding logic
- [ ] E2E test for failover scenario
- [ ] Performance test for forwarding overhead

## Trade-offs

| Aspect | Benefit | Cost |
|--------|---------|------|
| 水平扩展 | 支持多副本，提高吞吐量 | CreateSandbox 有转发延迟 |
| 高可用 | Leader 故障自动切换 | 切主期间短暂不可用 |
| 简化运维 | 统一 Service 入口 | 增加复杂度 |

## References

- `internal/controller/agentpool/registry.go:370-440` - Restore() implementation
- `cmd/controller/main.go:137-145` - Restore() call on startup
- etcd raft protocol for leader-follower pattern
- Kubernetes controller-runtime leader election
