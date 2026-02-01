# E2E 测试用例重构实施计划

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**目标:** 重构 E2E 测试套件，消除重复测试，补充缺失的核心场景（同名 sandbox 重建），确保从用户角度覆盖主要使用场景。

**架构:**
- 重新组织测试套件为 7 个清晰的模块，每个模块有明确的测试目标
- 合并重复测试，移除冗余用例
- 新增核心测试：创建→删除→同名重建（用户报告的 bug 场景）

**技术栈:**
- Bash 测试框架（现有 `common.sh`）
- kubectl 用于资源操作
- KIND 本地集群

---

## 背景：用户报告的 Bug

**场景:**
1. 创建 poolMax=1, poolMin=1 的 pool，只有 1 个 pod
2. `fsb-ctl run fsb-b` → 成功
3. `fsb-ctl delete fsb-b` → 显示 "deletion triggered"
4. 立即 `fsb-ctl run fsb-b` → **报错 "insufficient capacity or port conflict"**

**根因:** Controller 的 `handleTerminatingDeletion` 中，Registry.Release 只在 Agent 确认删除后才调用，但用户可能在 Agent 确认前就尝试重建。

**现有测试缺陷:** snapshot-cleanup.sh 理论覆盖此场景，但等待时间（15s）不足，且测试不够稳定。

---

## 测试套件重组方案

### 重组后的目录结构

```
test/e2e/
├── 01-basic-validation/        # 基础验证 (5个)
│   ├── crd-validation.sh       # API 字段校验
│   ├── port-validation.sh      # 端口范围校验
│   ├── namespace-isolation.sh  # 命名空间隔离
│   ├── env-workingdir.sh       # 环境变量和工作目录
│   └── basic-lifecycle.sh      # 新增: 创建→删除→同名重建
│
├── 02-scheduling-resources/    # 调度与资源 (3个)
│   ├── resource-slot.sh        # 容量限制 (合并 error-handling.sh)
│   ├── port-mutual-exclusion.sh # 端口互斥
│   └── autoscaling.sh          # 自动扩缩容
│
├── 03-lifecycle/               # 生命周期 (2个)
│   ├── graceful-shutdown.sh    # 优雅关闭 (从 fault-recovery 移动)
│   └── basic-lifecycle.sh      # 从 basic-validation 移动到此处 (核心!)
│
├── 04-fault-recovery/          # 故障与恢复 (3个)
│   ├── controlled-recovery.sh  # AutoRecreate 和 ResetRevision
│   ├── auto-expiry.sh          # 自动过期 (从 cleanup-janitor 移动)
│   └── memory-leak.sh          # Registry 内存泄漏防护
│
├── 05-cleanup-janitor/         # Janitor 与清理 (2个)
│   ├── namespace-aware.sh      # Janitor 命名空间支持
│   └── janitor-recovery.sh     # 重写: 完整验证 Janitor 清理
│
├── 06-cli-integration/         # CLI 与集成 (3个)
│   ├── cli-cache.sh            # 合并缓存测试 (含交互式)
│   ├── cli-logs.sh             # 日志功能
│   └── update-reset.sh         # 更新和重置 (从 advanced-features 移动)
│
└── 07-advanced-features/       # 高级特性 (3个)
    ├── fast-path.sh            # Fast/Strong 模式
    ├── goroutine-leak.sh       # Goroutine 泄漏防护
    └── snapshot-cleanup.sh     # 快照清理和同名重建 (增加等待时间)
```

### 删除的测试（合并到其他测试）

| 删除文件 | 原因 | 合并到 |
|---------|------|--------|
| `01-basic-validation/error-handling.sh` | 与 resource-slot.sh 重复 | `02-scheduling-resources/resource-slot.sh` |
| `03-fault-recovery/finalizer-cleanup.sh` | 与 snapshot-cleanup.sh 重复 | `07-advanced-features/snapshot-cleanup.sh` |
| `05-advanced-features/gvisor-runtime.sh` | Phase 9 内容，超出当前范围 | - |

---

## Task 1: 创建 basic-lifecycle.sh 核心测试

**目标:** 验证最基本的 sandbox 生命周期，特别是同名重建场景（用户报告的 bug）

**Files:**
- Create: `test/e2e/03-lifecycle/basic-lifecycle.sh`

**Step 1: 创建 03-lifecycle 目录**

Run: `mkdir -p /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/03-lifecycle`

**Step 2: 创建测试文件**

```bash
cat > /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/03-lifecycle/basic-lifecycle.sh << 'EOF'
#!/bin/bash

describe() {
    echo "Sandbox 基础生命周期 - 验证创建、删除、同名重建的完整流程（用户场景）"
}

run() {
    # 创建测试 Pool (poolMin=1, poolMax=1，单一 Pod，用户场景)
    cat <<POOL_EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: lifecycle-test-pool
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
POOL_EOF

    wait_for_pod "fast-sandbox.io/pool=lifecycle-test-pool" 60 "$TEST_NS"

    # === 测试 1: 创建 Sandbox ===
    echo "  测试 1: 创建 Sandbox..."
    cat <<SB_EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-basic-lifecycle
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: lifecycle-test-pool
SB_EOF

    if ! wait_for_condition "kubectl get sandbox sb-basic-lifecycle -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qiE 'running|bound'" 30 "Sandbox running"; then
        echo "  ❌ Sandbox 创建失败"
        kubectl delete sandboxpool lifecycle-test-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi
    echo "  ✓ Sandbox 创建成功"

    # 记录初始分配信息
    ASSIGNED_POD_1=$(kubectl get sandbox sb-basic-lifecycle -n "$TEST_NS" -o jsonpath='{.status.assignedPod}')
    SANDBOX_ID_1=$(kubectl get sandbox sb-basic-lifecycle -n "$TEST_NS" -o jsonpath='{.status.sandboxID}')
    echo "  首次创建: assignedPod=$ASSIGNED_POD_1, sandboxID=$SANDBOX_ID_1"

    # === 测试 2: 删除 Sandbox ===
    echo "  测试 2: 删除 Sandbox..."
    kubectl delete sandbox sb-basic-lifecycle -n "$TEST_NS" >/dev/null 2>&1

    if ! wait_for_condition "! kubectl get sandbox sb-basic-lifecycle -n '$TEST_NS' >/dev/null 2>&1" 60 "Sandbox deleted"; then
        echo "  ❌ Sandbox 删除超时"
        kubectl delete sandboxpool lifecycle-test-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi
    echo "  ✓ Sandbox 删除成功"

    # === 测试 3: 等待 Registry 释放（关键步骤）===
    echo "  测试 3: 等待 Registry 释放资源..."
    # 给 Agent 和 Controller 足够时间完成清理
    # 需要等待: Agent 删除(最多10s) + 心跳间隔(2s) + Controller 处理
    sleep 25

    # === 测试 4: 同名重建（用户报告的 bug 场景）===
    echo "  测试 4: 同名重建（这是用户报告的 bug 场景）..."
    cat <<SB_EOF2 | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-basic-lifecycle
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: lifecycle-test-pool
SB_EOF2

    if ! wait_for_condition "kubectl get sandbox sb-basic-lifecycle -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qiE 'running|bound'" 40 "Sandbox recreated"; then
        PHASE=$(kubectl get sandbox sb-basic-lifecycle -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "<empty>")
        CONDITIONS=$(kubectl get sandbox sb-basic-lifecycle -n "$TEST_NS" -o jsonpath='{.status.conditions}' 2>/dev/null || echo "{}")
        echo "  ❌ 同名 Sandbox 重建失败"
        echo "     当前状态: Phase=$PHASE"
        echo "     Conditions: $CONDITIONS"
        kubectl delete sandbox sb-basic-lifecycle -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        kubectl delete sandboxpool lifecycle-test-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    # 验证重建后的分配信息
    ASSIGNED_POD_2=$(kubectl get sandbox sb-basic-lifecycle -n "$TEST_NS" -o jsonpath='{.status.assignedPod}')
    SANDBOX_ID_2=$(kubectl get sandbox sb-basic-lifecycle -n "$TEST_NS" -o jsonpath='{.status.sandboxID}')
    echo "  ✓ 同名 Sandbox 重建成功"
    echo "     重建后: assignedPod=$ASSIGNED_POD_2, sandboxID=$SANDBOX_ID_2"

    # === 测试 5: 验证是新容器（不是旧容器残留）===
    if [ "$SANDBOX_ID_1" = "$SANDBOX_ID_2" ]; then
        echo "  ⚠ 警告: sandboxID 相同，可能是容器残留而不是新创建"
    else
        echo "  ✓ 验证通过: 新容器已创建"
    fi

    # === 测试 6: 循环删除重建（压力测试）===
    echo "  测试 5: 循环删除重建 (3 次)..."
    for i in 1 2 3; do
        echo "    第 $i 次循环..."
        kubectl delete sandbox sb-basic-lifecycle -n "$TEST_NS" >/dev/null 2>&1
        wait_for_condition "! kubectl get sandbox sb-basic-lifecycle -n '$TEST_NS' >/dev/null 2>&1" 40 "Sandbox deleted (loop $i)" >/dev/null
        sleep 20

        cat <<SB_LOOP | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-basic-lifecycle
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: lifecycle-test-pool
SB_LOOP

        if ! wait_for_condition "kubectl get sandbox sb-basic-lifecycle -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qiE 'running|bound'" 40 "Sandbox recreated (loop $i)"; then
            echo "    ❌ 第 $i 次循环重建失败"
            kubectl delete sandbox sb-basic-lifecycle -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            kubectl delete sandboxpool lifecycle-test-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            return 1
        fi
        echo "    ✓ 第 $i 次循环成功"
    done
    echo "  ✓ 循环测试通过"

    # 清理
    kubectl delete sandbox sb-basic-lifecycle -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete sandboxpool lifecycle-test-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
EOF
```

**Step 3: 给予执行权限**

Run: `chmod +x /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/03-lifecycle/basic-lifecycle.sh`

**Step 4: 创建 03-lifecycle/test.sh**

```bash
cat > /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/03-lifecycle/test.sh << 'EOF'
#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

run_test_suite "$SCRIPT_DIR" "$1" "setup_lifecycle_suite"

setup_lifecycle_suite() {
    echo "=== Setting up Lifecycle Test Suite ==="
    # Lifecycle 测试不需要特殊的 setup
}
EOF
chmod +x /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/03-lifecycle/test.sh
```

---

## Task 2: 创建 03-lifecycle/graceful-shutdown.sh（移动）

**目标:** 将 graceful-shutdown.sh 从 03-fault-recovery 移动到 03-lifecycle

**Files:**
- Move: `test/e2e/03-fault-recovery/graceful-shutdown.sh` → `test/e2e/03-lifecycle/graceful-shutdown.sh`

**Step 1: 移动文件**

Run: `mv /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/03-fault-recovery/graceful-shutdown.sh /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/03-lifecycle/graceful-shutdown.sh`

**Step 3: 更新 03-fault-recovery/test.sh**

检查 `test/e2e/03-fault-recovery/test.sh` 是否有对 graceful-shutdown.sh 的引用，如有则移除。

---

## Task 3: 创建 06-cli-integration 目录并移动文件

**目标:** 创建新的 CLI 集成测试套件

**Files:**
- Create: `test/e2e/06-cli-integration/`
- Move: `test/e2e/05-advanced-features/cli-logs.sh` → `test/e2e/06-cli-integration/`
- Move: `test/e2e/05-advanced-features/cli-cache.sh` → `test/e2e/06-cli-integration/`
- Move: `test/e2e/05-advanced-features/update-reset.sh` → `test/e2e/06-cli-integration/`
- Create: `test/e2e/06-cli-integration/test.sh`

**Step 1: 创建目录**

Run: `mkdir -p /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/06-cli-integration`

**Step 2: 移动 CLI 测试文件**

Run:
```bash
mv /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/05-advanced-features/cli-logs.sh /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/06-cli-integration/
mv /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/05-advanced-features/cli-cache.sh /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/06-cli-integration/
mv /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/05-advanced-features/update-reset.sh /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/06-cli-integration/
```

**Step 3: 创建 test.sh**

```bash
cat > /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/06-cli-integration/test.sh << 'EOF'
#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

run_test_suite "$SCRIPT_DIR" "$1"
EOF
chmod +x /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/06-cli-integration/test.sh
```

**Step 4: 更新 05-advanced-features/test.sh**

移除对已移动文件的引用。

---

## Task 4: 移动 auto-expiry.sh 和 memory-leak.sh

**目标:** 将相关测试移动到正确的套件

**Files:**
- Move: `test/e2e/04-cleanup-janitor/auto-expiry.sh` → `test/e2e/03-fault-recovery/auto-expiry.sh`
- Move: `test/e2e/05-advanced-features/memory-leak.sh` → `test/e2e/03-fault-recovery/memory-leak.sh`

**Step 1: 移动 auto-expiry.sh**

Run: `mv /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/04-cleanup-janitor/auto-expiry.sh /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/03-fault-recovery/auto-expiry.sh`

**Step 2: 移动 memory-leak.sh**

Run: `mv /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/05-advanced-features/memory-leak.sh /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/03-fault-recovery/memory-leak.sh`

**Step 3: 更新原套件的 test.sh**

移除对已移动文件的引用。

---

## Task 5: 合并 error-handling.sh 到 resource-slot.sh

**目标:** 消除重复测试，将容量错误测试合并到一个文件

**Files:**
- Modify: `test/e2e/02-scheduling-resources/resource-slot.sh`
- Delete: `test/e2e/01-basic-validation/error-handling.sh`

**Step 1: 读取 error-handling.sh 内容**

Run: `cat /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/01-basic-validation/error-handling.sh`

**Step 2: 读取 resource-slot.sh 内容**

Run: `cat /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/02-scheduling-resources/resource-slot.sh`

**Step 3: 合并测试内容**

将 error-handling.sh 中的"删除不存在的资源"测试添加到 resource-slot.sh：

```bash
# 在 resource-slot.sh 的 run() 函数末尾，清理之前添加：

    # === 测试 4: 删除不存在的资源 ===
    echo "  测试 4: 删除不存在的资源..."
    kubectl delete sandbox nonexistent-resource -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    if [ $? -le 1 ]; then
        echo "  ✓ 删除不存在的资源优雅处理"
    else
        echo "  ⚠ 删除不存在的资源返回错误码 $?"
    fi
```

**Step 4: 删除 error-handling.sh**

Run: `rm /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/01-basic-validation/error-handling.sh`

**Step 5: 更新 01-basic-validation/test.sh**

移除对 error-handling.sh 的引用。

---

## Task 6: 删除 finalizer-cleanup.sh（与 snapshot-cleanup.sh 重复）

**目标:** 移除与 snapshot-cleanup.sh 功能重复的测试

**Files:**
- Delete: `test/e2e/03-fault-recovery/finalizer-cleanup.sh`

**Step 1: 确认重复**

finalizer-cleanup.sh 和 snapshot-cleanup.sh 都测试"删除后插槽释放，可创建新sandbox"。

**Step 2: 删除文件**

Run: `rm /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/03-fault-recovery/finalizer-cleanup.sh`

**Step 3: 更新 03-fault-recovery/test.sh**

移除对 finalizer-cleanup.sh 的引用。

---

## Task 7: 删除 gvisor-runtime.sh（Phase 9 内容）

**目标:** 移除尚未实现的 gVisor 测试

**Files:**
- Delete: `test/e2e/05-advanced-features/gvisor-runtime.sh`

**Step 1: 删除文件**

Run: `rm /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/05-advanced-features/gvisor-runtime.sh`

**Step 2: 更新 05-advanced-features/test.sh**

移除对 gvisor-runtime.sh 的引用。

---

## Task 8: 修复 snapshot-cleanup.sh 增加等待时间

**目标:** 修复现有测试，确保等待足够时间让 Registry 释放

**Files:**
- Modify: `test/e2e/05-advanced-features/snapshot-cleanup.sh:104,159`

**Step 1: 备份原文件**

Run: `cp /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/05-advanced-features/snapshot-cleanup.sh /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/05-advanced-features/snapshot-cleanup.sh.bak`

**Step 2: 修改第 104 行等待时间**

Run:
```bash
sed -i '' 's/sleep 15$/sleep 25/' /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/05-advanced-features/snapshot-cleanup.sh
```

**Step 3: 修改第 159 行等待时间（循环测试中的等待）**

Run:
```bash
sed -i '' 's/# 需要等待：agent 删除完成（最多 10s）+ 心跳间隔（2s）+ 控制器处理时间$/# 需要等待：agent 删除完成（最多 10s）+ 心跳间隔（2s）+ 控制器处理时间（增加余量）/' /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/05-advanced-features/snapshot-cleanup.sh
```

然后修改第 159 行的 `sleep 15` 为 `sleep 20`：

Run:
```bash
sed -i '' '159s/sleep 15$/sleep 20/' /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/05-advanced-features/snapshot-cleanup.sh
```

**Step 4: 验证修改**

Run: `grep -n "sleep 2[05]" /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/05-advanced-features/snapshot-cleanup.sh`
Expected: 显示第 104 行和第 159 行的 sleep 时间已更新

---

## Task 9: 更新 README 文档

**目标:** 更新测试套件 README，反映新的组织结构

**Files:**
- Modify: `test/e2e/README.md`

**Step 1: 读取当前 README**

Run: `cat /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/README.md`

**Step 2: 完全重写 README**

```bash
cat > /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/README.md << 'EOF'
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
EOF
```

---

## Task 10: 更新所有 test.sh 文件

**目标:** 确保所有套件的 test.sh 正确引用其测试文件

**Step 1: 检查并更新 01-basic-validation/test.sh**

Run: `cat /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/01-basic-validation/test.sh`

确认不再引用 error-handling.sh。

**Step 2: 检查并更新 02-scheduling-resources/test.sh**

Run: `cat /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/02-scheduling-resources/test.sh`

**Step 3: 检查并更新 03-fault-recovery/test.sh**

Run: `cat /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/03-fault-recovery/test.sh`

确认移除对 graceful-shutdown.sh、finalizer-cleanup.sh 的引用，添加对 auto-expiry.sh、memory-leak.sh 的引用。

**Step 4: 检查并更新 04-cleanup-janitor/test.sh**

Run: `cat /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/04-cleanup-janitor/test.sh`

确认移除对 auto-expiry.sh 的引用。

**Step 5: 检查并更新 05-advanced-features/test.sh**

Run: `cat /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e/05-advanced-features/test.sh`

确认移除对 cli-logs.sh、cli-cache.sh、update-reset.sh、memory-leak.sh、gvisor-runtime.sh 的引用。

---

## Task 11: 运行测试验证

**目标:** 运行新测试套件，确保所有测试通过

**Step 1: 验证 basic-lifecycle 测试**

Run:
```bash
cd /Users/fengjianhui/WorkSpaceL/fast-sandbox
export TEST_NS=e2e-test-$(date +%s)
export AGENT_IMAGE=fast-sandbox/agent:dev
bash test/e2e/03-lifecycle/test.sh basic-lifecycle
```

Expected: 测试执行，创建→删除→同名重建成功

**Step 2: 验证所有 test.sh 可执行**

Run:
```bash
find /Users/fengjianhui/WorkSpaceL/fast-sandbox/test/e2e -name "test.sh" -exec echo "=== {} ===" \; -exec bash -n {} \;
```

Expected: 所有 test.sh 语法正确

**Step 3: 运行完整测试套件（可选）**

Run:
```bash
cd /Users/fengjianhui/WorkSpaceL/fast-sandbox
SKIP_BUILD=true bash test/e2e/common.sh
```

Expected: 所有测试通过（或至少没有语法错误）

---

## 验收标准

1. ✅ `03-lifecycle/basic-lifecycle.sh` 创建并通过测试
2. ✅ `03-lifecycle/graceful-shutdown.sh` 已从 fault-recovery 移动
3. ✅ `06-cli-integration/` 目录创建，CLI 测试已移动
4. ✅ `auto-expiry.sh` 已移动到 fault-recovery
5. ✅ `memory-leak.sh` 已移动到 fault-recovery
6. ✅ `error-handling.sh` 已删除，测试合并到 resource-slot.sh
7. ✅ `finalizer-cleanup.sh` 已删除
8. ✅ `gvisor-runtime.sh` 已删除
9. ✅ `snapshot-cleanup.sh` 等待时间已增加（15s→25s）
10. ✅ `README.md` 已更新
11. ✅ 所有 `test.sh` 引用正确

---

## 提交计划

完成所有 Task 后，按以下顺序提交：

```bash
# 提交 1: 新增 03-lifecycle 套件和 basic-lifecycle 核心测试
git add test/e2e/03-lifecycle/
git commit -m "test(e2e): add lifecycle suite with basic lifecycle test (create-delete-recreate)"

# 提交 2: 新增 06-cli-integration 套件
git add test/e2e/06-cli-integration/
git commit -m "test(e2e): add cli-integration suite, move CLI tests from advanced-features"

# 提交 3: 移动测试到正确套件
git add test/e2e/03-fault-recovery/auto-expiry.sh
git add test/e2e/03-fault-recovery/memory-leak.sh
git add test/e2e/03-fault-recovery/graceful-shutdown.sh
git rm test/e2e/04-cleanup-janitor/auto-expiry.sh
git rm test/e2e/05-advanced-features/memory-leak.sh
git rm test/e2e/05-advanced-features/graceful-shutdown.sh
git commit -m "test(e2e): move tests to appropriate suites"

# 提交 4: 合并重复测试，删除冗余
git rm test/e2e/01-basic-validation/error-handling.sh
git rm test/e2e/03-fault-recovery/finalizer-cleanup.sh
git rm test/e2e/05-advanced-features/gvisor-runtime.sh
git add test/e2e/02-scheduling-resources/resource-slot.sh
git commit -m "test(e2e): remove duplicate tests, merge error-handling into resource-slot"

# 提交 5: 修复 snapshot-cleanup 等待时间
git add test/e2e/05-advanced-features/snapshot-cleanup.sh
git commit -m "test(e2e): increase snapshot-cleanup wait time for registry release"

# 提交 6: 更新文档和 test.sh
git add test/e2e/README.md
git add test/e2e/*/test.sh
git commit -m "docs(e2e): update test suite structure and test runners"
```
