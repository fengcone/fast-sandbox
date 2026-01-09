#!/bin/bash
set -e

# --- 1. 配置与环境初始化 ---
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

trap cleanup_all EXIT

echo "=== [Setup] Building and Installing Infrastructure ==="
setup_env "controller agent"
install_infra

# --- 2. 准备 Pool (容量大，但端口会互斥) ---
cat <<EOF > "$SCRIPT_DIR/manifests/pool.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata: { name: port-test-pool }
spec:
  capacity: { poolMin: 1, poolMax: 2 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/pool.yaml"
wait_for_pod "fast-sandbox.io/pool=port-test-pool"

# --- 3. 执行核心测试 ---
echo "=== [Test] Scheduling Sandbox A (Port 8080) ==="
cat <<EOF > "$SCRIPT_DIR/manifests/sb-a.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-a }
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: port-test-pool
  exposedPorts: [8080]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/sb-a.yaml"

# 等待 A 运行并获取 Pod
sleep 10
POD_A=$(kubectl get sandbox sb-a -o jsonpath='{.status.assignedPod}')
echo "Sandbox A is on Pod: $POD_A"

echo "=== [Test] Scheduling Sandbox B (Port 8080) ==="
# B 请求同样的端口，即使 Slot 够，也必须去另一个 Pod
cat <<EOF > "$SCRIPT_DIR/manifests/sb-b.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-b }
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: port-test-pool
  exposedPorts: [8080]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/sb-b.yaml"

echo "Waiting for Sandbox B to be scheduled..."
for i in {1..20}; do
    POD_B=$(kubectl get sandbox sb-b -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")
    if [[ "$POD_B" != "" ]]; then
        echo "Sandbox B is on Pod: $POD_B"
        break
    fi
    sleep 5
done

if [[ "$POD_A" == "$POD_B" ]]; then
    echo "❌ FAILURE: Port conflict! Both sandboxes scheduled to the same pod $POD_A."
    exit 1
fi

echo "🎉 SUCCESS: Port mutual exclusion verified! Sandboxes are on different pods."

# 检查 Endpoints 回填
echo "Checking Status Endpoints..."
ENDPOINT_A=$(kubectl get sandbox sb-a -o jsonpath='{.status.endpoints[0]}')
echo "Sandbox A Endpoint: $ENDPOINT_A"
if [[ "$ENDPOINT_A" == *":8080" ]]; then
    echo "🎉 SUCCESS: Endpoint status correctly populated."
else
    echo "❌ FAILURE: Endpoint status missing or incorrect."
    exit 1
fi
