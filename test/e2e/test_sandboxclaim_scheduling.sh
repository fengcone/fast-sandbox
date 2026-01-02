#!/bin/bash

set -e

echo "=========================================="
echo "E2E Test: SandboxClaim Scheduling"
echo "=========================================="

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 工作目录
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

echo ""
echo -e "${YELLOW}Step 1: Cleaning KIND cluster resources${NC}"
kubectl delete sandboxclaim --all -n default --ignore-not-found=true
kubectl delete sandboxpool --all -n default --ignore-not-found=true
echo "Waiting for resources to be fully deleted..."
sleep 5

echo ""
echo -e "${YELLOW}Step 2: Creating SandboxPool${NC}"
kubectl apply -f "$PROJECT_ROOT/config/samples/sandboxpool_sample.yaml"

echo ""
echo -e "${YELLOW}Step 3: Waiting for Agent Pods to be Ready (timeout: 60s)${NC}"
for i in {1..12}; do
    READY_PODS=$(kubectl get pods -n default -l sandbox.fast.io/pool=test-sandbox-pool --no-headers 2>/dev/null | grep "1/1.*Running" | wc -l | tr -d ' ')
    TOTAL_PODS=$(kubectl get pods -n default -l sandbox.fast.io/pool=test-sandbox-pool --no-headers 2>/dev/null | wc -l | tr -d ' ')
    echo "  Attempt $i/12: Ready Pods = $READY_PODS / $TOTAL_PODS"
    
    if [ "$READY_PODS" -ge 2 ]; then
        echo -e "${GREEN}✓ Agent Pods are ready!${NC}"
        break
    fi
    
    if [ $i -eq 12 ]; then
        echo -e "${RED}✗ Timeout waiting for Agent Pods${NC}"
        kubectl get pods -n default -l sandbox.fast.io/pool=test-sandbox-pool
        kubectl describe pods -n default -l sandbox.fast.io/pool=test-sandbox-pool
        exit 1
    fi
    
    sleep 5
done

echo ""
echo -e "${YELLOW}Step 4: Checking Agent Pod details${NC}"
kubectl get pods -n default -l sandbox.fast.io/pool=test-sandbox-pool -o wide

echo ""
echo -e "${YELLOW}Step 5: Creating SandboxClaim with poolRef${NC}"
cat <<EOF | kubectl apply -f -
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxClaim
metadata:
  name: test-claim-with-pool
  namespace: default
spec:
  image: nginx:latest
  cpu: "100m"
  memory: "128Mi"
  port: 8080
  poolRef:
    name: test-sandbox-pool
    namespace: default
EOF

echo ""
echo -e "${YELLOW}Step 6: Waiting for SandboxClaim to be scheduled (timeout: 30s)${NC}"
for i in {1..6}; do
    PHASE=$(kubectl get sandboxclaim test-claim-with-pool -n default -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    ASSIGNED_POD=$(kubectl get sandboxclaim test-claim-with-pool -n default -o jsonpath='{.status.assignedAgentPod}' 2>/dev/null || echo "")
    
    echo "  Attempt $i/6: Phase = '$PHASE', AssignedPod = '$ASSIGNED_POD'"
    
    if [ "$PHASE" = "Running" ] && [ -n "$ASSIGNED_POD" ]; then
        echo -e "${GREEN}✓ SandboxClaim scheduled successfully!${NC}"
        break
    fi
    
    if [ $i -eq 6 ]; then
        echo -e "${RED}✗ Timeout waiting for SandboxClaim scheduling${NC}"
        kubectl get sandboxclaim test-claim-with-pool -n default -o yaml
        exit 1
    fi
    
    sleep 5
done

echo ""
echo -e "${YELLOW}Step 7: Verifying SandboxClaim status${NC}"
ASSIGNED_POD=$(kubectl get sandboxclaim test-claim-with-pool -n default -o jsonpath='{.status.assignedAgentPod}')
PHASE=$(kubectl get sandboxclaim test-claim-with-pool -n default -o jsonpath='{.status.phase}')
SANDBOX_ID=$(kubectl get sandboxclaim test-claim-with-pool -n default -o jsonpath='{.status.sandboxID}')
POD_IP=$(kubectl get sandboxclaim test-claim-with-pool -n default -o jsonpath='{.status.podIP}')

echo "  - Assigned Agent Pod: $ASSIGNED_POD"
echo "  - Phase: $PHASE"
echo "  - Sandbox ID: $SANDBOX_ID"
echo "  - Pod IP: $POD_IP"

echo ""
echo -e "${YELLOW}Step 8: Checking if assigned Pod belongs to SandboxPool${NC}"
POD_POOL_LABEL=$(kubectl get pod "$ASSIGNED_POD" -n default -o jsonpath='{.metadata.labels.sandbox\.fast\.io/pool}' 2>/dev/null || echo "")

if [ "$POD_POOL_LABEL" = "test-sandbox-pool" ]; then
    echo -e "${GREEN}✓ SUCCESS: Assigned Pod belongs to SandboxPool!${NC}"
else
    echo -e "${RED}✗ FAILED: Assigned Pod does not belong to expected SandboxPool${NC}"
    echo "  Expected Pool: test-sandbox-pool"
    echo "  Actual Pool Label: $POD_POOL_LABEL"
    exit 1
fi

echo ""
echo -e "${YELLOW}Step 9: Displaying final state${NC}"
echo ""
echo "--- SandboxPool Status ---"
kubectl get sandboxpool test-sandbox-pool -n default -o yaml | grep -A 10 "status:"

echo ""
echo "--- SandboxClaim Status ---"
kubectl get sandboxclaim test-claim-with-pool -n default -o yaml | grep -A 15 "status:"

echo ""
echo "--- Agent Pods ---"
kubectl get pods -n default -l sandbox.fast.io/pool=test-sandbox-pool

echo ""
echo -e "${GREEN}=========================================="
echo "E2E Test: PASSED ✓"
echo "==========================================${NC}"
