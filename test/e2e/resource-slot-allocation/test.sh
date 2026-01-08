#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

trap cleanup_all EXIT

setup_env "controller agent"
install_infra

echo "=== [Test] Verifying Resource Slot Logic (2000m / 2 Slots) ==="
mkdir -p "$SCRIPT_DIR/manifests"
cat <<EOF > "$SCRIPT_DIR/manifests/pool.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata: { name: resource-test-pool }
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 2
  runtimeType: container
  agentTemplate:
    spec:
      containers:
      - name: agent
        image: "$AGENT_IMAGE"
        resources: { limits: { cpu: "2000m", memory: "1Gi" } }
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/pool.yaml"
wait_for_pod "fast-sandbox.io/pool=resource-test-pool"

cat <<EOF > "$SCRIPT_DIR/manifests/sandbox.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-slot-check }
spec: { image: "docker.io/library/alpine:latest", command: ["/bin/sleep", "3600"], poolRef: resource-test-pool }
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/sandbox.yaml"

echo "Checking Agent logs for slot calculation..."
for i in {1..20}; do
    LOGS=$(kubectl logs -l fast-sandbox.io/pool=resource-test-pool --tail=100 2>/dev/null || echo "")
    if echo "$LOGS" | grep -q "RESOURCES_VERIFY: Slot allocated for sb-slot-check: CPU=1000m, Memory=536870912 bytes"; then
        echo "🎉 SUCCESS: Slot resources correctly calculated!"
        exit 0
    fi
    sleep 5
done

echo "❌ FAILURE: Log not found."
exit 1