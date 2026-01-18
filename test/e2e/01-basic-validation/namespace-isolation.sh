#!/bin/bash

describe() {
    echo "Namespace 隔离 - 验证跨 Namespace 调度被拒绝"
}

run() {
    # 在 namespace-a 创建 Pool
    kubectl create namespace "ns-a" >/dev/null 2>&1 || true
    kubectl create namespace "ns-b" >/dev/null 2>&1 || true

    cat <<EOF | kubectl apply -f - -n "ns-a" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: test-pool
spec:
  capacity: { poolMin: 1, poolMax: 2 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    # 等待 Agent 就绪
    if ! wait_for_pod "fast-sandbox.io/pool=test-pool" 60 "ns-a"; then
        echo "  ❌ Agent Pod 未就绪"
        kubectl delete namespace "ns-a" --ignore-not-found=true >/dev/null 2>&1
        kubectl delete namespace "ns-b" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    AGENT_POD=$(kubectl get pod -l "fast-sandbox.io/pool=test-pool" -n "ns-a" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
    echo "  Agent Pod: $AGENT_POD (ns-a)"

    # 测试 1: 同 Namespace 调度应该成功
    echo "  测试 1: 同 Namespace 调度..."
    cat <<EOF | kubectl apply -f - -n "ns-a" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-same-ns
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "60"]
  poolRef: test-pool
EOF

    if wait_for_condition "kubectl get sandbox sb-same-ns -n 'ns-a' -o jsonpath='{.status.phase}' 2>/dev/null | grep -qiE 'running|bound'" 30 "Same-NS sandbox running"; then
        echo "  ✓ 同 Namespace 调度成功"
    else
        echo "  ❌ 同 Namespace 调度失败"
        kubectl delete namespace "ns-a" --ignore-not-found=true >/dev/null 2>&1
        kubectl delete namespace "ns-b" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    # 测试 2: 跨 Namespace 调度应该失败（保持 Pending）
    echo "  测试 2: 跨 Namespace 调度应被拒绝..."
    cat <<EOF | kubectl apply -f - -n "ns-b" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-cross-ns
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "60"]
  poolRef: test-pool
EOF

    # 等待一段时间，检查状态
    # 跨 Namespace 的 Sandbox 应该无法被调度，保持 Pending 或没有 assignedPod
    sleep 15
    PHASE=$(kubectl get sandbox sb-cross-ns -n "ns-b" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    ASSIGNED=$(kubectl get sandbox sb-cross-ns -n "ns-b" -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")

    # 判断标准：没有被分配 Agent (ASSIGNED 为空) 即为通过
    if [ -z "$ASSIGNED" ]; then
        echo "  ✓ 跨 Namespace 调度被正确拒绝 (Phase: ${PHASE:-<empty>}, Assigned: <empty>)"
    else
        echo "  ❌ 跨 Namespace 调度未被拒绝 (Phase: $PHASE, Assigned: $ASSIGNED)"
        kubectl delete sandbox sb-cross-ns -n "ns-b" --ignore-not-found=true >/dev/null 2>&1
        kubectl delete namespace "ns-a" --ignore-not-found=true >/dev/null 2>&1
        kubectl delete namespace "ns-b" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi

    # 清理
    kubectl delete sandbox sb-same-ns -n "ns-a" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete sandbox sb-cross-ns -n "ns-b" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete sandboxpool test-pool -n "ns-a" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete namespace "ns-a" --ignore-not-found=true >/dev/null 2>&1
    kubectl delete namespace "ns-b" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
