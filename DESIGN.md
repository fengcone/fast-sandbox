# Sandbox 系统设计概要

## 1. 前提与目标

- **场景**: RL 训练/评估需要频繁创建高吞吐、低延迟的 sandbox 环境，用于执行代码和计算 reward。
- **核心思路**: 不直接用 kubelet 为每个 sandbox 创建 Pod，而是在集群中预先调度一批带 Agent 的 Pod，由 Agent 通过宿主机 containerd 在节点上创建 sandbox 容器，并复用当前 Pod 的资源隔离（cgroup / network）。
- **目标延迟**: 从用户创建 SandboxClaim 到可用的 sandbox，控制在约 300ms ~ 1s。

前提约束：
- node->images 信息仅通过当前已有的 Agent Pod 上报获得，不额外部署镜像索引 DaemonSet。
- 单个 Agent Pod 可以 oversubscribe CPU，由 Controller 策略控制逻辑并发上限。
- sandbox 容器在 Agent Pod 的网络下暴露端口，Controller 将 `<AgentPodIP:Port>` 返回给用户。
- Operator 基于 controller-runtime 实现。
- 不为 Agent 单独定义 CR，Agent 状态仅在 Controller 内存中维护（通过 RPC 心跳）。

## 2. 总体架构

整体拆为三类组件：

1. **Controller（Operator）**
   - 基于 controller-runtime 实现。
   - 负责：
     - 维护 Agent Pod 资源池（含一定 buffer），可按镜像亲和创建新的 Agent Pod。
     - 维护内存中的 AgentRegistry 和 image 索引（image -> Agents）。
     - 调度 SandboxClaim 到合适的 Agent Pod（镜像亲和 + 资源 + oversubscribe 策略）。
     - 通过 RPC 调用 Agent 创建/销毁 sandbox 容器，更新 SandboxClaim 状态。

2. **Agent Pod + Agent 进程**
   - 在 K8s 中预先调度的一批 Pod，每个 Pod 内运行一个 Agent 进程。
   - Pod 本身向 kube-scheduler 申请 CPU/Mem/ENI 等资源，相当于预占一块资源池。
   - Pod 通过 hostPath 挂载宿主机 containerd socket（以及必要目录），Agent 直接与 containerd 交互：
     - 列出本 node 可用镜像。
     - 创建/销毁 sandbox 容器（containerd container + task）。
   - sandbox 容器：
     - 使用当前 Pod 的 cgroup（或其子层级）和 network namespace。
     - 在 kubelet 看来只是 Pod 内多了一些进程，资源都计入该 Pod，不产生额外 Pod/容器对象负担。
   - Agent 周期性向 Controller 上报：
     - Pod/Node 信息（PodName/PodIP/NodeName）。
     - 逻辑 capacity / 当前运行 sandbox 数。
     - 当前 node 已有镜像列表。

3. **用户 / RL 系统**
   - 通过创建 `SandboxClaim` CR 来声明需要一个 sandbox。
   - 读取 `SandboxClaim.status.address` 获取 `<AgentPodIP:Port>`，通过该地址与 sandbox 通信（HTTP/gRPC 等）。

## 3. CRD：SandboxClaim

唯一 CRD：`SandboxClaim`，由用户创建，Controller 负责调度与状态推进。

### 3.1 Spec 字段

- `image` (string): sandbox 使用的镜像。
- `resources`:
  - `cpu` (string): 单个 sandbox 期望的 CPU 配额（如 "500m"）。
  - `memory` (string): 单个 sandbox 期望的内存配额（如 "1Gi"）。
- `ttlSeconds` (*int32): sandbox 的存活时长上限（可选）。
- `command` ([]string): 容器启动命令（可选）。
- `args` ([]string): 容器启动参数（可选）。
- `env` ([]EnvVar): 环境变量（可选）。
- `port` (int): sandbox 在 Agent Pod 网络下监听的端口，或者为 0 时由 Agent 分配。
- `affinityHints`（可选）:
  - `nodeSelector` (map[string]string): 节点选择偏好。
  - `zone` (string): 机房/可用区偏好等。

### 3.2 Status 字段

- `phase` (string):
  - `Pending` | `Scheduling` | `Allocating` | `Running` | `Failed` | `Succeeded` | `Expired`。
- `assignedAgentPod` (LocalObjectReference): 选中的 Agent Pod（namespace/name）。
- `nodeName` (string): Agent 所在节点。
- `sandboxID` (string): Agent 内部标识的 sandbox ID（容器 ID 或逻辑 ID）。
- `address` (string): 访问地址，形如 `<AgentPodIP:Port>`。
- `conditions` ([]Condition): 记录失败原因、超时等。

## 4. Controller 内部模块

Controller 作为 Operator，使用 controller-runtime 管理 Reconciler 和 client。

### 4.1 SandboxClaimReconciler

职责：
- Watch `SandboxClaim`，根据 `spec` 和当前 Agent 状态执行：
  - Pending -> Scheduling：调用 Scheduler 选择合适 Agent。
  - Scheduling -> Allocating：通过 RPC 调用 Agent 创建 sandbox。
  - Allocating -> Running：写回 sandboxID 和 address。
  - TTL 到期或用户删除时触发回收流程，调用 Agent 销毁 sandbox。

主要状态流转：
1. `Pending`：新建的 SandboxClaim，等待调度。
2. `Scheduling`：已开始调度，正在选择 Agent。
3. `Allocating`：已分配 Agent，正在创建 sandbox 容器。
4. `Running`：sandbox 已创建并可用。
5. `Failed/Succeeded/Expired`：终态，记录原因或 TTL 到期。

### 4.2 AgentRegistry（内存结构）

- 线程安全地维护所有在线 Agent 的信息：
  - `AgentID`
  - `Namespace`
  - `PodName`
  - `PodIP`
  - `NodeName`
  - `Capacity`：逻辑最大可承载 sandbox 数，允许 oversubscribe 算法设定。
  - `Allocated`：当前已分配 sandbox 数。
  - `Images`：该节点已有镜像列表（由 Agent 上报）。
  - `LastHeartbeat`：最近心跳时间。
- 提供接口：
  - 注册/更新 Agent。
  - 查询所有/单个 Agent。
  - 按 image 查询支持该镜像的 Agent。
  - 分配/释放 slot（更新 Allocated）。
- 定期清理心跳超时的 Agent，将其从调度池中剔除。

### 4.3 Image 索引

- 基于 AgentRegistry 中的 `Images` 字段，构建内存中的 `image -> Agents` 反向索引。
- 仅包含有 Agent 的节点上的镜像信息，不尝试覆盖全集群。
- 调度时优先使用有该镜像的 Agent，没有时可以退化为只按资源选择。

### 4.4 Scheduler（镜像亲和 + oversubscribe）

输入：SandboxClaim 的 spec。
输出：一个选中的 AgentID 或错误。

调度步骤：
1. 从 AgentRegistry 获取所有健康 Agent（心跳正常）。
2. 按照是否拥有目标镜像分为两类：
   - 有该镜像的 Agent。
   - 没有该镜像但资源可用的 Agent（作为 fallback）。
3. 对每个候选 Agent 计算评分：
   - 有目标镜像：加权高优先级。
   - 剩余 logical capacity 越大越好。
   - oversubscribe 控制：
     - 当 `Allocated / Capacity` 超过某阈值时降权或过滤，以防过载。
4. 选择得分最高的 AgentID 返回；若没有候选，则返回调度失败，Reconciler 稍后重试或交给 AgentPool 扩容逻辑。

### 4.5 AgentPool / 扩缩容（可选后续扩展）

- 根据当前 Agent 的 `Capacity/Allocated` 和 Pending 的 SandboxClaim 数量，决定是否：
  - 创建新的 Agent Pod（可加 nodeAffinity 指向某些节点）。
  - 缩减长期闲置的 Agent Pod。
- 使用 controller-runtime Client 直接创建/更新对应的 Deployment 或 Pod 对象。

## 5. Agent 设计

Agent 运行在预先调度好的 Pod 内，负责与 containerd 交互并执行 Controller 的指令。

### 5.1 Agent Pod 形态与权限

- Pod 配置：
  - 通过 hostPath 挂载宿主机 `/run/containerd/containerd.sock` 等。
  - 可能需要特权容器或额外权限，以便操作 cgroup 和 netns（具体权限按实际实现确定）。
- Pod 的资源 request/limit 由调度时决定，用于控制该 Pod 作为资源池的大小。

### 5.2 Agent 主流程

1. 启动时读取配置：
   - `POD_NAME`、`POD_NAMESPACE`、`NODE_NAME` 等。
   - `CONTROLLER_ADDR`：Controller 的 RPC 地址。
   - 每个 sandbox 默认资源和最大并发 slot 策略。
2. 初始化：
   - containerd client。
   - SandboxManager：封装 sandbox 容器的创建/销毁逻辑。
   - ControllerClient：封装与 Controller 的 RPC 通信。
3. 注册：
   - 调用 Controller 的 `Register` RPC 上报：
     - AgentID（可以由 Agent 自生成）。
     - PodName/PodIP/NodeName。
     - 初始 Capacity 和当前镜像列表。
4. 心跳：
   - 周期性发送 `Heartbeat` RPC：
     - 当前运行中的 sandbox 数。
     - 镜像列表（如有变化）。
5. 接收创建/销毁 sandbox 指令：
   - 暴露 http Server 给 Controller：
     - `CreateSandbox`：根据参数创建 sandbox 容器。
     - `DestroySandbox`：销毁指定 sandbox。

### 5.3 SandboxManager 与 containerd 集成

- 负责：
  - 从 `/proc/self/cgroup` 推导当前 Agent Pod 的 cgroup 路径。
  - 从 `/proc/self/ns/net` 获取当前 netns 作为 sandbox 容器的网络 namespace。
  - 使用 containerd：
    - 为指定 image 创建 container + task。
    - 将容器进程加入当前 Pod 的 cgroup 和 netns。
- 内部维护 `sandboxID -> metadata` 映射：
  - 包含容器 ID、PID、监听端口、关联的 SandboxClaim UID 等。

## 6. Controller <-> Agent RPC 协议（概念）

Controller 与 Agent 通过 http 通信

- `Register(RegisterRequest) -> RegisterResponse`
  - Agent 上报自身信息和初始状态，Controller 在内存中注册 Agent。
- `Heartbeat(HeartbeatRequest) -> HeartbeatResponse`
  - Agent 周期性上报当前运行 sandbox 数、镜像列表等。
- `CreateSandbox(CreateSandboxRequest) -> CreateSandboxResponse`
  - Controller 在调度后调用该 RPC 请求 Agent 创建 sandbox 容器。
  - 返回 `sandbox_id` 和实际监听的 `port`。
- `DestroySandbox(DestroySandboxRequest) -> DestroySandboxResponse`
  - Controller 请求销毁一个 sandbox。

Controller 在成功调用 `CreateSandbox` 后，将：
- 使用 Agent 的 PodIP（来自 AgentRegistry）和返回的 `port` 构造 `<AgentPodIP:Port>`。
- 写入 SandboxClaim 的 `status.address`，供用户侧使用。

## 7. 项目目录结构（初版）

```text
fast-sandbox/
  cmd/
    controller/        # Operator 入口：controller-runtime manager
      main.go
    agent/             # Agent 入口
      main.go
  api/
    v1alpha1/
      sandboxclaim_types.go   # SandboxClaim CRD 类型
  internal/
    controller/
      sandboxclaim_controller.go   # Reconciler
      scheduler/
        scheduler.go              # 调度逻辑
        policy.go                 # 打分/oversubscribe 策略
      agentpool/
        registry.go               # AgentRegistry & image index
        heartbeat.go              # 心跳清理等
    agent/
      runtime/
        sandbox_manager.go        # sandbox 生命周期 & containerd 集成
        containerd_client.go      # containerd client 封装
        cgroup_netns.go           # cgroup/netns 处理
      client/
        controller_client.go      # 调用 Controller 的 RPC 客户端
      server/
        rpc_server.go             # Agent 暴露的 http Server
  config/
    crd/
    rbac/
    manager/
    samples/
  go.mod                          # Go 模块定义（后续初始化）
```

后续开发会按照上述结构依次实现：
- 初始化 go.mod 与基础依赖（controller-runtime 等）。
- 填充 SandboxClaim 类型与 Reconciler 骨架。
- 创建 Agent 程序的基础结构（注册/心跳骨架）。
- 逐步实现容器创建、调度策略等细节。