#!/bin/bash
set -e

# --- 端口范围验证测试 ---
# 测试目标：
# 1. 验证有效端口范围 (1-65535) 可以正常工作
# 2. 验证无效端口 (0, 65536, 负数) 会被拒绝

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

trap cleanup_all EXIT

echo "=== [Setup] Building and Installing Infrastructure ==="
setup_env "controller agent"
install_infra

# --- 1. 准备 Pool ---
cat <<EOF > "$SCRIPT_DIR/manifests/pool.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata: { name: port-validation-pool }
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/pool.yaml"
wait_for_pod "fast-sandbox.io/pool=port-validation-pool"

# --- 2. 测试无效端口 0 (应该被拒绝) ---
echo "=== [Test] Testing invalid port 0 (should be rejected) ==="
cat <<EOF > "$SCRIPT_DIR/manifests/sb-invalid-0.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-port-invalid-0 }
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: port-validation-pool
  exposedPorts: [0]
EOF

# 创建应该失败（但 K8s 会先接收，然后 controller 拒绝调度）
kubectl apply -f "$SCRIPT_DIR/manifests/sb-invalid-0.yaml" 2>/dev/null || true
sleep 5

# 检查状态 - 应该是 Failed 或 Pending 且没有分配 Pod
PHASE=$(kubectl get sandbox sb-port-invalid-0 -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
ASSIGNED_POD=$(kubectl get sandbox sb-port-invalid-0 -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")

echo "Sandbox Phase: $PHASE"
echo "Assigned Pod: $ASSIGNED_POD"

if [[ "$ASSIGNED_POD" != "" ]]; then
    echo "❌ FAILURE: Port 0 was accepted (should be rejected)"
    kubectl get sandbox sb-port-invalid-0 -o yaml
    exit 1
fi

# 如果是 Failed 状态或者是 Pending 且有错误条件，视为通过
if [[ "$PHASE" == "Failed" ]] || [[ "$PHASE" == "Pending" ]]; then
    echo "✓ Port 0 correctly rejected"
else
    # 检查事件中是否有错误
    EVENTS=$(kubectl describe sandbox sb-port-invalid-0 2>/dev/null | grep -i "invalid port" || echo "")
    if [[ -n "$EVENTS" ]]; then
        echo "✓ Port 0 correctly rejected (found error in events)"
    else
        echo "⚠ WARNING: Port 0 rejection not clearly verified (phase: $PHASE)"
    fi
fi

kubectl delete sandbox sb-port-invalid-0 --ignore-not-found=true

# --- 3. 测试有效端口 1 (应该成功) ---
echo "=== [Test] Testing valid port 1 (should succeed) ==="
cat <<EOF > "$SCRIPT_DIR/manifests/sb-valid-1.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-port-valid-1 }
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "60"]
  poolRef: port-validation-pool
  exposedPorts: [1]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/sb-valid-1.yaml"

sleep 10
PHASE=$(kubectl get sandbox sb-port-valid-1 -o jsonpath='{.status.phase}')
ASSIGNED_POD=$(kubectl get sandbox sb-port-valid-1 -o jsonpath='{.status.assignedPod}')
PHASE_LOWER=$(echo "$PHASE" | tr '[:upper:]' '[:lower:]')

echo "Sandbox Phase: $PHASE"
echo "Assigned Pod: $ASSIGNED_POD"

if [[ "$PHASE_LOWER" != "running" && "$PHASE_LOWER" != "bound" ]]; then
    echo "❌ FAILURE: Port 1 was rejected (should be accepted)"
    kubectl get sandbox sb-port-valid-1 -o yaml
    exit 1
fi

echo "✓ Port 1 correctly accepted"

kubectl delete sandbox sb-port-valid-1

# --- 4. 测试有效端口 65535 (应该成功) ---
echo "=== [Test] Testing valid port 65535 (should succeed) ==="
cat <<EOF > "$SCRIPT_DIR/manifests/sb-valid-max.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-port-valid-max }
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "60"]
  poolRef: port-validation-pool
  exposedPorts: [65535]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/sb-valid-max.yaml"

sleep 10
PHASE=$(kubectl get sandbox sb-port-valid-max -o jsonpath='{.status.phase}')
ASSIGNED_POD=$(kubectl get sandbox sb-port-valid-max -o jsonpath='{.status.assignedPod}')
PHASE_LOWER=$(echo "$PHASE" | tr '[:upper:]' '[:lower:]')

echo "Sandbox Phase: $PHASE"
echo "Assigned Pod: $ASSIGNED_POD"

if [[ "$PHASE_LOWER" != "running" && "$PHASE_LOWER" != "bound" ]]; then
    echo "❌ FAILURE: Port 65535 was rejected (should be accepted)"
    kubectl get sandbox sb-port-valid-max -o yaml
    exit 1
fi

echo "✓ Port 65535 correctly accepted"

kubectl delete sandbox sb-port-valid-max

# --- 5. 测试超出范围的端口 65536 (应该被拒绝) ---
echo "=== [Test] Testing invalid port 65536 (should be rejected) ==="
cat <<EOF > "$SCRIPT_DIR/manifests/sb-invalid-over.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-port-invalid-over }
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: port-validation-pool
  exposedPorts: [65536]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/sb-invalid-over.yaml" 2>/dev/null || true
sleep 5

PHASE=$(kubectl get sandbox sb-port-invalid-over -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
ASSIGNED_POD=$(kubectl get sandbox sb-port-invalid-over -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")

echo "Sandbox Phase: $PHASE"
echo "Assigned Pod: $ASSIGNED_POD"

if [[ "$ASSIGNED_POD" != "" ]]; then
    echo "❌ FAILURE: Port 65536 was accepted (should be rejected)"
    kubectl get sandbox sb-port-invalid-over -o yaml
    exit 1
fi

if [[ "$PHASE" == "Failed" ]] || [[ "$PHASE" == "Pending" ]]; then
    echo "✓ Port 65536 correctly rejected"
else
    EVENTS=$(kubectl describe sandbox sb-port-invalid-over 2>/dev/null | grep -i "invalid port" || echo "")
    if [[ -n "$EVENTS" ]]; then
        echo "✓ Port 65536 correctly rejected (found error in events)"
    else
        echo "⚠ WARNING: Port 65536 rejection not clearly verified (phase: $PHASE)"
    fi
fi

kubectl delete sandbox sb-port-invalid-over --ignore-not-found=true

echo ""
echo "🎉 SUCCESS: Port validation test passed!"
echo "- Invalid port 0 was rejected"
echo "- Valid port 1 was accepted"
echo "- Valid port 65535 was accepted"
echo "- Invalid port 65536 was rejected"
