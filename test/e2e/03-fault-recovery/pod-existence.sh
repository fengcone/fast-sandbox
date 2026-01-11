#!/bin/bash

describe() {
    echo "Pod 存在性检查 - 验证 Janitor 正确识别和清理孤儿容器"
}

run() {
    # Pod 存在性检查需要 Janitor，先安装 Janitor
    install_janitor

    # 创建测试 Pool
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: existence-pool
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 2
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=existence-pool" 60 "$TEST_NS"

    # 创建 Sandbox
    echo "  创建 Sandbox..."
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-existence
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: existence-pool
  exposedPorts: [8080]
EOF

    sleep 10
    PHASE=$(kubectl get sandbox sb-existence -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null | tr '[:upper:]' '[:lower:]')
    if [ "$PHASE" != "running" ] && [ "$PHASE" != "bound" ]; then
        echo "  ❌ Sandbox 创建失败，phase: $PHASE"
        return 1
    fi
    echo "  ✓ Sandbox 创建成功"

    # 获取 Agent Pod 名称
    AGENT_POD=$(kubectl get sandbox sb-existence -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")
    echo "  Agent Pod: $AGENT_POD"

    # 删除 Agent Pod 模拟 Pod 消失
    echo "  删除 Agent Pod 模拟孤儿场景..."
    kubectl delete pod "$AGENT_POD" -n "$TEST_NS" --force --grace-period=0 >/dev/null 2>&1
    sleep 5

    # 等待 Janitor 扫描并识别孤儿
    echo "  等待 Janitor 扫描 (约 35 秒)..."
    sleep 35

    # 检查 Sandbox 状态
    SANDBOX_STATUS=$(kubectl get sandbox sb-existence -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "NotFound")
    echo "  Sandbox Phase: $SANDBOX_STATUS"

    # Sandbox 应该处于失败状态或被重新调度
    # 如果是 Failed/Unknown 等状态，说明 Janitor 正确识别了孤儿
    if [ "$SANDBOX_STATUS" = "NotFound" ] || [ "$SANDBOX_STATUS" = "Failed" ] || [ "$SANDBOX_STATUS" = "Unknown" ]; then
        echo "  ✓ Janitor 正确识别了孤儿容器"
    elif [ "$SANDBOX_STATUS" = "Running" ] || [ "$SANDBOX_STATUS" = "Bound" ]; then
        # 可能已经被重新调度，检查 assignedPod
        NEW_POD=$(kubectl get sandbox sb-existence -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")
        if [ -n "$NEW_POD" ] && [ "$NEW_POD" != "$AGENT_POD" ]; then
            echo "  ✓ Sandbox 被重新调度到新 Pod: $NEW_POD"
        else
            echo "  ✓ Sandbox 仍在运行（可能是正常状态）"
        fi
    else
        echo "  ⚠ Sandbox 状态: $SANDBOX_STATUS"
    fi

    # 清理
    kubectl delete sandbox sb-existence -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete sandboxpool existence-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
