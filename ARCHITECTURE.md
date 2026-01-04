# Fast Sandbox 架构设计文档

## 1. 概述 (Overview)

**Fast Sandbox** 是一个基于 Kubernetes 的高性能 Sandbox 管理系统。其核心目标是提供毫秒级的容器启动速度，主要用于 serverless 函数、代码沙箱执行等对启动延迟高度敏感的场景。

系统的核心设计理念是：**资源预热 (Resource Pooling)** + **镜像缓存亲和 (Image Affinity)** + **目标式调度 (Targeted Scheduling)**。

## 2. 核心架构 (Core Architecture)

系统采用 **Controller-Agent** 分离架构，建立在 Kubernetes 之上，但接管了 Sandbox 粒度的调度与管理。

![架构图](ARCHITECTURE.png)

## 3. 核心组件设计

### 3.1. SandboxPool (资源缓冲层)
*   **职责**: 维护一组 "热" 资源（Agent Pods）。
*   **管控模式**: **Direct Pod Management** (直接管控 Pod)。
    *   `SandboxPoolController` 不依赖 `Deployment` 或 `StatefulSet`，而是直接创建和管理 `CoreV1.Pod` 资源。
    *   **优势**:
        1.  **精准调度 (Targeted Provisioning)**: 可根据待处理 Sandbox 的镜像需求，结合 Node 镜像缓存情况，定向在特定 Node 上创建 Agent Pod，最大化亲和性。
        2.  **精细化缩容**: 可选择性删除空闲时间最长或位于资源紧张节点的 Pod，而非随机缩容。
*   **机制**: 
    *   Controller 根据 `SandboxPool` CR 定义的容量（Min/Max），向 K8s 申请创建 Agent Pods。
    *   **Pod 构建**: 自动注入 Runtime 所需的特权配置（HostPath 挂载、SecurityContext、Env 等），确保 Agent 能接管宿主机 Containerd。
    *   这些 Pods 是**同构的**，申请了固定的物理资源（如 4C8G），作为后续 Sandbox 的宿主。

### 3.2. Agent (数据面)
运行在 Agent Pod 内部的守护进程，是 Sandbox 的实际管理者。

*   **Runtime 架构**: 
    *   **方案**: **Host Containerd Integration** (优先实现)。
    *   **原理**: Agent Pod 通过 `VolumeMounts` 挂载宿主机的 `/run/containerd/containerd.sock`。
    *   **操作**: Agent 通过 gRPC 直接调用宿主机的 containerd 接口来创建、销毁、查询容器。
    *   **优势**: 
        1.  **零镜像拉取**: 直接复用宿主机上已经存在的镜像缓存。
        2.  **高性能**: 避免 Docker-in-Docker 的层级损耗。

*   **资源管理 (Resource Constraint)**:
    *   **约束**: Sandbox 消耗的物理资源必须受限于 Agent Pod 申请的 K8s 资源（Request/Limit）。
    *   **实现**: 在创建 Sandbox 容器时，Agent 需指定 Cgroup Parent 或手动将 Sandbox 进程加入到 Agent Pod 所在的 Cgroup 路径下。这样，Kubelet 只需监控 Agent Pod 的总资源使用情况。

*   **状态汇报**:
    *   **被动响应 (Pull Model)**: 响应 Controller 的状态查询请求。
    *   **关键特性**: 在响应中实时扫描并返回当前宿主机（Node）上存在的**镜像列表 (Image List)**，供调度器使用。

### 3.3. Controller (控制面)
负责全局状态的协调和调度。

*   **调度逻辑 (Targeted Scheduling)**:
    当一个新的 `Sandbox` CR 被创建时，Controller 执行以下逻辑：
    1.  **Pool 筛选 (Pool Selection)**: 根据 `spec.poolRef` 找到对应的 SandboxPool，并筛选出属于该 Pool 的所有 Agent Pods (通常通过 Label `fast-sandbox.io/pool-name` 关联)。
    2.  **资源过滤 (Filter)**: 在上述 Pool 的 Agent 中，筛选出状态为 Healthy 且剩余资源满足需求的 Agent。
    3.  **打分 (Score) - 镜像亲和性**: 
        *   检查 Agent 上报的 `Images` 列表。
        *   如果 Agent 所在的 Node 上已经有了 Sandbox 所需的镜像，该 Agent 得分大幅提高。
        *   **目的**: 优先调度到有镜像的节点，省去 `Image Pull` 的时间，实现秒级启动。
    4.  **绑定 (Bind)**: 选定 Agent，将 Sandbox 的目标状态（Desired State）推送到该 Agent。

*   **同步机制**:
    *   采用 **主动轮询与指令下发相结合** 的模式。
    *   **状态获取**: Controller 定期调用 Agent 的 `GetAgentStatus` 接口获取资源、Sandbox 状态和镜像缓存。
    *   **指令下发**: 调度完成后，Controller 调用 Agent 的 `SyncSandboxes` 接口下发期望状态。

## 4. 接口定义与扩展性

### 4.1 Runtime 抽象接口
为了支持未来的强隔离需求（如 RL 强化学习任务），Runtime 层被设计为接口，支持多种实现。

```go
type Runtime interface {
    // 初始化
    Initialize(ctx context.Context, config RuntimeConfig) error
    
    // 核心生命周期
    CreateSandbox(ctx context.Context, config *SandboxConfig) (*SandboxMetadata, error)
    DeleteSandbox(ctx context.Context, sandboxID string) error
    
    // 镜像与查询
    ListImages(ctx context.Context) ([]string, error) // 关键：用于调度决策
    ListSandboxes(ctx context.Context) ([]*SandboxMetadata, error)
}
```

### 4.2 支持的运行时后端
1.  **Containerd (v1/v2)**: 
    *   *当前首选*。利用 Namespace 隔离（如 `k8s.io` 或自定义 namespace）。
    *   轻量，标准，利用宿主机缓存。
2.  **Firecracker / Kata Containers (VM)**: 
    *   *未来扩展*。针对不可信代码或需要强内核隔离的场景。
    *   Agent 内部可以调用 Firecracker API 启动 MicroVM。
3.  **Docker**:
    *   备选。用于开发环境或特定旧系统兼容。

## 5. 工作流示例 (Workflow)

1.  **初始化**: 用户部署 `SandboxPool`，PoolController 创建 5 个 Agent Pods。
2.  **启动**: Agent Pod 启动，连接宿主机 Containerd，扫描发现本地有 `python:3.9` 镜像，上报给 Controller。
3.  **请求**: 用户提交一个 `Sandbox` CR，要求镜像 `python:3.9`，命令 `python script.py`。
4.  **调度**: Sandbox Controller 看到 CR，发现 Agent A 所在的节点有 `python:3.9` 缓存，且有空闲资源，选中 Agent A。
5.  **执行**: Controller 调用 Agent A 的 `/sync` 接口。
6.  **创建**: Agent A 调用宿主机 containerd，以 Agent Pod 的 Cgroup 为父级，启动一个新的容器。
7.  **完成**: 容器启动（无需拉取镜像，耗时 < 500ms），Agent A 返回成功，Controller 更新 CR 状态为 Running。

## 6. 开发计划

1.  **Phase 1: 核心 Runtime 实现**: 实现 `internal/agent/runtime/containerd_runtime.go`，跑通 ListImages 和 CreateSandbox。
2.  **Phase 2: Agent 服务化**: 完善 Agent HTTP Server，实现与 Controller 的协议对接。
3.  **Phase 3: Controller 基础调度**: 实现简单的 Round-Robin 调度，跑通端到端流程。
4.  **Phase 4: 高级特性**: 实现镜像亲和性调度和 Cgroup 资源限制。