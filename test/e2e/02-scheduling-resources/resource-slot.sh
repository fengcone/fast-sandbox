#!/bin/bash

describe() {
    echo "资源插槽计算 - 验证 CPU/内存按插槽均分 (2000m / 2 slots = 1000m per slot)"
}

run() {
    # 创建测试 Pool
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
        resources:
          limits:
            cpu: "2000m"
            memory: "1Gi"
EOF

    wait_for_pod "fast-sandbox.io/pool=resource-test-pool" 60 "$TEST_NS"

    # 创建 Sandbox
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-slot-check
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: resource-test-pool
EOF

    echo "  检查 Agent 日志中的插槽计算..."
    local count=0
    for i in $(seq 1 20); do
        LOGS=$(kubectl logs -l "fast-sandbox.io/pool=resource-test-pool" -n "$TEST_NS" --tail=100 2>/dev/null || echo "")
        if echo "$LOGS" | grep -q "RESOURCES_VERIFY: Slot allocated for sb-slot-check: CPU=1000m"; then
            echo "  ✓ 插槽资源计算正确 (1000m CPU)"

            # 清理
            kubectl delete sandbox sb-slot-check -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            kubectl delete sandboxpool resource-test-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            return 0
        fi
        sleep 5
    done

    echo "  ❌ 未找到插槽计算日志"
    return 1
}
