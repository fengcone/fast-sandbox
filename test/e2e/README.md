# Fast Sandbox E2E 测试指南

本目录包含 Fast Sandbox 的工业级端到端测试套件，旨在验证系统在各种极端场景下的可靠性、性能与自愈能力。

## 🧪 测试架构设计 (V2)

测试套件已重构为 5 个核心模块，覆盖从基础验证到高级故障注入的全生命周期。

1.  **套件化管理**: 所有测试按功能领域分组（如 `01-basic`, `05-advanced`）。
2.  **环境自愈**: `common.sh` 提供智能的资源清理与等待逻辑，支持 `FORCE_RECREATE_CLUSTER` 模式。
3.  **CLI 集成**: 高级测试直接集成 `kubectl-fastsb` 官方二进制，验证真实的 CLI 交互链路。
4.  **故障注入**: 通过 ValidatingWebhook 模拟 CRD 写入失败，验证 Janitor 的物理闭环能力。

## 📂 测试套件概览

| 套件目录 | 描述 | 关键测试点 |
| :--- | :--- | :--- |
| **01-basic-validation** | 基础功能验证 | CRD 字段校验、端口范围检查、错误处理机制 |
| **02-scheduling-resources** | 调度与资源 | 自动扩缩容、端口互斥调度、资源插槽(Slot)计算 |
| **03-fault-recovery** | 故障恢复 | Controller 崩溃恢复、Agent 失联自愈、Finalizer 闭环清理 |
| **04-cleanup-janitor** | 清理与运维 | 自动过期(Auto-Expiry)、跨命名空间支持、Janitor 孤儿回收 |
| **05-advanced-features** | 高级特性 | **Fast-Path (Fast/Strong) 双模**、CLI 集成、Webhook 故障注入、gVisor 运行时 |

## 🛠 如何运行测试

### 1. 运行单个套件 (推荐)
每个套件目录下的 `test.sh` 是独立的可执行入口。

```bash
# 运行基础验证
./test/e2e/01-basic-validation/test.sh

# 运行高级特性 (Fast-Path, CLI, Webhook)
./test/e2e/05-advanced-features/test.sh
```

### 2. 运行指定 Case
套件脚本支持传入参数以运行特定的 Sub-case（模糊匹配）。

```bash
# 只运行 Fast-Path 相关的测试
./test/e2e/05-advanced-features/test.sh fast-path
```

### 3. 全量回归
按顺序执行所有套件，确保系统整体健康。

```bash
# 依次运行 01 -> 05
export SKIP_BUILD=true  # 跳过重复构建，加速回归
for i in test/e2e/0*/test.sh; do $i; done
```

## ⚙️ 环境变量与调试

| 变量名 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `SKIP_BUILD` | `""` | 设为 `true` 可跳过 `docker build` 和 `kind load`，仅运行测试逻辑（前提是镜像已加载）。 |
| `FORCE_RECREATE_CLUSTER` | `false` | 设为 `true` 会在测试前**物理销毁并重建** KIND 集群，用于解决镜像缓存顽疾。 |
| `CLUSTER_NAME` | `fast-sandbox` | 指定 KIND 集群名称。 |

**示例：强制重建集群并运行高级测试**
```bash
export FORCE_RECREATE_CLUSTER=true
./test/e2e/05-advanced-features/test.sh
```

## 🔍 故障排查指南

1.  **Fast-Path 404 / Unimplemented**:
    通常是因为 Controller 镜像未更新。
    *   **解决**: 运行 `make build-controller-linux` 并 `kind load`，或者开启 `FORCE_RECREATE_CLUSTER=true`。

2.  **Janitor 清理超时**:
    Janitor 默认扫描间隔为 2分钟。E2E 环境中通过 `--scan-interval=10s` 加速。
    *   **检查**: `kubectl get ds -n fast-sandbox-system` 确认 Janitor 参数。

3.  **Pod Never Appeared**:
    通常发生在集群重建后，Controller 尚未完成 Leader Election。
    *   **解决**: `common.sh` 中的 `wait_for_pod` 已内置重试逻辑，确保 Controller 心跳同步完成后再继续。

## ⚠️ 开发原则
*   **Case 隔离**: 每个测试脚本(`*.sh`) 必须在退出时清理其创建的资源 (`trap cleanup EXIT`)。
*   **命名空间隔离**: 高级测试（如 Webhook）应使用独立的命名空间，避免误删共享资源。
*   **二进制一致性**: 测试脚本应优先使用 `bin/kubectl-fastsb` 等官方构建产物，而非临时 `go run`。