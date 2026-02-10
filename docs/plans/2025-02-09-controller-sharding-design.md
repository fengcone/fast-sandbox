# Controller Sharding Design

**Date**: 2025-02-09
**Status**: Design Approved
**Author**: Fast Sandbox Team

## Problem Statement

需要水平扩展 Controller 以提高吞吐量。每个 Pool 约定由同一个 Controller 实例调度，避免 Registry 冲突。客户端需要智能路由到正确的 Controller。

## Solution: Controller Sharding + Client-Side Routing

### Architecture Overview

```
                        ┌─────────────────────────────────┐
                        │    K8s Service (fast-sandbox)   │
                        │    (可以是 NLB 负载均衡)         │
                        └────────────┬────────────────────┘
                                     │
                           ┌─────────┼─────────┐
                           │         │         │
                      ┌────▼───┐ ┌──▼─────┐ ┌─▼──────┐
                      │Cont-1  │ │Cont-2  │ │Cont-3  │
                      │(Leader)│ │        │ │        │
                      │Pool-A  │ │Pool-B  │ │Pool-C  │
                      │Pool-D  │ │Pool-E  │ │        │
                      │15 SBs  │ │12 SBs  │ │8 SBs   │
                      └────────┘ └────────┘ └────────┘
                           │         │         │
                           └─────────┴─────────┘
                                     │
                    ┌────────────────┴────────────────┐
                    │   ControllerPoolAssignment CRD  │
                    │   (Leader 维护，Controller watch)│
                    └─────────────────────────────────┘
                                     │
                    ┌────────────────┴────────────────┐
                    │     WatchRouteTable() (gRPC)    │
                    │     fsb-ctl/SDK 订阅路由变更     │
                    └─────────────────────────────────┘
```

### Request Routing Flow

```
fsb-ctl: 创建 sandbox (pool-b)
    │
    ├─→ 查本地路由缓存: pool-b → Cont-2 (10.0.1.5:9090)
    │
    └─→ 直接 gRPC 调用 Cont-2.CreateSandbox()
            │
            └─→ Cont-2 独立处理，Registry 分配 Agent
```

## CRD Definition

### CRD 结构

```yaml
apiVersion: fast-sandbox.io/v1alpha1
kind: ControllerPoolAssignment
metadata:
  name: controller-pod-abc123  # Controller Pod UID
  labels:
    fast-sandbox.io/role: controller
spec:                          # ← Leader 写（分配信息）
  controllerID: controller-pod-abc123
  controllerIP: 10.244.1.5     # gRPC 直连地址
  pools:
    - pool-a
    - pool-d
  sandboxCount: 15
status:                        # ← Controller 自己写（就绪状态）
  ready: true                  # Pool 同步完成
  lastHeartbeat: "2025-02-09T10:00:00Z"
  poolStatus:                  # 每个 Pool 的详细状态
    pool-a:
      synced: true
      agentCount: 2
      sandboxCount: 15
      lastSync: "2025-02-09T10:00:00Z"
    pool-d:
      synced: true
      agentCount: 1
      sandboxCount: 8
      lastSync: "2025-02-09T10:00:00Z"
```

### CRD 读写分工

| 字段 | 写入者 | 说明 |
|------|--------|------|
| `spec.controllerID` | Leader | Controller 标识 |
| `spec.controllerIP` | Leader | gRPC 直连地址 |
| `spec.pools` | Leader | 分配的 Pool 列表 |
| `spec.sandboxCount` | Leader | 负载统计 |
| `status.ready` | Controller | 整体就绪状态 |
| `status.lastHeartbeat` | Controller | 心跳时间 |
| `status.poolStatus` | Controller | 每个 Pool 的同步状态 |

**用途**：
- **Leader**：维护 spec（分配状态），故障转移时更新 pools
- **Controller**：Watch 自己的 spec 变化，更新 status 报告就绪状态
- **fsb-ctl/SDK**：通过 `WatchRouteTable()` 获取完整路由表（spec + status）

## gRPC API

### 新增 API

```protobuf
service FastPathService {
  // 现有 API...
  rpc CreateSandbox(CreateRequest) returns (CreateResponse);
  rpc DeleteSandbox(DeleteRequest) returns (DeleteResponse);

  // 新增：流式 Watch 路由表
  rpc WatchRouteTable(WatchRouteRequest) returns (stream RouteTableUpdate);
}

message RouteTableUpdate {
  repeated ControllerRoute routes = 1;
  int64 generation = 2;  // 用于检测变更
}

message ControllerRoute {
  string controller_id = 1;
  string controller_ip = 2;  // gRPC 直连地址
  repeated string pools = 3;
  int32 sandbox_count = 4;
  bool ready = 5;           // Pool 同步完成
}
```

### 错误码扩展

```protobuf
enum ErrorCode {
  // 现有错误码...
  NOT_MY_POOL = 10;           // Pool 不由该 Controller 管理
  POOL_NOT_READY = 11;        // Pool 正在同步中
  CONTROLLER_UNAVAILABLE = 12; // 目标 Controller 不可用
}
```

## Corner Cases Handling

### 场景 1：路由过期（fsb-ctl 路由到错误的 Controller）

**原因**：fsb-ctl 路由缓存过期，或 Pool 刚被重新分配

**处理**：
```go
// fsb-ctl
func (c *Client) CreateSandbox(pool, name string, req CreateRequest) error {
    controllerIP := c.routeTable.Lookup(pool)

    resp, err := grpc.Call(controllerIP, req)
    if err != nil && err.Code() == NotMyPool {
        // 1. 立即重试到正确的 Controller
        correctIP := err.GetDetail()  // 错误中携带正确的 IP
        resp, err = grpc.Call(correctIP, req)

        // 2. 后台刷新路由表
        go c.refreshRouteTableAsync()
    }
    return resp, err
}
```

**Controller 侧**：
```go
func (c *Controller) CreateSandbox(req CreateRequest) error {
    if !c.isPoolManaged(req.PoolRef) {
        // 查询正确的 Controller
        correctIP := c.getPoolController(req.PoolRef)
        return status.Errorf(codes.NotFound,
            "pool %s not managed by this controller, try %s",
            req.PoolRef, correctIP).WithDetails(correctIP)
    }
    // ...
}
```

---

### 场景 2：Controller 挂了（Pool 重新分配详细流程）

**完整时序图**：

```
时间  Leader                    Cont-A (挂)           Cont-B               fsb-ctl
  │
  │     健康检查 (每 5s)           💀 挂了
  │         │
  │         │ ping 超时
  │         ▼
  │   检测到 Cont-A 挂了
  │         │
  │         ▼
  │   获取 Cont-A 的 Pool 列表
  │   [pool-a, pool-d]
  │         │
  │         ▼
  │   选择负载最小的 Controller
  │   Cont-B (8 SBs) ← 选中
  │         │
  │         ▼
  │   ╔════════════════════════════════════════╗
  │   ║  Step 1: 从 Cont-A 移除 Pool            ║
  │   ╚════════════════════════════════════════╝
  │   更新 Cont-A CRD:
  │   spec.pools: [] (清空)
  │         │
  │         ▼
  │   ╔════════════════════════════════════════╗
  │   ║  Step 2: 分配给 Cont-B                  ║
  │   ╚════════════════════════════════════════╝
  │   更新 Cont-B CRD:
  │   spec.pools: [pool-a, pool-d]
  │   spec.sandboxCount: 8 + 23 = 31
  │         │
  │         ├──────────────────────────────────────> WatchRouteTable()
  │         │                                        推送路由更新
  │         │
  │         │
  │                                    watch CRD 变更
  │                                    │
  │                                    ▼
  │                              检测到新 Pool: pool-a, pool-d
  │                                    │
  │                                    ▼
  │                              ╔═══════════════════════════╗
  │                              ║  Step 3: 同步 Pool 状态  ║
  │                              ╚═══════════════════════════╝
  │                              调用 Restore() 从 CRD 重建
  │                              ├── List pool-a 的 Sandbox
  │                              ├── List pool-d 的 Sandbox
  │                              ├── 重建 Registry.Allocated
  │                              └── 等待 Agent Pod Ready
  │                                    │
  │                                    ▼
  │                              更新 CRD status:
  │                              status.ready: true
  │                              status.poolStatus.pool-a.synced: true
  │                              status.poolStatus.pool-d.synced: true
  │                                    │
  │                                    ├───────────────────────────> WatchRouteTable()
  │                                    │                           推送 status.ready=true
  │                                    │
  │                                    │
  │   fsb-ctl 更新路由表:
  │   pool-a → Cont-B (ready=true)
  │   pool-d → Cont-B (ready=true)
  │         │
  │         ▼
  │   客户端请求重试到 Cont-B ✓
```

**Leader 侧代码**：
```go
func (l *Leader) handleControllerFailure(deadController Controller) {
    deadPools := l.getControllerPools(deadController.ID)

    // Step 1: 从挂掉的 Controller 移除 Pool
    l.updateControllerCRD(deadController.ID, func(crd *ControllerPoolAssignment) {
        crd.Spec.Pools = []string{}
        crd.Spec.SandboxCount = 0
    })

    // Step 2: 重新分配每个 Pool
    for _, pool := range deadPools {
        target := l.selectLeastLoadedController()
        l.assignPoolToController(pool, target)
    }
}

func (l *Leader) assignPoolToController(pool, controllerID string) {
    // 更新目标 Controller 的 CRD
    l.updateControllerCRD(controllerID, func(crd *ControllerPoolAssignment) {
        crd.Spec.Pools = append(crd.Spec.Pools, pool)
        crd.Spec.SandboxCount += l.getPoolSandboxCount(pool)
    })
}
```

**Controller 侧代码**：
```go
func (c *Controller) watchOwnCRD() {
    watcher := c.K8sClient.Watch(&ControllerPoolAssignment{}, c.controllerID)
    for event := range watcher.ResultChan() {
        if event.Type == "Modified" {
            c.onPoolAssignmentChanged(event.Object)
        }
    }
}

func (c *Controller) onPoolAssignmentChanged(crd *ControllerPoolAssignment) {
    oldPools := c.managedPools
    newPools := crd.Spec.Pools

    // 新增的 Pool 需要同步
    for _, pool := range diff(newPools, oldPools) {
        c.syncPool(pool)
    }

    // 移除的 Pool 需要清理
    for _, pool := range diff(oldPools, newPools) {
        c.cleanupPool(pool)
    }
}

func (c *Controller) syncPool(pool string) error {
    // 1. 调用 Restore() 从 CRD 重建该 Pool 的状态
    // 2. 等待 Agent Pod Ready
    // 3. 更新 CRD status.ready = true
    if err := c.Registry.RestorePool(pool); err != nil {
        return err
    }

    // 更新 status
    c.updateStatus(func(status *ControllerPoolAssignmentStatus) {
        if status.PoolStatus == nil {
            status.PoolStatus = make(map[string]PoolStatus)
        }
        status.PoolStatus[pool] = PoolStatus{
            Synced:       true,
            AgentCount:   c.getPoolAgentCount(pool),
            SandboxCount: c.getPoolSandboxCount(pool),
            LastSync:     metav1.Now(),
        }
    })
    return nil
}
```

---

### 场景 3：新 Controller 加入

**Controller 注册**：创建自己的 CRD 记录
```go
func (c *Controller) registerSelf() {
    crd := &ControllerPoolAssignment{
        ObjectMeta: metav1.ObjectMeta{
            Name: c.controllerID,  // Pod UID
            Labels: map[string]string{
                "fast-sandbox.io/role": "controller",
            },
        },
        Spec: ControllerPoolAssignmentSpec{
            ControllerID: c.controllerID,
            ControllerIP: c.podIP,
            Pools:        []string{},  // 初始为空
        },
    }
    c.K8sClient.Create(ctx, crd)
}
```

**Leader 发现**：Watch CRD 变化
```go
func (l *Leader) watchControllers() {
    watcher := c.K8sClient.Watch(&ControllerPoolAssignment{})
    for event := range watcher.ResultChan() {
        if event.Type == "Added" {
            l.onControllerJoined(event.Object)
        }
    }
}
```

---

### 场景 4：Pool 重新分配时的进行中请求

**策略**：立即中断 + 客户端重试 + Pool 就绪检查

**关键点**：
- Cont-B 在 `status.ready = true` 之前拒绝请求
- fsb-ctl 收到 `Unavailable` 后等待重试
- fsb-ctl 监听 `status.ready` 变化后重试

**Controller 侧检查**：
```go
func (c *Controller) CreateSandbox(req CreateRequest) error {
    pool := req.PoolRef

    // 检查是否是自己管理的 Pool
    if !c.isPoolManaged(pool) {
        correctIP := c.getPoolController(pool)
        return status.Errorf(codes.NotFound, "not my pool, try %s", correctIP)
    }

    // 检查 Pool 是否已就绪（status.ready=true 且 status.poolStatus[pool].synced=true）
    if !c.isPoolReady(pool) {
        return status.Errorf(codes.Unavailable,
            "pool %s is being initialized, retry soon", pool)
    }

    // 正常处理
    return c.createSandbox(req)
}

func (c *Controller) isPoolReady(pool string) bool {
    crd := c.getOwnCRD()
    if !crd.Status.Ready {
        return false
    }
    poolStatus, ok := crd.Status.PoolStatus[pool]
    if !ok || !poolStatus.Synced {
        return false
    }
    return true
}
```

---

### 场景 5：Pool 创建时的初始分配

**Leader 监听 Pool 创建，负载均衡分配**：
```go
func (l *Leader) watchPools() {
    watcher := c.K8sClient.Watch(&SandboxPool{})
    for event := range watcher.ResultChan() {
        if event.Type == "Added" {
            pool := event.Object.(*SandboxPool)
            target := l.selectLeastLoadedController()
            l.assignPoolToController(pool.Name, target)
        }
    }
}

func (l *Leader) selectLeastLoadedController() string {
    controllers := l.getAllControllers()
    var minCount int = math.MaxInt32
    var selected string

    for _, ctrl := range controllers {
        crd := l.getControllerCRD(ctrl.ID)
        if crd.Spec.SandboxCount < minCount {
            minCount = crd.Spec.SandboxCount
            selected = ctrl.ID
        }
    }
    return selected
}
```

## Leader Election

利用 K8s Lease 机制（已有）：

```go
// controller-runtime 内置
func (m *Manager) LeaderElection(...) {
    // 获得锁的成为 Leader
    // 负责健康检查、Pool 分配、重新分配
}
```

**Leader 额外职责**（相比普通 Controller）：
1. 健康检查其他 Controller
2. Pool 初始分配（监听 Pool 创建）
3. Pool 重新分配（Controller 故障时）
4. 维护 ControllerPoolAssignment CRD

## Implementation Checklist

### Phase 1: CRD & Basic Infrastructure
- [ ] 定义 ControllerPoolAssignment CRD
- [ ] 生成 CRD 代码（codegen）
- [ ] 扩展 FastPathServer 结构
- [ ] 实现 Controller 注册逻辑

### Phase 2: Leader Logic
- [ ] 实现 Leader 健康检查
- [ ] 实现 Pool 初始分配（监听创建事件）
- [ ] 实现 Pool 重新分配逻辑
- [ ] 实现 Controller 故障处理

### Phase 3: Controller Logic
- [ ] 实现 Controller watch 自己的 CRD
- [ ] 实现 Registry 同步（Restore）
- [ ] 实现 Pool 就绪检查
- [ ] 扩展错误码（NotMyPool, PoolNotReady）

### Phase 4: Client/SDK
- [ ] 实现 WatchRouteTable() gRPC API
- [ ] 实现 fsb-ctl 路由表缓存
- [ ] 实现重试 + 后台刷新逻辑
- [ ] 更新 fsb-ctl 所有命令使用路由

### Phase 5: Testing
- [ ] 单元测试：Leader 分配逻辑
- [ ] 单元测试：路由过期处理
- [ ] E2E 测试：Controller 故障转移
- [ ] E2E 测试：Pool 重新分配
- [ ] 性能测试：水平扩展吞吐量

## Trade-offs

| Aspect | Benefit | Cost |
|--------|---------|------|
| 水平扩展 | 线性提升吞吐量 | 增加客户端复杂度 |
| 分片隔离 | 每个 Registry 独立，无冲突 | Pool 需要绑定 Controller |
| 客户端路由 | 减少转发开销 | 需要维护路由表 |
| 故障转移 | 自动重新分配 | 短暂不可用 |

## Comparison: Leader-Follower vs Sharding

| 特性 | Leader-Follower | Controller Sharding |
|------|-----------------|-------------------|
| 复杂度 | 中等 | 较高 |
| 吞吐量 | 受限于单 Leader | 线性扩展 |
| 客户端 | 简单（Service 入口） | 复杂（路由表） |
| 故障隔离 | 全部依赖 Leader | 分片隔离 |
| 适用场景 | 中小规模 | 大规模部署 |

## References

- MongoDB Sharding
- Kafka Producer Partitioner
- Kubernetes Operator Pattern
