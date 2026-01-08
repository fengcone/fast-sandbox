#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

trap cleanup_all EXIT

setup_env "controller agent janitor"
install_infra
install_janitor

echo "=== [Test] Simulating Crash and Janitor Recovery ==="
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
metadata: { name: target-orphan }
spec: { image: "docker.io/library/alpine:latest", command: ["/bin/sleep", "3600"], poolRef: janitor-test-pool }
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/sandbox.yaml"

sleep 15
AGENT_POD=$(kubectl get sandbox target-orphan -o jsonpath='{.status.assignedPod}')

echo "Killing Agent Pod forcefully..."
kubectl delete pod "$AGENT_POD" --force --grace-period=0

echo "Waiting for Janitor to cleanup orphan container..."
for i in {1..30}; do
    # 检查 Containerd 容器列表
    COUNT=$(docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io container list -q | grep target-orphan | wc -l || echo "0")
    if [ "$COUNT" -eq 0 ]; then
        echo "🎉 SUCCESS: Janitor recycled the container!"
        exit 0
    fi
    sleep 10
done

echo "❌ FAILURE: Cleanup timed out."
exit 1