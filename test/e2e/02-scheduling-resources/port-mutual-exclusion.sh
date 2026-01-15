#!/bin/bash

describe() {
    echo "端口互斥调度 - 验证相同端口的 Sandbox 被调度到不同 Pod"
}

run() {
    # 创建测试 Pool (设置 poolMin: 2 确保有足够的 Pod)
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: port-test-pool
spec:
  capacity: { poolMin: 2, poolMax: 2 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=port-test-pool" 60 "$TEST_NS"

    # 等待两个 Agent Pod 都就绪
    echo "  等待 2 个 Agent Pod 就绪..."
    local count=0
    for i in $(seq 1 30); do
        COUNT=$(kubectl get pods -l "fast-sandbox.io/pool=port-test-pool" -n "$TEST_NS" --no-headers 2>/dev/null | wc -l | tr -d ' ')
        if [ "$COUNT" -ge 2 ]; then
            echo "  ✓ 2 个 Agent Pod 就绪"
            break
        fi
        sleep 2
    done

    if [ "$COUNT" -lt 2 ]; then
        echo "  ❌ Agent Pod 就绪超时，当前数量: $COUNT"
        return 1
    fi

    # 创建 Sandbox A，使用端口 8080
    echo "  调度 Sandbox A (端口 8080)..."
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-port-a
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: port-test-pool
  exposedPorts: [8080]
EOF

    # 等待 Sandbox A 分配完成
    if ! wait_for_condition "kubectl get sandbox sb-port-a -n '$TEST_NS' -o jsonpath='{.status.assignedPod}' 2>/dev/null | grep -q '.'" 30 "Sandbox A assigned"; then
        echo "  ❌ Sandbox A 分配超时"
        return 1
    fi

    POD_A=$(kubectl get sandbox sb-port-a -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")
    if [ -z "$POD_A" ]; then
        echo "  ❌ Sandbox A 分配失败"
        return 1
    fi
    echo "  Sandbox A 分配到 Pod: $POD_A"

    # 创建 Sandbox B，请求相同端口
    echo "  调度 Sandbox B (端口 8080)..."
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-port-b
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: port-test-pool
  exposedPorts: [8080]
EOF

    echo "  等待 Sandbox B 调度完成..."
    if ! wait_for_condition "kubectl get sandbox sb-port-b -n '$TEST_NS' -o jsonpath='{.status.assignedPod}' 2>/dev/null | grep -q '.'" 60 "Sandbox B assigned"; then
        echo "  ❌ Sandbox B 分配超时"
        return 1
    fi

    POD_B=$(kubectl get sandbox sb-port-b -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")
    if [ -z "$POD_B" ]; then
        echo "  ❌ Sandbox B 分配失败"
        return 1
    fi
    echo "  Sandbox B 分配到 Pod: $POD_B"

    # 验证它们在不同的 Pod 上
    if [ "$POD_A" = "$POD_B" ]; then
        echo "  ❌ 端口冲突！两个 Sandbox 被调度到同一个 Pod: $POD_A"
        return 1
    fi
    echo "  ✓ 端口互斥验证成功，Sandbox 在不同 Pod 上"

    # 检查 Endpoint 状态
    ENDPOINT_A=$(kubectl get sandbox sb-port-a -n "$TEST_NS" -o jsonpath='{.status.endpoints[0]}' 2>/dev/null || echo "")
    if [[ "$ENDPOINT_A" == *":8080" ]]; then
        echo "  ✓ Endpoint 状态正确填充: $ENDPOINT_A"
    else
        echo "  ⚠ Endpoint 状态: $ENDPOINT_A"
    fi

    # 清理
    kubectl delete sandbox sb-port-a sb-port-b -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete sandboxpool port-test-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
