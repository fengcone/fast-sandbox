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
     - 维护 Agent Pod 资源池（含一定 buffer），可按镜像亲和创建新的 Agent Pod（通过 SandboxPool CRD 管理）。
     - 维护内存中的 AgentRegistry 和 image 索引（image -> Agents）。
     - 调度 SandboxClaim 到合适的 Agent Pod（镜像亲和 + 资源 + oversubscribe 策略）。
     - 通过 AgentControlLoop + HTTP Sandboxes 协议驱动 Agent 创建/销毁 sandbox 容器，更新 SandboxClaim 状态。

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
- `poolRef`（可选）:
  - 引用一个 `SandboxPool` 作为首选的 Agent 资源池。如果为 `nil`，则由调度器在所有可用池中自主选择。

### 3.2 Status 字段

- `phase` (string):
  - `Pending` | `Scheduling` | `Running` | `Failed` | `Succeeded` | `Expired`。
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
  - `PoolName`：所属的 SandboxPool 名称，用于调度约束。
  - `Capacity`：逻辑最大可承载 sandbox 数，允许 oversubscribe 算法设定。
  - `Allocated`：当前已分配 sandbox 数。
  - `Images`：该节点已有镜像列表（由 Agent 返回或同步）。
  - `LastSync`：最近一次与 Agent 成功同步 Sandboxes 的时间（实现上可复用 LastHeartbeat 字段）。
- 提供接口：
  - 注册/更新 Agent。
  - 查询所有/单个 Agent。
  - 按 image 查询支持该镜像的 Agent。
  - 按 pool 查询 Agent。
  - 分配/释放 slot（更新 Allocated）。
- 定期清理长时间未响应 Sandboxes 请求的 Agent，将其从调度池中剔除。

### 4.3 Image 索引

- 基于 AgentRegistry 中的 `Images` 字段，构建内存中的 `image -> Agents` 反向索引。
- 仅包含有 Agent 的节点上的镜像信息，不尝试覆盖全集群。
- 调度时优先使用有该镜像的 Agent，没有时可以退化为只按资源选择。

### 4.4 Scheduler（镜像亲和 + oversubscribe + Pool 约束）

输入：SandboxClaim 的 spec（包含可选的 `poolRef`）。
输出：一个选中的 AgentID 或错误。

调度步骤：
1. 从 AgentRegistry 获取所有健康 Agent。
2. 如果 `SandboxClaim.spec.poolRef` 不为 `nil`：
   - 仅在指定 `SandboxPool` 中的 Agent 里选择候选。
   - 否则，在所有 pool 的 Agent 中选择。
3. 按照是否拥有目标镜像分为两类：
   - 有该镜像的 Agent。
   - 没有该镜像但资源可用的 Agent（作为 fallback）。
4. 对每个候选 Agent 计算评分：
   - 有目标镜像：加权高优先级。
   - 剩余 logical capacity 越大越好。
   - oversubscribe 控制：
     - 当 `Allocated / Capacity` 超过某阈值时降权或过滤，以防过载。
5. 选择得分最高的 AgentID 返回；若没有候选，则返回调度失败，Reconciler 稍后重试或交给 SandboxPool 扩容逻辑。

### 4.5 SandboxPool / 扩缩容

- 通过 `SandboxPool` CRD 管理 Agent Pod 池：
  - 根据 `spec.capacity.poolMin/poolMax` 和当前 Pod 数量，保证池子总规模。
  - 根据 `spec.capacity.bufferMin/bufferMax` 和当前 idleAgents 数量，适度扩容或缩容空闲 Pod。
  - 创建/删除的 Pod 都基于 `agentTemplate` 生成，并打上 pool 的 label，与 `SandboxPool` 建立 ownerReference。
- 详情见本文档第 8 节 `SandboxPool CRD 设计`。

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
- Pod 的资源 request/limit 由 `SandboxPool.spec.agentTemplate` 决定，用于控制该 Pod 作为资源池的大小。

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
      sandboxpool_types.go    # SandboxPool CRD 类型
  internal/
    controller/
      sandboxclaim_controller.go   # Reconciler
      sandboxpool_controller.go    # SandboxPool 控制器
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

## 8. SandboxPool CRD 设计

`SandboxPool` 用于管理 Agent Pod 资源池，是一个 Namespaced CRD：

- `apiVersion: sandbox.fast.io/v1alpha1`
- `kind: SandboxPool`
- `scope: Namespaced`

### 8.1 Spec 字段

```yaml
spec:
  capacity:
    poolMin: 5        # 最小总 Pod 数
    poolMax: 50       # 最大总 Pod 数
    bufferMin: 5      # 期望的最小空闲 Agent 数（idleAgents 下限）
    bufferMax: 20     # 期望的最大空闲 Agent 数（idleAgents 上限）

  agentTemplate:      # 完整 PodTemplateSpec
    metadata:
      labels:
        sandbox.fast.io/role: agent
    spec:
      serviceAccountName: sandbox-agent
      containers:
        - name: agent
          image: your-registry/fast-sandbox-agent:v1
          imagePullPolicy: IfNotPresent
          resources:
            requests:
              cpu: "4"
              memory: "8Gi"
            limits:
              cpu: "4"
              memory: "8Gi"
          ports:
            - name: http
              containerPort: 8081
          env:
            - name: LOG_LEVEL
              value: "info"
          volumeMounts:
            - name: containerd-sock
              mountPath: /run/containerd
      volumes:
        - name: containerd-sock
          hostPath:
            path: /run/containerd
            type: Socket
      nodeSelector:
        sandbox-node-pool: "true"
      tolerations:
        - key: "sandbox"
          operator: "Exists"
          effect: "NoSchedule"
      affinity:
        # 可选：按 node/zone 分散
        podAntiAffinity: { }
```

字段语义说明：

- **capacity.poolMin**：池中最少要有多少个 Agent Pod，控制器会尽量保证 `currentPods >= poolMin`。
- **capacity.poolMax**：池中 Agent Pod 的最大数量，控制器不会创建超过该值的 Pod。
- **capacity.bufferMin**：逻辑上的“空闲 Agent 数”下限。空闲 Agent 定义为当前没有运行任何 sandbox 的 Agent。当 `idleAgents < bufferMin` 且 `currentPods < poolMax` 时，控制器倾向于扩容，创建新的 Pod。
- **capacity.bufferMax**：逻辑上的“空闲 Agent 数”上限。当 `idleAgents > bufferMax` 且 `currentPods > poolMin` 时，控制器会优先删除完全空闲的 Pod 以缩小池子。
- **agentTemplate**：完整的 PodTemplateSpec，用于创建 Agent Pod：
  - 用户可以通过此处配置镜像、资源规格、nodeSelector、tolerations、affinity、环境变量等。
  - 控制器在创建 Pod 时会额外注入：
    - `sandbox.fast.io/pool=<pool-name>` label。
    - ownerReference 指向对应的 `SandboxPool`。

### 8.2 Status 字段

```yaml
status:
  observedGeneration: 2

  # Pod 维度
  currentPods: 18      # 当前池内 Pod 总数
  readyPods: 17        # Ready 状态的 Pod 数

  # Agent 维度（结合 AgentRegistry 汇总）
  totalAgents: 17      # 实际在线的 Agent 数
  idleAgents: 6        # 当前 idle（无 sandbox）Agent 数
  busyAgents: 11       # 当前正在运行 sandbox 的 Agent 数

  conditions:
    - type: Available
      status: "True"
      reason: "SufficientIdleAgents"
      message: "idleAgents >= bufferMin"
    - type: Scaling
      status: "False"
      reason: "Stable"
```

说明：

- 不做 node 粒度统计，只保留整体视角。
- `currentPods/readyPods` 由控制器通过 List Pod 计算。
- `totalAgents/idleAgents/busyAgents` 来自 AgentRegistry + 最近一次 Sandboxes 同步结果。

### 8.3 SandboxPool 控制器行为（直接管理 Pods）

`SandboxPool` 控制器直接管理带有 label `sandbox.fast.io/pool=<pool-name>` 的 Pod：

1. **获取当前状态**：
   - 列出所有属于该 `SandboxPool` 的 Pod：带 `sandbox.fast.io/pool=<pool-name>` 且 ownerRef 指向该 `SandboxPool`。
   - 统计 `currentPods`、`readyPods`。
   - 从 AgentRegistry 中筛选 `poolName=<pool-name>` 的 Agent，统计 `totalAgents/idleAgents/busyAgents`。

2. **更新 Status**：
   - 写入 `observedGeneration`、上述统计字段以及 `conditions`。

3. **伸缩决策**：
   - **保证最小池大小**：
     - 若 `currentPods < poolMin` 且资源允许：创建 `poolMin - currentPods` 个新 Pod（上限 clamp 到 `poolMax`）。
   - **空闲不足（扩容）**：
     - 若 `idleAgents < bufferMin` 且 `currentPods < poolMax`：
       - 估算缺口 `needed = bufferMin - idleAgents`。
       - 创建最多 `min(needed, poolMax - currentPods)` 个 Pod。
   - **空闲过多（缩容）**：
     - 若 `idleAgents > bufferMax` 且 `currentPods > poolMin`：
       - 计算 `toDrop = min(idleAgents - bufferMax, currentPods - poolMin)`。
       - 从完全 idle 的 Pod 中挑选 `toDrop` 个删除（避免影响正在运行 sandbox 的 Pod）。

4. **安全性约束**：
   - 只删除当前没有运行 sandbox 的 Pod（通过 AgentRegistry 中的 `runningSandboxCount`/idle 状态判定）。
   - 当 idle Pod 数不够缩到 `bufferMax` 目标时，只缩到所有 idle Pod 删除完为止。

## 9. 开发计划

结合以上设计，后续开发分阶段推进：

### 9.1 第一阶段：CRD 与类型定义

1. 在 `api/v1alpha1/` 中新增 `sandboxpool_types.go`：
   - 定义 `SandboxPoolSpec`（含 `capacity` 和 `agentTemplate`）。
   - 定义 `SandboxPoolStatus`（含统计字段与 conditions）。
   - 定义 `SandboxPool` / `SandboxPoolList` 并注册到 Scheme。
2. 在 `sandboxclaim_types.go` 中增加：
   - `PoolReference` 结构体（`name/namespace`）。
   - `SandboxClaimSpec` 中新增 `PoolRef *PoolReference` 字段（omitempty）。

### 9.2 第二阶段：控制器骨架

3. 在 `internal/controller/` 中新增 `sandboxpool_controller.go`：
   - 基本 Reconciler：Watch `SandboxPool`，构建控制循环骨架。
   - 仅实现对 `SandboxPool` 对象的读取和 Status 更新的空逻辑（返回 ctrl.Result{}）。
4. 在 `cmd/controller/main.go` 中：
   - 注册 `SandboxPool` CRD 的 Scheme。
   - 初始化并注册 `SandboxPoolReconciler`。

### 9.3 第三阶段：SandboxPool 控制逻辑

5. 在 `sandboxpool_controller.go` 中实现：
   - 基于 label 和 ownerReference 列出池内 Pod，统计 `currentPods/readyPods`。
   - 调用 AgentRegistry 汇总 `totalAgents/idleAgents/busyAgents`（按 poolName 过滤）。
   - 填写 `status` 并持久化。
6. 实现池的伸缩逻辑：
   - 根据 `spec.capacity` 和当前统计值，创建/删除 Pod。
   - 创建 Pod 时使用 `spec.agentTemplate`，并注入必要 label 和 ownerReference。

### 9.4 第四阶段：Scheduler 与 AgentControlLoop 适配

7. 修改 Scheduler：
   - 支持使用 `SandboxClaim.spec.poolRef` 过滤候选 Agent。
   - 优先在指定 `SandboxPool` 中查找 Agent；无 poolRef 则全局处理。
8. 确认 AgentControlLoop 中：
   - 能够按 Agent 所属 pool 做必要的调试和限流（如按 pool 分批同步）。

### 9.5 第五阶段：Agent 侧 Sandboxes 实现

9. 在 `internal/agent/runtime/` 中实现 `SandboxManager` 与 containerd 集成：
   - 支持创建/销毁 sandbox 容器。
   - 支持根据期望列表执行增删改。
10. 在 `internal/agent/server/rpc_server.go` 中：
    - 实现 `/api/v1/agent/sandboxes` 处理逻辑：
      - 解析期望列表。
      - 调用 `SandboxManager` 完成状态对齐。
      - 返回 `sandboxesStatus`、images、capacity 等。

### 9.6 第六阶段：集成测试与调试

11. 在 KIND 集群中：
    - 部署 `SandboxPool` CRD 与控制器。
    - 创建一个或多个 `SandboxPool` 实例，验证池化 Pod 行为（扩缩容、资源分布）。
    - 创建带/不带 `poolRef` 的 `SandboxClaim`，验证调度与 sandbox 创建流程。
12. 根据测试结果迭代调度策略、容量策略与错误处理逻辑。
