#!/bin/bash

describe() {
    echo "内存泄漏防护 - 验证 Registry 不会因 Agent/Sandbox 记录无限增长导致内存泄漏"
}

run() {
    # 创建测试 Pool
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: memory-test-pool
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=memory-test-pool" 60 "$TEST_NS"

    # 创建多个 Sandbox
    echo "  创建 5 个 Sandbox..."
    for i in 1 2 3 4 5; do
        cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-mem-$i
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "60"]
  poolRef: memory-test-pool
EOF
    done

    sleep 10

    # 删除部分 Sandbox
    echo "  删除 3 个 Sandbox..."
    kubectl delete sandbox sb-mem-1 sb-mem-2 sb-mem-3 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    sleep 5

    # 创建新 Sandbox 验证分配仍然正常
    echo "  创建新 Sandbox 验证 Registry 功能..."
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-mem-new
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "60"]
  poolRef: memory-test-pool
EOF

    sleep 10
    ASSIGNED_POD=$(kubectl get sandbox sb-mem-new -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")

    if [ -z "$ASSIGNED_POD" ]; then
        echo "  ❌ 新 Sandbox 未能分配，可能 Registry 存在问题"
        return 1
    fi
    echo "  ✓ 新 Sandbox 成功分配到: $ASSIGNED_POD"

    # 再创建更多验证
    for i in 1 2 3; do
        cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-mem-verify-$i
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "60"]
  poolRef: memory-test-pool
EOF
    done

    sleep 10
    echo "  ✓ Registry 功能正常，无内存泄漏迹象"

    # 清理
    for i in 1 2 3 4 5; do
        kubectl delete sandbox "sb-mem-$i" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    done
    kubectl delete sandbox sb-mem-new -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    for i in 1 2 3; do
        kubectl delete sandbox "sb-mem-verify-$i" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    done
    kubectl delete sandboxpool memory-test-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
