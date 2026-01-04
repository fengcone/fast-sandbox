#!/bin/bash
set -e

CLUSTER_NAME="fast-sandbox"
AGENT_IMAGE="fast-sandbox/agent:dev"
CONTROLLER_IMAGE="fast-sandbox/controller:dev"

echo "=== 0. Cleanup Old Resources ==="
kubectl delete deployment fast-sandbox-agent --ignore-not-found=true
kubectl delete pods -l app=sandbox-agent --ignore-not-found=true --force --grace-period=0
kubectl delete deployment fast-sandbox-controller --ignore-not-found=true
# Also clean CRs
kubectl delete sandboxpool --all --ignore-not-found=true
kubectl delete sandbox --all --ignore-not-found=true

echo "=== 1. Building Images ==="
# Ensure we are in root
cd "$(dirname "$0")/../../"
make docker-agent AGENT_IMAGE=$AGENT_IMAGE
make docker-controller CONTROLLER_IMAGE=$CONTROLLER_IMAGE

echo "=== 2. Loading Images into Kind ==="
if ! kind get clusters | grep -q "^$CLUSTER_NAME$"; then
    echo "Cluster $CLUSTER_NAME not found. Creating..."
    kind create cluster --name $CLUSTER_NAME
fi
kind load docker-image $AGENT_IMAGE --name $CLUSTER_NAME
kind load docker-image $CONTROLLER_IMAGE --name $CLUSTER_NAME

echo "=== 3. Deploying Controller ==="
kubectl delete deployment fast-sandbox-controller --ignore-not-found=true
kubectl apply -f test/e2e/manifests/controller-deploy.yaml

echo "=== 4. Waiting for Controller Ready ==="
kubectl rollout status deployment/fast-sandbox-controller --timeout=60s

echo "=== 5. Applying SandboxPool ==="
kubectl apply -f test/e2e/manifests/pool.yaml

echo "=== 6. Waiting for Agent Pod Created by Pool ==="
sleep 5 # Wait for reconcile
kubectl wait --for=condition=ready pod -l app=sandbox-agent --timeout=120s
AGENT_POD=$(kubectl get pod -l app=sandbox-agent -o jsonpath="{.items[0].metadata.name}")
echo "Agent Pod Created: $AGENT_POD"

echo "=== 7. Applying Sandbox ==="
kubectl apply -f test/e2e/manifests/sandbox.yaml

echo "=== 8. Verifying Sandbox Status ==="
echo "Waiting for sandbox to be Bound..."
# Loop check for status
for i in {1..30}; do
    PHASE=$(kubectl get sandbox test-sandbox -o jsonpath="{.status.phase}" 2>/dev/null || echo "")
    ASSIGNED=$(kubectl get sandbox test-sandbox -o jsonpath="{.status.assignedPod}" 2>/dev/null || echo "")
    echo "Check $i: Phase=$PHASE, Assigned=$ASSIGNED"
    
    if [[ "$PHASE" == "Bound" || "$PHASE" == "Running" ]] && [[ "$ASSIGNED" != "" ]]; then
        echo "SUCCESS: Sandbox scheduled to $ASSIGNED"
        break
    fi
    if [ $i -eq 30 ]; then
        echo "TIMEOUT: Sandbox failed to schedule/run."
        echo "--- Controller Logs ---"
        kubectl logs -l control-plane=controller-manager --tail=50
        echo "--- Sandbox Description ---"
        kubectl describe sandbox test-sandbox
        exit 1
    fi
    sleep 2
done

echo "=== Test Completed Successfully ==="
