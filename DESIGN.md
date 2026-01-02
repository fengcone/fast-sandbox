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
- 不为 Agent 单独定义 CR，Agent 状态仅在 Controller 内存中维护（通过 HTTP 周期同步，Controller 主动与 Agent 通信）。

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
   - Controller 周期性或事件驱动地向 Agent 发送 Sandboxes 请求：
     - 请求体携带该 Agent 的期望 sandbox 列表（Desired State）。
     - 响应体携带当前运行 sandbox 状态、镜像列表、capacity 等（Observed State）。
   - Agent 不再主动向 Controller 发送心跳，只通过 HTTP 响应回报状态。
   
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
  - Pending -> Scheduling：调用 Scheduler 选择合适 Agent，写入 `assignedAgentPod`，表示期望该 Agent 承载此 sandbox。
  - Scheduling -> Running：由 Controller 的 AgentControlLoop 通过 Sandboxes 同步协议驱动，Agent 创建/更新实际容器后回报状态，Reconciler 将 `SandboxClaim.status` 更新为 Running。
  - TTL 到期或用户删除时触发回收流程，在期望状态中移除对应 sandbox，Agent 随后清理容器。

主要状态流转：
1. `Pending`：新建的 SandboxClaim，等待调度。
2. `Scheduling`：已分配 Agent，等待 Agent 根据期望状态拉起 sandbox。
3. `Running`：sandbox 已创建并可用。
4. `Failed/Succeeded/Expired`：终态，记录原因或 TTL 到期。

### 4.2 AgentRegistry（内存结构）

- 线程安全地维护所有在线 Agent 的信息：
  - `AgentID`
  - `Namespace`
  - `PodName`
  - `PodIP`
  - `NodeName`
  - `Capacity`：逻辑最大可承载 sandbox 数，允许 oversubscribe 算法设定。
  - `Allocated`：当前已分配 sandbox 数。
  - `Images`：该节点已有镜像列表（由 Agent 返回或同步）。
  - `LastSync`：最近一次与 Agent 成功同步 Sandboxes 的时间（实现上可复用 LastHeartbeat 字段）。
- 提供接口：
  - 注册/更新 Agent。
  - 查询所有/单个 Agent。
  - 按 image 查询支持该镜像的 Agent。
  - 分配/释放 slot（更新 Allocated）。
- 定期清理长时间未响应 Sandboxes 请求的 Agent，将其从调度池中剔除。

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

### 4.6 AgentControlLoop（Sandboxes 同步）

- 作为 Controller 内部的控制循环，对每个 Agent 周期性或事件驱动地执行：
  - 从 `SandboxClaim` 中收集该 Agent 的期望 sandboxes 列表（`assignedAgentPod` 为该 Agent）。
  - 通过 HTTP 调用 Agent 的 `/api/v1/agent/sandboxes`，发送期望 sandboxes（Desired State）。
  - 根据返回的 `sandboxesStatus` 更新对应 `SandboxClaim.status`，并刷新 AgentRegistry 中的 capacity、runningSandboxCount、images 以及最近同步时间。
- 可结合 workqueue，在 `SandboxClaim` 或 Agent 状态变化时立即触发同步；同时保留低频全量同步作为兜底，增强鲁棒性。

## 5. Agent 设计

Agent 运行在预先调度好的 Pod 内，负责与 containerd 交互并执行 Controller 的指令。

### 5.1 Agent Pod 形态与权限

- Pod 配置：
  - 通过 hostPath 挂载宿主机 `/run/containerd/containerd.sock` 等。
  - 可能需要特权容器或额外权限，以便操作 cgroup 和 netns（具体权限按实际实现确定）。
- Pod 的资源 request/limit 由调度时决定，用于控制该 Pod 作为资源池的大小。

### 5.2 Agent 主流程

1. 启动时读取配置：
   - `POD_NAME`、`POD_NAMESPACE`、`NODE_NAME` 等（用于标识自身）。
   - 每个 sandbox 默认资源和最大并发 slot 策略。
2. 初始化：
   - containerd client。
   - SandboxManager：封装 sandbox 容器的创建/销毁逻辑。
   - HTTP Server：暴露 `/api/v1/agent/sandboxes` 等接口给 Controller 调用。
3. 接收 Controller 的 Sandboxes 请求：
   - 对比请求中的期望 sandboxes 列表与本地实际运行的 sandbox 集合。
   - 对于需要新增的 sandbox：创建容器并加入当前 Pod 的 cgroup/netns。
   - 对于需要删除的 sandbox：停止并删除容器。
   - 汇总当前所有 sandbox 的状态、镜像列表、capacity 等，返回给 Controller。
4. （可选）本地 TTL 与清理：
   - 根据每个 sandbox 的 `ttlSeconds` 或内部策略执行超时清理。
   - 将过期/失败信息反映在返回的 `sandboxesStatus` 中。

### 5.3 SandboxManager 与 containerd 集成

- 负责：
  - 从 `/proc/self/cgroup` 推导当前 Agent Pod 的 cgroup 路径。
  - 从 `/proc/self/ns/net` 获取当前 netns 作为 sandbox 容器的网络 namespace。
  - 使用 containerd：
    - 为指定 image 创建 container + task。
    - 将容器进程加入当前 Pod 的 cgroup 和 netns。
- 内部维护 `sandboxID -> metadata` 映射：
  - 包含容器 ID、PID、监听端口、关联的 SandboxClaim UID 等。

## 6. Controller <-> Agent HTTP 协议（概念）

Controller 与 Agent 通过 HTTP 通信，以“期望/实际状态”对齐为核心：

- `POST /api/v1/agent/sandboxes`
  - **方向**：Controller -> Agent。
  - **Request**：包含该 Agent 的期望 sandbox 列表（Desired State），以及可选的 fullSync 标志。
  - **Response**：返回当前运行的 sandbox 状态列表、Agent 的 capacity、runningSandboxCount、images 等（Observed State）。
- （可选）首次启动或 Controller 重启时，可以发送空的期望列表，仅用于拉取 Agent 当前状态（同步现状）。

Controller 在收到 `sandboxesStatus` 后，将：
- 使用 Agent 的 PodIP 和返回的 `port` 构造 `<AgentPodIP:Port>`。
- 写入对应 `SandboxClaim` 的 `status.address` 和 `status.sandboxID` 等字段。
- 更新 AgentRegistry 中该 Agent 的 images/capacity/最近同步时间。
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
        registry.go               # AgentRegistry & image index（含最近同步时间）
      agentclient/
        client.go                 # Controller 侧调用 Agent 的 HTTP Sandboxes 接口
      agentserver/
        server.go                 # Controller 侧接收 Agent 上报（如注册/兼容用途）
    agent/
      runtime/
        sandbox_manager.go        # sandbox 生命周期 & containerd 集成
        containerd_client.go      # containerd client 封装
        cgroup_netns.go           # cgroup/netns 处理
      server/
        rpc_server.go             # Agent 暴露的 HTTP Sandboxes 接口
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