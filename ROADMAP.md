# Fast Sandbox 演进蓝图 (Roadmap)

本文档记录了 Fast Sandbox 项目的后续开发计划。

## 0. 基础设施与测试 (Infrastructure & Testing) - P0 (最高优先级)

### 0.1 E2E 测试工业级重构
*   **目标**: 提升测试的可靠性、独立性和自动化程度，像对待生产代码一样对待测试。
*   **整理原则**:
    1.  **Case 目录化**: 每个独立的测试场景（如 `test-autoscaling`, `test-resource-quota`）拥有自己的独立文件夹。
    2.  **依赖自包含**: 测试 Case 依赖的所有资源描述（Manifests）必须存放在该 Case 文件夹内，禁止跨 Case 共享 Manifests（核心组件除外）。
    3.  **CRD 标准化**: 所有的 CRD 定义统一存放在项目根目录的 `@config/crd/` 目录下，测试脚本必须从此路径引用 CRD，严禁在测试目录中维护 CRD 副本。
    4.  **全生命周期闭环**: 脚本启动时需完成：集群选择、镜像构建、镜像导入、控制器部署、资源池预热；脚本结束时必须清理**所有**资源（CRD、Controller、Pods 等），确保环境零污染。
    5.  **描述性命名**: 目录命名必须清晰表达测试意图（如 `scale-up-on-demand`），避免使用 `core` 等模糊词汇。

---

## 1. 基础功能 (Core Essentials) - 提升稳定性与可用性

### 1.1 精细化资源配额 (Hard Resource Limits)
*   **目标**: 确保 Sandbox 严格受限于分配的资源，不影响宿主机和其他 Sandbox。
*   **任务**: 
    *   在 Agent 启动时读取 Pod 的真实资源限制 (via Downward API)。
    *   在创建 Sandbox 子 Cgroup 后，自动计算并写入 `cpu.max` / `memory.max` (Cgroup v2)。
    *   实现基于 Slot 权重的 CPU 分配逻辑。

### 1.2 端口管理与访问方案 (Network Port Management)
*   **目标**: 解决多 Sandbox 共享 Pod IP 导致的端口冲突。
*   **任务**:
    *   **动态端口池**: Agent 维护本地可用端口池，在创建 Sandbox 时通过环境变量 `SANDBOX_PORT` 动态分配。
    *   **内置反向代理**: Agent Pod 内部运行轻量级代理，根据请求头（如 `X-Sandbox-ID`）将流量转发至对应容器。

### 1.3 宿主机“清道夫” (Node Janitor DaemonSet)
*   **目标**: 清理 Agent 意外崩溃留下的孤儿资源。
*   **任务**:
    *   部署一个全局 DaemonSet，定期扫描 `fast-sandbox.io/managed` 标签。
    *   自动回收那些对应的 Agent Pod 已不存在的孤儿容器、Task 和 Snapshot。
    *   清理 `/run/containerd/fifo` 目录下的陈旧管道文件。

### 1.4 状态自愈与心跳摘除 (Self-healing)
*   **目标**: 快速感知节点故障并重调度。
*   **任务**:
    *   在 `Registry` 中增加存活检查（TTLs）。
    *   如果 Agent 超过 N 秒无心跳，Controller 自动将其标记为 `Offline`。
    *   自动重置 `Offline` 节点上的 Sandbox 状态为 `Pending` 触发重调度。

### 1.5 基础设施注入 (Infrastructure Injection)
*   **目标**: 在 Sandbox 内静默运行系统级辅助进程（用于监控、指令下发等）。
*   **方案**:
    *   **二进制透传**: Agent Pod 将预装的辅助二进制通过 `Mounts` 注入到容器内部固定目录（如 `/.fast-sandbox/helper`）。
    *   **静默启动**: 自动修改 OCI Spec，使用辅助二进制包装用户原始命令，确保辅助进程先于用户进程启动。
    *   **用户透明**: 这一过程在 Sandbox API 层面不暴露，属于基础设施层的统一行为。

### 1.6 沙箱生命周期自动过期 (Automatic Expiry)
*   **目标**: 自动清理长期运行或被遗忘的沙箱，释放资源。
*   **方案**:
    *   **到期监控**: `SandboxController` 监控 `Spec.ExpireTime` 字段。
    *   **自动回收**: 当系统时间超过 `ExpireTime` 时，Controller 自动执行沙箱删除逻辑（GC）。
    *   **资源闭环**: 触发缩容和 Registry 状态更新，确保“资源-生命周期-销毁”全自动化。

---

## 2. 进阶功能 (Advanced Features) - 追求极致速度与智能化

### 2.1 Fast-Path API (绕过 K8s 控制面延迟)
*   **目标**: 实现 < 50ms 的极速调度响应。
*   **方案**: 
    *   Controller 暴露高性能 gRPC/HTTP 接口 `POST /v1/sandbox`。
    *   **内存直调**: 调度器在内存中选定 Agent 并直接下发指令，不等待 K8s ETCD 写入。
    *   **异步落盘**: 容器跑起来后再异步补齐 `Sandbox` CRD 记录。

### 2.2 预测性镜像预热 (Predictive Image Pre-warming)
*   **目标**: 消除冷启动中的镜像拉取时间。
*   **方案**:
    *   分析历史请求，识别热点镜像。
    *   当 SandboxPool 扩容新 Agent 或周期性维护时，自动下发 `PullImage` 命令给所有 Agent。

### 2.3 状态快照与回滚 (Checkpoint & Restore - AI 的“后悔药”)
*   **目标**: 赋予 AI Agent 瞬间回滚到“已知好状态”的能力，提升复杂任务探索的成功率。
*   **方案**:
    *   **gVisor 原生快照**: 利用 gVisor (runsc) 的 Sentry 架构，将沙箱内进程的完整内存、变量和文件描述符 Dump 到磁盘。
    *   **分支尝试**: 支持从一个“黄金快照”并行恢复出多个沙箱副本，让 AI 执行不同的分支策略。
    *   **极速回滚**: 实现 < 300ms 的恢复速度，使用户和 AI 几乎感知不到状态切换。

### 2.4 流量触发式动态水位 (Flow-aware Scaling)
*   **目标**: 极致节能，实现 Scale-to-Zero。
*   **方案**:
    *   根据请求频率动态调整 `BufferMin`。
    *   低峰期缩容至 0，首个请求到达时同步激活资源池。

### 2.5 统一命令行工具 (kubectl-fs 插件)
*   **目标**: 提升开发者体验。
*   **方案**:
    *   开发 `kubectl-fs logs <sandbox-id>`：通过 Agent 流式回传容器日志。
    *   开发 `kubectl-fs exec <sandbox-id>`：建立经过 Agent 中转的 TTY 通道。

---

## 3. 架构抉择备忘

*   **隔离技术**: 默认使用 `runc` (Namespace/Cgroup)，对于高危代码强制切换至 `gVisor` 或 `Firecracker` (需环境支持)。
*   **调度哲学**: 优先保障速度（镜像局部性），其次保障资源平衡。
