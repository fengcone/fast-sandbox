#!/bin/bash

describe() {
    echo "按需自动扩缩容 - 验证 Pool 根据需求从 1 扩容到 2 个 Pod"
}

run() {
    # 创建测试 Pool
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: scale-pool
spec:
  capacity: { poolMin: 1, poolMax: 2 }
  maxSandboxesPerPod: 1
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=scale-pool" 60 "$TEST_NS"

    echo "  创建 2 个 Sandbox 触发扩容..."

    # 创建 2 个 Sandbox，每个 Pod 只能放 1 个
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-scale-1
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: scale-pool
---
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-scale-2
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: scale-pool
EOF

    echo "  等待 Pool 扩容到 2 个 Pod..."
    local count=0
    for i in $(seq 1 30); do
        COUNT=$(kubectl get pods -l "fast-sandbox.io/pool=scale-pool" -n "$TEST_NS" --no-headers 2>/dev/null | wc -l | tr -d ' ')
        if [ "$COUNT" -ge 2 ]; then
            echo "  ✓ Pool 成功扩容到 2 个 Pod"

            # 清理
            kubectl delete sandbox sb-scale-1 sb-scale-2 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            kubectl delete sandboxpool scale-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            return 0
        fi
        sleep 3
    done

    echo "  ❌ Pool 扩容失败，当前 Pod 数量: $COUNT"
    return 1
}
