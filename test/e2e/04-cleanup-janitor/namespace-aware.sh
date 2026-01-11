#!/bin/bash

describe() {
    echo "跨命名空间支持 - 验证 Janitor 正确处理非 default 命名空间的 Sandbox"
}

run() {
    # 创建第二个测试命名空间
    TEST_NS_2="${TEST_NS}-extra"
    kubectl create namespace "$TEST_NS_2" 2>/dev/null || true

    # 在第一个命名空间创建 Pool
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: ns-test-pool
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 2
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=ns-test-pool" 60 "$TEST_NS"

    # 在第一个命名空间创建 Sandbox
    echo "  在命名空间 $TEST_NS 创建 Sandbox..."
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-ns-test
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: ns-test-pool
  exposedPorts: [8080]
EOF

    sleep 10
    ASSIGNED_POD=$(kubectl get sandbox sb-ns-test -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")
    if [ -z "$ASSIGNED_POD" ]; then
        echo "  ❌ Sandbox 未被分配"
        kubectl delete namespace "$TEST_NS_2" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi
    echo "  ✓ Sandbox 分配到 Pod: $ASSIGNED_POD"

    # 等待 Janitor 扫描
    echo "  等待 Janitor 扫描周期..."
    sleep 35

    # 验证 Sandbox 仍然存在
    if kubectl get sandbox sb-ns-test -n "$TEST_NS" >/dev/null 2>&1; then
        echo "  ✓ 非 default 命名空间的 Sandbox 没有被错误清理"
    else
        echo "  ❌ Sandbox 被 Janitor 错误清理了"
        kubectl delete namespace "$TEST_NS_2" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    # 清理
    kubectl delete sandbox sb-ns-test -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete sandboxpool ns-test-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete namespace "$TEST_NS_2" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
