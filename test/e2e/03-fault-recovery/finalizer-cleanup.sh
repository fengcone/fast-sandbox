#!/bin/bash

describe() {
    echo "Finalizer 清理 - 验证删除 Sandbox 时 finalizer 被正确移除，资源被释放"
}

run() {
    # 创建测试 Pool
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: finalizer-test-pool
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 2
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=finalizer-test-pool" 60 "$TEST_NS"

    # 获取 Agent Pod 名称
    AGENT_POD=$(kubectl get pod -l "fast-sandbox.io/pool=finalizer-test-pool" -n "$TEST_NS" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    if [ -z "$AGENT_POD" ]; then
        echo "  ❌ Agent Pod 未找到"
        return 1
    fi
    echo "  Agent Pod: $AGENT_POD"

    # 创建第一个 Sandbox 占用插槽
    echo "  创建 Sandbox A 占用插槽..."
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-finalizer-a
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: finalizer-test-pool
  exposedPorts: [8080]
EOF

    # 等待 Sandbox A 运行
    if ! wait_for_condition "kubectl get sandbox sb-finalizer-a -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qiE 'running|bound'" 30 "Sandbox A running"; then
        echo "  ❌ Sandbox A 未运行"
        return 1
    fi

    PHASE_A=$(kubectl get sandbox sb-finalizer-a -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    echo "  Sandbox A Phase: $PHASE_A"

    # 验证 finalizer 存在
    echo "  验证 finalizer 存在..."
    FINALIZERS=$(kubectl get sandbox sb-finalizer-a -n "$TEST_NS" -o jsonpath='{.metadata.finalizers}' 2>/dev/null || echo "")
    if echo "$FINALIZERS" | grep -q "sandbox.fast.io/cleanup"; then
        echo "  ✓ Finalizer 存在: sandbox.fast.io/cleanup"
    else
        echo "  ❌ Finalizer 未找到"
        return 1
    fi

    # 删除 Sandbox A
    echo "  删除 Sandbox A..."
    kubectl delete sandbox sb-finalizer-a -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    # 等待删除完成
    echo "  等待 Sandbox 删除完成..."
    if ! wait_for_condition "! kubectl get sandbox sb-finalizer-a -n '$TEST_NS' >/dev/null 2>&1" 60 "Sandbox A deleted"; then
        echo "  ❌ Sandbox 删除超时"
        return 1
    fi
    echo "  ✓ Sandbox 删除成功"

    # 创建第二个 Sandbox 验证插槽已释放
    echo "  创建 Sandbox B 验证插槽已释放..."
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-finalizer-b
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: finalizer-test-pool
  exposedPorts: [5758]
EOF

    # 等待 Sandbox B 运行
    if ! wait_for_condition "kubectl get sandbox sb-finalizer-b -n '$TEST_NS' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qiE 'running|bound'" 30 "Sandbox B running"; then
        echo "  ❌ Sandbox B 未运行"
        return 1
    fi

    PHASE_B=$(kubectl get sandbox sb-finalizer-b -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    ASSIGNED_POD_B=$(kubectl get sandbox sb-finalizer-b -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")

    if [ -z "$PHASE_B" ] || [[ ! "$PHASE_B" =~ [Rr]unning|[Bb]ound ]]; then
        echo "  ❌ Sandbox B 未运行，phase: $PHASE_B"
        return 1
    fi

    # 验证 B 被分配到了同一个 Agent Pod
    if [ "$ASSIGNED_POD_B" = "$AGENT_POD" ]; then
        echo "  ✓ 插槽正确释放，Sandbox B 分配到同一 Pod: $AGENT_POD"
    else
        echo "  ❌ 插槽未正确释放。B 分配到 $ASSIGNED_POD_B，期望 $AGENT_POD"
        return 1
    fi

    # 清理
    kubectl delete sandbox sb-finalizer-b -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete sandboxpool finalizer-test-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
