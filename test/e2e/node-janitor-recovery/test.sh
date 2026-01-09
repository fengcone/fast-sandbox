#!/bin/bash
set -e

# --- 1. 配置与环境初始化 ---
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

trap cleanup_all EXIT

echo "=== [Setup] Building and Installing Infrastructure ==="
setup_env "controller agent janitor"
install_infra
install_janitor

# --- 2. 创建测试沙箱 ---
mkdir -p "$SCRIPT_DIR/manifests"
cat <<EOF > "$SCRIPT_DIR/manifests/pool.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata: { name: janitor-test-pool }
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/pool.yaml"
wait_for_pod "fast-sandbox.io/pool=janitor-test-pool"

cat <<EOF > "$SCRIPT_DIR/manifests/sandbox.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-to-be-orphaned }
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: janitor-test-pool
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/sandbox.yaml"

# 等待运行
sleep 15
# 获取带有正确标签的容器 ID
CONTAINER_ID=$(docker exec fast-sandbox-control-plane ctr -n k8s.io containers ls | grep "sb-to-be-orphaned" | awk '{print $1}')
if [ -z "$CONTAINER_ID" ]; then
    echo "❌ FAILURE: Container not found on host."
    exit 1
fi
echo "Container ID: $CONTAINER_ID"

# --- 3. 模拟逻辑丢失：直接删除 CRD 并移除 Finalizer ---
echo "=== [Test] Simulating Logic Loss (Deleting CRD without cleanup) ==="
# 我们需要去掉 finalizer 否则无法直接删除
kubectl patch sandbox sb-to-be-orphaned -p '{"metadata":{"finalizers":null}}' --type=merge
kubectl delete sandbox sb-to-be-orphaned --wait=false

echo "Sandbox CRD deleted. Now waiting for Janitor reconciliation (60s grace period)..."

# 这里的等待时间需要超过 Janitor 的 60s 保护窗口
for i in {1..20}; do
    STILL_EXISTS=$(docker exec fast-sandbox-control-plane ctr -n k8s.io containers ls -q | grep "$CONTAINER_ID" || true)
    if [ -z "$STILL_EXISTS" ]; then
        echo "🎉 SUCCESS: Janitor detected orphan and cleaned it up!"
        exit 0
    fi
    echo "Check $i: Container $CONTAINER_ID still running..."
    sleep 10
done

echo "❌ FAILURE: Janitor failed to clean up orphaned container."
exit 1
