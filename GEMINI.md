# Gemini 记忆快照 (Fast Sandbox)

## 核心架构 (Core Architecture)
- **Fast-Path 双模机制**: 
  - `Fast 模式`: Agent 先行，CRD 异步 (RT < 50ms)。Janitor 通过 `orphan-timeout` (默认 10s) 自动清理异步失败产生的孤儿。
  - `Strong 模式`: CRD 先行 (phase=Pending)，Agent 后行。
- **命令式驱动**: 废弃 `SyncSandboxes`，全链路采用 `CreateSandbox`/`DeleteSandbox` 接口。
- **内存权威性 (Registry)**: 采用“内存原子预留 + 启动时 Restore”模式。Restore 协程在 Controller 启动时通过扫描全量 CRD 重建状态。
- **调度策略**: 
  - 核心权重: 镜像亲和性 (Image Hit = +1000 分) > 负载均衡 (Allocated 计数)。
  - 触发机制: 监听 Agent Pod `Ready` 事件，精准唤醒 `Pending` 沙箱。

## 关键流程 (Key Workflows)
- **创建流**: `Registry.Allocate` (插槽+端口+镜像分) -> `Agent.Create` -> `K8s.Create/Update` (根据模式顺序不同)。
- **删除流**: `Finalizer` 保护 -> `Agent.Delete` -> `Registry.Release` -> `Remove Finalizer`。
- **Janitor 逻辑**: Informer 监听 Pod 消失 + 周期性 CRD 深度对账。判定准则: (物理有 & 逻辑无 & Age > 10s) 或 (UID 错配)。

## 测试标准 (Testing Standards)
- **套件化组织**: `01-basic-validation` 到 `05-advanced-features`。
- **故障模拟**: 使用 `ValidatingWebhook` 模拟 CRD 写入失败以验证自愈能力。
- **性能优化**: 环境变量 `SKIP_BUILD=true` 可加速回归。

## 开发者备忘 (Notes)
- KIND 环境 Cgroup v2 嵌套限制: 宿主机隔离采用 soft limits 策略。
- 端口管理: 沙箱间端口物理互斥，Status 自动回填 `Endpoints`。
- 插件注入: 必须满足路径白名单校验，使用 Wrapper 模式拦截启动命令。