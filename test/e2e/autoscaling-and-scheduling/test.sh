#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

trap cleanup_all EXIT

setup_env "controller agent"
install_infra

echo "=== [Test] Verifying Demand-based Autoscaling (Slot=1) ==="
mkdir -p "$SCRIPT_DIR/manifests"
cat <<EOF > "$SCRIPT_DIR/manifests/pool.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata: { name: scale-pool }
spec:
  capacity: { poolMin: 1, poolMax: 2 }
  maxSandboxesPerPod: 1
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/pool.yaml"
wait_for_pod "fast-sandbox.io/pool=scale-pool"

echo "Creating 2 sandboxes to trigger scale up..."
cat <<EOF | kubectl apply -f -
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-scale-1 }
spec: { image: "docker.io/library/alpine:latest", command: ["/bin/sleep", "3600"], poolRef: scale-pool }
---
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-scale-2 }
spec: { image: "docker.io/library/alpine:latest", command: ["/bin/sleep", "3600"], poolRef: scale-pool }
EOF

echo "Waiting for pool to scale up to 2 pods..."
for i in {1..30}; do
    COUNT=$(kubectl get pods -l fast-sandbox.io/pool=scale-pool --no-headers 2>/dev/null | wc -l)
    if [ "$COUNT" -ge 2 ]; then
        echo "🎉 SUCCESS: Scaled up to 2 pods!"
        exit 0
    fi
    sleep 3
done

echo "❌ FAILURE: Failed to scale up."
exit 1