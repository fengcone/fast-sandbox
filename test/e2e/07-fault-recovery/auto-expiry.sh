#!/bin/bash

describe() {
    echo "自动过期回收 - 验证带 expireTime 的 Sandbox 自动被垃圾回收"
}

run() {
    # 创建测试 Pool
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: expiry-pool
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=expiry-pool" 60 "$TEST_NS"

    # 计算 20 秒后的过期时间
    # macOS date 命令语法
    if date -v+20S >/dev/null 2>&1; then
        EXPIRY_TIME=$(date -u -v+20S +"%Y-%m-%dT%H:%M:%SZ")
    else
        # Linux GNU date
        EXPIRY_TIME=$(date -u -d "+20 seconds" +"%Y-%m-%dT%H:%M:%SZ")
    fi

    echo "  创建带过期时间的 Sandbox (20 秒后过期)..."
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: test-expiry
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: expiry-pool
  expireTime: "$EXPIRY_TIME"
EOF

    sleep 10
    echo "  等待过期 (最多等待 60s)..."
    local expired=false
    for i in $(seq 1 12); do
        PHASE=$(kubectl get sandbox test-expiry -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
        if [ "$PHASE" = "Expired" ]; then
            expired=true
            break
        fi
        echo "  检查 $i: Sandbox Phase=$PHASE..."
        sleep 5
    done

    if [ "$expired" = "true" ]; then
        echo "  ✓ Sandbox 已过期，CRD 保留用于查询"

        # 验证 CRD 仍然存在
        if kubectl get sandbox test-expiry -n "$TEST_NS" >/dev/null 2>&1; then
            echo "  ✓ CRD 保留成功"
        else
            echo "  ✗ CRD 被意外删除"
            return 1
        fi

        # 验证状态字段
        PHASE=$(kubectl get sandbox test-expiry -n "$TEST_NS" -o jsonpath='{.status.phase}')
        ASSIGNED_POD=$(kubectl get sandbox test-expiry -n "$TEST_NS" -o jsonpath='{.status.assignedPod}')
        SANDBOX_ID=$(kubectl get sandbox test-expiry -n "$TEST_NS" -o jsonpath='{.status.sandboxID}')

        if [ "$PHASE" = "Expired" ] && [ "$ASSIGNED_POD" = "" ] && [ "$SANDBOX_ID" = "" ]; then
            echo "  ✓ 状态字段正确: Phase=Expired, assignedPod 和 sandboxID 已清空"
        else
            echo "  ✗ 状态字段不正确: Phase=$PHASE, assignedPod=$ASSIGNED_POD, sandboxID=$SANDBOX_ID"
            return 1
        fi
    else
        echo "  ❌ 过期后 Sandbox 状态未变为 Expired"
        kubectl get sandbox test-expiry -n "$TEST_NS" -o yaml 2>/dev/null | tail -20
        kubectl delete sandbox test-expiry -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    # 清理 - 手动删除过期的 CRD
    echo "  清理过期的 CRD..."
    kubectl delete sandbox test-expiry -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    # 清理
    kubectl delete sandboxpool expiry-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
