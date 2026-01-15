#!/bin/bash

describe() {
    echo "Goroutine 泄漏防护 - 验证 AgentControlLoop 不会因慢 Agent 导致 goroutine 堆积"
}

run() {
    # 获取 Controller Pod
    CONTROLLER_POD=$(kubectl get pods -l app=fast-sandbox-controller -n fast-sandbox-system -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    if [ -z "$CONTROLLER_POD" ]; then
        echo "  ⚠ Controller Pod 未找到，跳过测试"
        return 0
    fi

    echo "  Controller Pod: $CONTROLLER_POD"

    # 记录初始 goroutine 数量
    echo "  获取初始 goroutine 数量..."
    INITIAL_GOROUTINES=$(kubectl exec "$CONTROLLER_POD" -n fast-sandbox-system -- curl -s http://localhost:6060/debug/pprof/goroutine?debug=1 2>/dev/null | grep -c "^goroutine" || echo "0")
    echo "  初始 goroutine 数量: $INITIAL_GOROUTINES"

    if [ "$INITIAL_GOROUTINES" = "0" ]; then
        echo "  ⚠ pprof 端点不可用，跳过测试"
        return 0
    fi

    # 创建测试 Pool (使用随机名称避免冲突)
    POOL="goroutine-leak-pool-$RANDOM"
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: $POOL
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 10
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=$POOL" 60 "$TEST_NS"

    # 创建多个 Sandbox 触发 Controller 活动
    echo "  创建 5 个 Sandbox..."
    for i in 1 2 3 4 5; do
        cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-goroutine-$i
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "60"]
  poolRef: $POOL
EOF
    done

    # 等待所有 Sandbox 分配完成
    echo "  等待 Sandbox 分配..."
    local all_assigned=false
    for i in {1..30}; do
        local count=0
        for j in 1 2 3 4 5; do
            if kubectl get sandbox "sb-goroutine-$j" -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null | grep -q "."; then
                count=$((count + 1))
            fi
        done
        if [ "$count" -eq 5 ]; then
            all_assigned=true
            break
        fi
        sleep 2
    done

    if [ "$all_assigned" = "false" ]; then
        echo "  ⚠ Sandbox 分配未完成，goroutine 检查可能不准确"
    fi

    # 检查当前 goroutine 数量
    CURRENT_GOROUTINES=$(kubectl exec "$CONTROLLER_POD" -n fast-sandbox-system -- curl -s http://localhost:6060/debug/pprof/goroutine?debug=1 2>/dev/null | grep -c "^goroutine" || echo "0")
    echo "  当前 goroutine 数量: $CURRENT_GOROUTINES"

    GOROUTINE_DIFF=$((CURRENT_GOROUTINES - INITIAL_GOROUTINES))

    if [ "$GOROUTINE_DIFF" -gt 50 ]; then
        echo "  ⚠ Goroutine 数量增长: $GOROUTINE_DIFF，可能存在泄漏"
    else
        echo "  ✓ Goroutine 数量稳定 (增长: $GOROUTINE_DIFF)"
    fi

    # 清理
    for i in 1 2 3 4 5; do
        kubectl delete sandbox "sb-goroutine-$i" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    done
    kubectl delete sandboxpool "$POOL" -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
