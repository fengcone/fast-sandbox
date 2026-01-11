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
    echo "  等待过期（30 秒）..."
    sleep 30

    # 验证 Sandbox 已被自动删除
    if ! kubectl get sandbox test-expiry -n "$TEST_NS" >/dev/null 2>&1; then
        echo "  ✓ Sandbox 已被自动垃圾回收"
    else
        echo "  ❌ 过期后 Sandbox 仍然存在"
        kubectl delete sandbox test-expiry -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        kubectl delete sandboxpool expiry-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    # 清理
    kubectl delete sandboxpool expiry-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
