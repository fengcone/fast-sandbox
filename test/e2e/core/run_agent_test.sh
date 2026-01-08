#!/bin/bash
set -e

CLUSTER_NAME="fast-sandbox"
IMAGE_NAME="fast-sandbox/agent:dev"

echo "=== 1. Building Agent Binary & Image ==="
# 确保在项目根目录运行
cd "$(dirname "$0")/../../"
make docker-agent AGENT_IMAGE=$IMAGE_NAME

echo "=== 2. Loading Image into Kind Cluster '$CLUSTER_NAME' ==="
# Check if cluster exists
if ! kind get clusters | grep -q "^$CLUSTER_NAME$"; then
    echo "Cluster $CLUSTER_NAME not found. Creating..."
    kind create cluster --name $CLUSTER_NAME
fi
kind load docker-image $IMAGE_NAME --name $CLUSTER_NAME

echo "=== 3. Deploying Agent ==="
kubectl apply -f test/e2e/manifests/agent-deploy.yaml

echo "=== 4. Waiting for Agent to be Ready ==="
# Wait a bit for scheduling
sleep 2
kubectl wait --for=condition=ready pod -l app=sandbox-agent --timeout=120s

echo "=== 5. Setting up Port Forward ==="
POD_NAME=$(kubectl get pod -l app=sandbox-agent -o jsonpath="{.items[0].metadata.name}")
echo "Agent Pod: $POD_NAME"

# Kill previous port-forward if any
pkill -f "kubectl port-forward pod/$POD_NAME" || true

kubectl port-forward pod/$POD_NAME 8081:8081 > /dev/null 2>&1 &
PF_PID=$!
echo "Port-forward PID: $PF_PID"
sleep 5 # wait for connection

echo "=== 6. Calling Agent API to Create Sandbox ==="
# Construct Request
cat <<EOF > sandbox-req.json
{
  "agentId": "$POD_NAME",
  "sandboxSpecs": [
    {
      "sandboxId": "sb-test-001",
      "claimUid": "claim-1",
      "claimName": "test-claim",
      "image": "docker.io/library/alpine:latest", 
      "command": ["/bin/sleep", "3600"],
      "cpu": "100m",
      "memory": "100Mi"
    }
  ]
}
EOF
# Note: Using alpine:latest assuming it might be pulled if not present. 
# Better to use the agent image itself which we know is loaded? 
# Using fast-sandbox/agent:dev is safer for offline test.
# Ensure we use the full reference including docker.io/ as seen in containerd
sed -i.bak "s|docker.io/library/alpine:latest|docker.io/$IMAGE_NAME|g" sandbox-req.json

echo "Sending Sync Request..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST http://localhost:8081/api/v1/agent/sync -d @sandbox-req.json --header "Content-Type: application/json")

if [ "$HTTP_CODE" -ne 200 ]; then
    echo "Error: Sync API returned $HTTP_CODE"
    kill $PF_PID
    exit 1
fi
echo "Sync API Success"

echo "=== 7. Verifying Sandbox Status ==="
sleep 5 # Increase wait time slightly
RESPONSE=$(curl -s http://localhost:8081/api/v1/agent/status)
echo "Agent Status Response:"
if command -v jq >/dev/null 2>&1; then
    echo "$RESPONSE" | jq .
else
    echo "$RESPONSE"
fi

echo "=== Agent Logs (Last 50 lines) ==="
kubectl logs $POD_NAME --tail=50

echo "=== Verification ==="
if echo "$RESPONSE" | grep -q '"sandboxId":"sb-test-001"'; then
    echo "SUCCESS: Sandbox ID found."
else
    echo "FAILURE: Sandbox ID NOT found."
fi

if echo "$RESPONSE" | grep -q '"phase":"running"'; then
    echo "SUCCESS: Sandbox is running."
else
    echo "FAILURE: Sandbox is NOT running."
fi

# Cleanup
echo "Cleaning up..."
kill $PF_PID
rm sandbox-req.json sandbox-req.json.bak
# Comment out delete to keep environment for manual inspection if needed
kubectl delete -f test/e2e/manifests/agent-deploy.yaml --ignore-not-found=true

echo "=== Test Completed ==="
