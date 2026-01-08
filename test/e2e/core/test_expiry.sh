#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../../../" && pwd)"
MANIFEST_DIR="$SCRIPT_DIR/manifests"

CLUSTER_NAME="fast-sandbox"
AGENT_IMAGE="fast-sandbox/agent:dev"
CONTROLLER_IMAGE="fast-sandbox/controller:dev"

echo "=== 1. Preparing Local KIND Environment ==="
cd "$ROOT"
make docker-controller
make docker-agent
kind load docker-image $CONTROLLER_IMAGE --name $CLUSTER_NAME
kind load docker-image $AGENT_IMAGE --name $CLUSTER_NAME

kubectl rollout restart deployment/fast-sandbox-controller
kubectl rollout status deployment/fast-sandbox-controller --timeout=60s

echo "=== 2. Creating Pool and Sandbox with Expiry ==="
cat <<EOF > "$MANIFEST_DIR/pool-expiry-test.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: autoscale-pool
  namespace: default
spec:
  capacity:
    poolMin: 1
    poolMax: 2
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers:
      - name: agent
        image: $AGENT_IMAGE
        imagePullPolicy: IfNotPresent
EOF
kubectl apply -f "$MANIFEST_DIR/pool-expiry-test.yaml"

EXPIRY_TIME=$(date -u -v+20S +"%Y-%m-%dT%H:%M:%SZ")
echo "Target Expiry Time: $EXPIRY_TIME"

cat <<EOF > "$MANIFEST_DIR/sandbox-expiry.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: expiry-sandbox
  namespace: default
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: autoscale-pool
  expireTime: "$EXPIRY_TIME"
EOF
kubectl apply -f "$MANIFEST_DIR/sandbox-expiry.yaml"

echo "=== 3. Waiting for Expiration (30 seconds) ==="
sleep 35

echo "=== 4. Verifying Sandbox is Deleted ==="
FINAL_STATUS=$(kubectl get sandbox expiry-sandbox 2>&1 || echo "Deleted")

if [[ "$FINAL_STATUS" == *"NotFound"* || "$FINAL_STATUS" == "Deleted" ]]; then
    echo "🎉 SUCCESS: Sandbox was automatically garbage collected!"
else
    echo "❌ FAILURE: Sandbox still exists after expiry time."
    exit 1
fi