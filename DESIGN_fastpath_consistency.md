# Fast-Path 一致性模式设计文档

## 1. 背景

### 1.1 问题描述

当前 Fast-Path 使用 `SyncSandboxes`（声明式全量同步）与 Agent 通信，存在严重 Bug：

```
Fast-Path 调用 SyncSandboxes(单个 sandbox)
    ↓
Agent 的 SyncSandboxes 是声明式：期望状态 = 传过来的列表
    ↓
Agent 删除所有不在列表中的 sandbox
    ↓
同 Pod 其他 sandbox 被误杀 ❌
```

### 1.2 解决思路

**全面放弃 Sync 语义，改用命令式 API**：
- 删除 `SyncSandboxes` 接口
- 新增 `CreateSandbox` / `DeleteSandbox` 命令式接口
- 所有路径（Fast-Path、Controller）统一使用命令式 API

## 2. 两种一致性模式

### 2.1 Fast 模式（默认，低一致性/最终一致性）

**核心思想**: Agent 先创建，CRD 后写，Janitor 等待 CRD 同步

```
┌─────────────────────────────────────────────────────────────┐
│  Fast-Path CreateSandbox (Fast 模式)                        │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  1. Registry.Allocate() ──────────▶ 选定 Agent Pod          │
│  2. Agent.CreateSandbox() ────────▶ 创建成功                │
│  3. K8s.Create(CRD) ──────────────▶ 异步写入                │
│  4. 立即返回响应                                            │
│                                                             │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│  Janitor 清理逻辑 (Fast 模式)                               │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  1. Agent.getStatus() → [sb-001, sb-002]                   │
│  2. K8s List CRD → [sb-001]                                │
│  3. 发现 sb-002: Agent 有，CRD 无                          │
│  4. 检查 sb-002 创建时间:                                   │
│     - 创建时间 < orphanCleanupTimeout (默认 10s)           │
│       → 等待，下次轮询再检查                                │
│     - 创建时间 ≥ orphanCleanupTimeout                      │
│       → 判定为孤儿，清理                                    │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

**优点**:
- 延迟最低
- 用户最早能拿到 sandbox 使用

**缺点**:
- CRD 写入失败时，正在使用的 sandbox 可能被强制回收
- 需要 Janitor 实现超时等待逻辑

**适用场景**: 对延迟极度敏感，可接受偶发异常

### 2.2 Strong 模式（强一致性）

**核心思想**: CRD 先写，Agent 后创建

```
┌─────────────────────────────────────────────────────────────┐
│  Fast-Path CreateSandbox (Strong 模式)                      │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  1. Registry.Allocate() ──────────▶ 选定 Agent Pod          │
│  2. K8s.Create(CRD, phase=Pending)                          │
│  3. Agent.CreateSandbox() ────────▶ 创建成功                │
│  4. K8s.Update(CRD, phase=Bound, assignedPod=xxx)           │
│  5. 返回响应                                                │
│                                                             │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│  Janitor 清理逻辑 (Strong 模式)                             │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  1. Agent 有，CRD 无 → 不会发生（CRD 先创建）               │
│  2. CRD 有，Agent 无 → 标记 Failed（Agent 故障）            │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

**优点**:
- 强一致性保证
- 不会误删正在使用的 sandbox
- Janitor 逻辑简单

**缺点**:
- 延迟略高（多一次 K8s 写）

**适用场景**: 需要强一致性保证的业务

## 3. API 设计

### 3.1 Agent API

```
删除:
  POST /api/v1/agent/sync

新增:
  POST /api/v1/agent/create  - 创建单个 sandbox（幂等）
  POST /api/v1/agent/delete  - 删除单个 sandbox
  GET  /api/v1/agent/status  - 查询当前状态（含创建时间）
```

#### CreateSandbox Request/Response

```go
type CreateSandboxRequest struct {
    Sandbox   SandboxSpec `json:"sandbox"`
}

type CreateSandboxResponse struct {
    Success      bool   `json:"success"`
    Message      string `json:"message,omitempty"`
    SandboxID    string `json:"sandboxId"`
    CreatedAt    int64  `json:"createdAt"`    // Unix timestamp，Janitor 需要用
}
```

#### Status Response（增强）

```go
type SandboxMetadata struct {
    SandboxID string `json:"sandboxId"`
    CreatedAt int64  `json:"createdAt"`    // 新增：创建时间戳
    // ... 其他字段
}
```

### 3.2 Fast-Path gRPC API

```protobuf
message CreateRequest {
    string image = 1;
    string pool_ref = 2;
    repeated int32 exposed_ports = 3;
    repeated string command = 4;
    repeated string args = 5;
    string namespace = 6;

    // 新增：指定一致性模式
    // 空字符串表示使用 Controller 默认模式
    ConsistencyMode consistency_mode = 7;
}

enum ConsistencyMode {
    FAST = 0;     // 先创建 Agent，后写 CRD
    STRONG = 1;   // 先写 CRD，后创建 Agent
}

message CreateResponse {
    string sandbox_id = 1;
    string agent_pod = 2;
    repeated string endpoints = 3;
}
```

### 3.3 Controller 启动参数

```bash
# 启动参数
--fastpath-consistency-mode=fast      # 默认值: fast
--fastpath-orphan-timeout=10s         # Fast 模式下孤儿清理超时，默认: 10s
```

## 4. 模块修改清单

| 模块 | 文件 | 修改内容 |
|------|------|----------|
| **API 定义** | `internal/api/types.go` | 删除 `SandboxesRequest`；添加 `CreateSandboxRequest`, `DeleteSandboxRequest`；`SandboxMetadata` 添加 `CreatedAt` |
| **Agent Server** | `internal/agent/server/rpc_server.go` | 删除 `/sync` handler；添加 `/create`, `/delete` handler；status 返回创建时间 |
| **Agent Manager** | `internal/agent/runtime/sandbox_manager.go` | 删除 `SyncSandboxes()`；添加 `CreateSandbox()`, `DeleteSandbox()`（幂等） |
| **Agent Client** | `pkg/client/agent_client.go` | 删除 `SyncSandboxes()`；添加 `CreateSandbox()`, `DeleteSandbox()` |
| **Fast-Path Server** | `internal/controller/fastpath/server.go` | 支持两种模式；根据 mode 选择执行顺序 |
| **Sandbox Controller** | `internal/controller/sandbox_controller.go` | 删除 `syncAgent()`；改为事件驱动的 `CreateSandbox()`/`DeleteSandbox()` |
| **Janitor** | `internal/controller/janitor/` | 实现基于创建时间的超时等待逻辑 |
| **Main** | `cmd/main.go` 或 `cmd/controller/main.go` | 添加启动参数 |

## 5. 关键实现细节

### 5.1 Agent.CreateSandbox 幂等性

```go
func (m *SandboxManager) CreateSandbox(spec api.SandboxSpec) (*CreateSandboxResponse, error) {
    m.mu.Lock()
    defer m.mu.Unlock()

    // 1. 幂等检查：已存在则直接返回成功
    if sb, _ := m.runtime.GetSandbox(spec.SandboxID); sb != nil {
        return &CreateSandboxResponse{
            Success:   true,
            SandboxID: spec.SandboxID,
            CreatedAt: sb.CreatedAt,  // 返回实际创建时间
        }, nil
    }

    // 2. 不存在则创建
    if err := m.runtime.CreateSandbox(spec); err != nil {
        return nil, err
    }

    return &CreateSandboxResponse{
        Success:   true,
        SandboxID: spec.SandboxID,
        CreatedAt: time.Now().Unix(),
    }, nil
}
```

### 5.2 Fast-Path 两种模式实现

```go
func (s *Server) CreateSandbox(ctx context.Context, req *fastpathv1.CreateRequest) (*fastpathv1.CreateResponse, error) {
    // 确定一致性模式
    mode := s.defaultMode  // 从启动参数获取默认值
    if req.ConsistencyMode == STRONG {
        mode = Strong
    }

    agent, err := s.Registry.Allocate(tempSB)
    if err != nil {
        return nil, err
    }

    if mode == Fast {
        return s.createFast(ctx, req, agent, tempSB)
    }
    return s.createStrong(ctx, req, agent, tempSB)
}

func (s *Server) createFast(...) (*fastpathv1.CreateResponse, error) {
    // 1. 先创建 Agent
    createResp, err := s.AgentClient.CreateSandbox(endpoint, createReq)
    if err != nil {
        s.Registry.Release(agent.ID, tempSB)
        return nil, err
    }

    // 2. 异步创建 CRD
    go s.asyncCreateCRD(tempSB, agent)

    // 3. 立即返回
    return &fastpathv1.CreateResponse{...}, nil
}

func (s *Server) createStrong(...) (*fastpathv1.CreateResponse, error) {
    // 1. 先创建 CRD (phase=Pending)
    tempSB.Status.Phase = "Pending"
    if err := s.K8sClient.Create(ctx, tempSB); err != nil {
        s.Registry.Release(agent.ID, tempSB)
        return nil, err
    }

    // 2. 创建 Agent
    _, err := s.AgentClient.CreateSandbox(endpoint, createReq)
    if err != nil {
        // 清理 CRD
        s.K8sClient.Delete(ctx, tempSB)
        s.Registry.Release(agent.ID, tempSB)
        return nil, err
    }

    // 3. 更新 CRD (phase=Bound)
    tempSB.Status.Phase = "Bound"
    tempSB.Status.AssignedPod = agent.PodName
    tempSB.Status.NodeName = agent.NodeName
    s.K8sClient.Status().Update(ctx, tempSB)

    return &fastpathv1.CreateResponse{...}, nil
}
```

### 5.3 Janitor 孤儿清理（Fast 模式）

```go
func (j *Janitor) reconcileAgent(agentPod string) {
    // 1. 获取 Agent 状态
    status := j.agentClient.GetStatus(agentPod)

    // 2. 获取 K8s CRD 列表
    crdList := j.k8sClient.ListSandboxes()

    crdMap := make(map[string]bool)
    for _, crd := range crdList {
        crdMap[crd.Name] = true
    }

    now := time.Now().Unix()

    // 3. 找出孤儿
    for _, sb := range status.SandboxStatuses {
        if !crdMap[sb.SandboxID] {
            // 检查创建时间
            age := now - sb.CreatedAt
            if age < int64(j.orphanTimeout.Seconds()) {
                log.Printf("Sandbox %s created %ds ago, waiting for CRD", sb.SandboxID, age)
                continue
            }

            // 超时，判定为孤儿，清理
            log.Printf("Sandbox %s is orphan (age %ds), cleaning up", sb.SandboxID, age)
            j.agentClient.DeleteSandbox(agentPod, sb.SandboxID)
        }
    }
}
```

### 5.4 Controller 事件驱动（替代 Sync）

```go
func (r *SandboxReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    sb := &apiv1alpha1.Sandbox{}
    if err := r.Get(ctx, req.NamespacedName, sb); err != nil {
        // 已删除，触发删除逻辑
        return r.handleDelete(ctx, req.NamespacedName)
    }

    switch sb.Status.Phase {
    case "", "Pending":
        return r.handleCreate(ctx, sb)
    case "Bound":
        return ctrl.Result{}, nil
    case "Failed":
        return ctrl.Result{}, nil
    }

    return ctrl.Result{}, nil
}

func (r *SandboxReconciler) handleCreate(ctx context.Context, sb *apiv1alpha1.Sandbox) (ctrl.Result, error) {
    agent := r.getAgent(sb.Status.AssignedPod)
    if agent == nil {
        return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
    }

    // 幂等创建
    if err := r.agentClient.CreateSandbox(agent.Endpoint, sb.Spec); err != nil {
        sb.Status.Phase = "Failed"
        sb.Status.Message = err.Error()
        r.Status().Update(ctx, sb)
        return ctrl.Result{}, nil
    }

    // 更新状态
    sb.Status.Phase = "Bound"
    r.Status().Update(ctx, sb)
    return ctrl.Result{}, nil
}
```

## 6. 测试计划

### 6.1 单元测试

- `AgentClient.CreateSandbox()` 幂等性
- `AgentClient.DeleteSandbox()` 成功/失败处理
- Fast/Strong 两种模式执行流程

### 6.2 E2E 测试

`test/e2e/05-advanced-features/fast-path.sh`:

```bash
describe() {
    echo "Fast-Path 一致性模式验证"
}

run() {
    # 1. Fast 模式：验证创建成功，Janitor 不误删
    # 2. Fast 模式：模拟 CRD 写入失败，验证孤儿清理
    # 3. Strong 模式：验证强一致性
    # 4. 混合场景：Fast 创建的 sandbox 与 Controller 创建的共存
}
```

## 7. 风险与注意事项

1. **Fast 模式异常**: CRD 写入失败时，正在使用的 sandbox 会被清理，需要在文档中明确说明
2. **时钟同步**: Janitor 依赖 Agent 返回的创建时间，需确保 Agent 与 Controller 时钟同步
3. **网络分区**: K8s API Server 不可用时，Fast 模式仍可工作，但会产生孤儿

## 8. 参考文档

- Agent API 规范: `internal/api/types.go`
- Fast-Path 协议: `api/proto/v1/fastpath.proto`
- Controller Reconcile 逻辑: `internal/controller/sandbox_controller.go`
