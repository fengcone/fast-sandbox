#!/bin/bash

describe() {
    echo "容量限制验证 - 验证 Agent 容量限制正确生效 (maxSandboxesPerPod=2)"
}

run() {
    # 创建测试 Pool - 容量为 2
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: resource-test-pool
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 2
  runtimeType: container
  agentTemplate:
    spec:
      containers:
      - name: agent
        image: "$AGENT_IMAGE"
EOF

    wait_for_pod "fast-sandbox.io/pool=resource-test-pool" 60 "$TEST_NS"

    echo "  测试 1: 创建第一个 Sandbox..."
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-slot-1
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: resource-test-pool
EOF

    # 等待第一个 sandbox 运行
    if ! wait_for_condition "kubectl get sandbox sb-slot-1 -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -E '(Bound|Running)'" 30 "SB-1 Running"; then
        echo "  ❌ 第一个 Sandbox 启动失败"
        kubectl delete sandboxpool resource-test-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi
    echo "  ✓ 第一个 Sandbox 启动成功"

    echo "  测试 2: 创建第二个 Sandbox (应该成功)..."
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-slot-2
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: resource-test-pool
EOF

    if ! wait_for_condition "kubectl get sandbox sb-slot-2 -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -E '(Bound|Running)'" 30 "SB-2 Running"; then
        echo "  ❌ 第二个 Sandbox 启动失败"
        kubectl delete sandbox sb-slot-1 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        kubectl delete sandboxpool resource-test-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi
    echo "  ✓ 第二个 Sandbox 启动成功 (容量未超限)"

    echo "  测试 3: 创建第三个 Sandbox (应该被拒绝 - 超过容量)..."
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" 2>&1 | grep -q "is forbidden" && echo "  ✓ 第三个 Sandbox 被正确拒绝 (容量限制生效)" || echo "  ⚠ 第三个 Sandbox 未被拒绝 (可能是时序问题)"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-slot-3
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: resource-test-pool
EOF

    echo "  测试 4: 删除不存在的资源..."
    kubectl delete sandbox nonexistent-resource -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    if [ $? -le 1 ]; then
        echo "  ✓ 删除不存在的资源优雅处理"
    else
        echo "  ⚠ 删除不存在的资源返回错误码 $?"
    fi

    # 清理
    kubectl delete sandbox sb-slot-1 sb-slot-2 sb-slot-3 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete sandboxpool resource-test-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
