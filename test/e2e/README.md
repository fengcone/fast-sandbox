# Fast Sandbox E2E Tests

端到端测试套件，验证 Fast Sandbox 的核心功能和用户场景。

## 测试套件结构

### 01-basic-validation (基础验证)

测试 CRD API 的基础校验和基本功能。

- `crd-validation.sh` - API 字段校验（必填字段、空值、枚举）
- `port-validation.sh` - 端口范围校验（0、65536 越界）
- `namespace-isolation.sh` - 命名空间隔离（同 NS 调度成功，跨 NS 拒绝）
- `env-workingdir.sh` - 环境变量和工作目录

### 02-scheduling-resources (调度与资源)

测试 Sandbox 调度逻辑和资源分配。

- `resource-slot.sh` - 容量限制（maxSandboxesPerPod=2，第3个被拒绝）
- `port-mutual-exclusion.sh` - 端口互斥调度（相同端口分配到不同 Pod）
- `autoscaling.sh` - Pool 自动扩缩容（poolMin=1→2）

### 03-lifecycle (生命周期)

测试 Sandbox 完整生命周期，包括用户最关心的创建-删除-重建场景。

- `basic-lifecycle.sh` - **核心测试**：创建→删除→同名重建完整流程（含循环测试）
- `graceful-shutdown.sh` - 优雅关闭流程（SIGTERM → Terminating → 删除）

### 04-cleanup-janitor (Janitor 与清理)

测试过期清理和 Janitor 功能。

- `namespace-aware.sh` - Janitor 正确处理非 default namespace
- `janitor-recovery.sh` - Janitor 孤儿容器清理

### 05-advanced-features (高级特性)

测试高级功能和特性。

- `fast-path.sh` - Fast/Strong 一致性模式、端口隔离、孤儿清理
- `goroutine-leak.sh` - Controller Goroutine 泄漏防护
- `snapshot-cleanup.sh` - 快照清理和同名重建（CLI 场景）

### 06-cli-integration (CLI 集成)

测试 CLI 工具与系统集成。

- `cli-cache.sh` - CLI 缓存机制
- `cli-logs.sh` - CLI 日志功能
- `update-reset.sh` - 更新和重置功能

### 07-fault-recovery (故障与恢复)

测试故障场景和恢复机制。

- `controlled-recovery.sh` - AutoRecreate 和 ResetRevision
- `auto-expiry.sh` - 自动过期（expireTime 触发）
- `memory-leak.sh` - Registry 内存泄漏防护

## 运行测试

### 运行所有测试套件

```bash
cd test/e2e
./common.sh
```

### 运行特定套件

```bash
cd test/e2e/01-basic-validation
./test.sh
```

### 运行特定测试（过滤）

```bash
cd test/e2e
./common.sh "lifecycle"
```

### 强制重建集群

```bash
FORCE_RECREATE_CLUSTER=true bash test/e2e/common.sh
```

## 调试技巧

### 查看 Controller 日志

```bash
kubectl logs -l app=fast-sandbox-controller -n fast-sandbox-system --tail=50
```

### 查看 Agent 日志

```bash
kubectl logs -l app=sandbox-agent -n <namespace> --all-containers --tail=50
```

### 查看 Sandbox 状态

```bash
kubectl get sandbox <name> -n <namespace> -o yaml
```

## 测试覆盖的核心场景

1. ✅ **创建 → 删除 → 重建同名 Sandbox**（用户报告的 bug 场景）
2. ✅ 容量限制和资源分配
3. ✅ 端口冲突和互斥调度
4. ✅ 命名空间隔离
5. ✅ 自动扩缩容
6. ✅ 优雅关闭和 Finalizer 清理
7. ✅ Agent 丢失恢复（AutoRecreate/Manual）
8. ✅ 自动过期
9. ✅ Janitor 孤儿清理
10. ✅ CLI 缓存和交互模式
11. ✅ Fast/Strong 一致性模式
12. ✅ 内存和 Goroutine 泄漏防护
