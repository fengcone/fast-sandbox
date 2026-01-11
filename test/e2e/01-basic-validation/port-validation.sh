#!/bin/bash

describe() {
    echo "端口范围验证 - 验证端口 1-65535 范围检查，越界端口被拒绝"
}

run() {
    # 创建测试 Pool
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: port-validation-pool
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF

    wait_for_pod "fast-sandbox.io/pool=port-validation-pool" 60 "$TEST_NS"

    # 测试1: 端口 0 应该被拒绝
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-port-invalid-0
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "60"]
  poolRef: port-validation-pool
  exposedPorts: [0]
EOF

    sleep 5
    PHASE=$(kubectl get sandbox sb-port-invalid-0 -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    ASSIGNED_POD=$(kubectl get sandbox sb-port-invalid-0 -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")

    if [ "$ASSIGNED_POD" != "" ]; then
        echo "  ❌ 端口 0 被错误接受"
        kubectl delete sandbox sb-port-invalid-0 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi
    echo "  ✓ 端口 0 被正确拒绝"
    kubectl delete sandbox sb-port-invalid-0 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    # 测试2: 端口 1 应该成功
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-port-valid-1
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "60"]
  poolRef: port-validation-pool
  exposedPorts: [1]
EOF

    sleep 10
    PHASE=$(kubectl get sandbox sb-port-valid-1 -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null | tr '[:upper:]' '[:lower:]')
    if [ "$PHASE" != "running" ] && [ "$PHASE" != "bound" ]; then
        echo "  ❌ 端口 1 被错误拒绝"
        return 1
    fi
    echo "  ✓ 端口 1 被正确接受"
    kubectl delete sandbox sb-port-valid-1 -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    # 测试3: 端口 65535 应该成功
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-port-valid-max
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "60"]
  poolRef: port-validation-pool
  exposedPorts: [65535]
EOF

    sleep 10
    PHASE=$(kubectl get sandbox sb-port-valid-max -n "$TEST_NS" -o jsonpath='{.status.phase}' 2>/dev/null | tr '[:upper:]' '[:lower:]')
    if [ "$PHASE" != "running" ] && [ "$PHASE" != "bound" ]; then
        echo "  ❌ 端口 65535 被错误拒绝"
        return 1
    fi
    echo "  ✓ 端口 65535 被正确接受"
    kubectl delete sandbox sb-port-valid-max -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    # 测试4: 端口 65536 应该被拒绝
    cat <<EOF | kubectl apply -f - -n "$TEST_NS" >/dev/null 2>&1
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-port-invalid-over
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "60"]
  poolRef: port-validation-pool
  exposedPorts: [65536]
EOF

    sleep 5
    ASSIGNED_POD=$(kubectl get sandbox sb-port-invalid-over -n "$TEST_NS" -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")
    if [ "$ASSIGNED_POD" != "" ]; then
        echo "  ❌ 端口 65536 被错误接受"
        kubectl delete sandbox sb-port-invalid-over -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1
        return 1
    fi
    echo "  ✓ 端口 65536 被正确拒绝"
    kubectl delete sandbox sb-port-invalid-over -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    # 清理 Pool
    kubectl delete sandboxpool port-validation-pool -n "$TEST_NS" --ignore-not-found=true >/dev/null 2>&1

    return 0
}
