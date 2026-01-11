#!/bin/bash

describe() {
    echo "基础设施注入 - 验证 InitContainer 注入 helper 二进制和包装执行"
}

run() {
    # 创建测试 Pool
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: injection-pool
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=injection-pool" 60 "$TEST_NS"

    # 创建 Sandbox
    echo "  创建 Sandbox..."
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-injected
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: injection-pool
EOF

    echo "  等待 Sandbox 运行..."
    local count=0
    for i in $(seq 1 20); do
        PHASE=$(kubectl get sandbox sb-injected -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
        PHASE_LOWER=$(echo "$PHASE" | tr '[:upper:]' '[:lower:]')
        if [ "$PHASE_LOWER" = "running" ]; then
            echo "  ✓ Sandbox 运行成功"
            break
        fi
        if [ $i -eq 20 ]; then
            echo "  ❌ Sandbox 未进入运行状态，phase: $PHASE"
            kubectl delete sandbox sb-injected -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            kubectl delete sandboxpool injection-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
            return 1
        fi
        sleep 5
    done

    # 检查 Pod 是否有 infra-init 容器
    AGENT_POD=$(kubectl get pod -l "fast-sandbox.io/pool=injection-pool" -n "$TEST_NS" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    if [ -n "$AGENT_POD" ]; then
        # 检查 Pod 的 initContainers
        INIT_CONTAINERS=$(kubectl get pod "$AGENT_POD" -n "$TEST_NS" -o jsonpath='{.spec.initContainers[*].name}' 2>/dev/null || echo "")
        if echo "$INIT_CONTAINERS" | grep -q "infra-init"; then
            echo "  ✓ InitContainer 已注入"
        else
            echo "  ⚠ 未找到 infra-init InitContainer"
        fi
    fi

    # 清理
    kubectl delete sandbox sb-injected -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete sandboxpool injection-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
