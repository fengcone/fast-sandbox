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
