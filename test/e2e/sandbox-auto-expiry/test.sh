#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

# 确保无论脚本如何退出，都会执行清理
trap cleanup_all EXIT

# 1. 环境准备
setup_env "controller agent"
install_infra

# 2. 业务准备
mkdir -p "$SCRIPT_DIR/manifests"
cat <<EOF > "$SCRIPT_DIR/manifests/pool.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata: { name: expiry-pool }
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/pool.yaml"
wait_for_pod "fast-sandbox.io/pool=expiry-pool"

# 3. 核心测试
echo "=== [Test] Creating Sandbox with Expiry (20s) ==="
EXPIRY_TIME=$(date -u -v+20S +"%Y-%m-%dT%H:%M:%SZ")
cat <<EOF > "$SCRIPT_DIR/manifests/sandbox.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: test-expiry }
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: expiry-pool
  expireTime: "$EXPIRY_TIME"
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/sandbox.yaml"

sleep 10
echo "Waiting for expiration (another 30s)..."
sleep 30

if kubectl get sandbox test-expiry 2>&1 | grep -q "NotFound"; then
    echo "🎉 SUCCESS: Sandbox automatically garbage collected!"
else
    echo "❌ FAILURE: Sandbox still exists after expiry."
    exit 1
fi