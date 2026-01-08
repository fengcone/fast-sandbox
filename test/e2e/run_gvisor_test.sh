#!/bin/bash
set -e

CLUSTER_NAME="fast-sandbox"
AGENT_IMAGE="fast-sandbox/agent:dev"
CONTROLLER_IMAGE="fast-sandbox/controller:dev"

echo "=== 0. Cleanup Old Resources ==="
kubectl delete sandboxpool --all --ignore-not-found=true || true
kubectl delete sandbox --all --ignore-not-found=true || true
kubectl delete pods -l app=sandbox-agent --force --grace-period=0 --ignore-not-found=true || true

echo "=== 1. Building and Loading Images ==="
cd "$(dirname "$0")/../../"
make docker-agent AGENT_IMAGE=$AGENT_IMAGE
make docker-controller CONTROLLER_IMAGE=$CONTROLLER_IMAGE

echo "=== 1.1 Checking Cluster Status ==="
if ! kind get clusters | grep -q "^$CLUSTER_NAME$"; then
    echo "Cluster $CLUSTER_NAME not found. Creating..."
    kind create cluster --name $CLUSTER_NAME --image kindest/node:v1.35.0
else
    echo "Cluster $CLUSTER_NAME already exists."
fi

echo "=== 1.2 Loading Agent Image ($AGENT_IMAGE) ==="
kind load docker-image $AGENT_IMAGE --name $CLUSTER_NAME
echo "Agent Image Loaded."

echo "=== 1.3 Loading Controller Image ($CONTROLLER_IMAGE) ==="
kind load docker-image $CONTROLLER_IMAGE --name $CLUSTER_NAME
echo "Controller Image Loaded."

echo "=== 2. Setting up gVisor in KIND Node ==="
./test/e2e/setup_gvisor_node.sh

echo "=== 3. Deploying Control Plane ==="
kubectl apply -f test/e2e/manifests/controller-deploy.yaml
kubectl rollout status deployment/fast-sandbox-controller --timeout=60s

echo "=== 4. Creating gVisor Pool ==="
cat <<EOF > test/e2e/manifests/pool-gvisor.yaml
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: gvisor-pool
  namespace: default
spec:
  capacity:
    poolMin: 1
    poolMax: 2
  maxSandboxesPerPod: 5
  runtimeType: gvisor
  agentTemplate:
    spec:
      containers:
      - name: agent
        image: $AGENT_IMAGE
        imagePullPolicy: IfNotPresent
EOF
kubectl apply -f test/e2e/manifests/pool-gvisor.yaml

echo "=== 5. Waiting for gVisor Agent Pod ==="
sleep 5
kubectl wait --for=condition=ready pod -l fast-sandbox.io/pool=gvisor-pool --timeout=60s
AGENT_POD=$(kubectl get pod -l fast-sandbox.io/pool=gvisor-pool -o jsonpath="{.items[0].metadata.name}")

echo "=== 6. Creating Sandbox in gVisor Pool ==="
cat <<EOF > test/e2e/manifests/sandbox-gvisor.yaml
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: gvisor-sandbox
  namespace: default
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: gvisor-pool
EOF
kubectl apply -f test/e2e/manifests/sandbox-gvisor.yaml

echo "=== 7. Verifying Sandbox Status ==="
for i in {1..30}; do
    PHASE=$(kubectl get sandbox gvisor-sandbox -o jsonpath="{.status.phase}" 2>/dev/null || echo "")
    echo "Check $i: Phase=$PHASE"
    if [[ "$PHASE" == "running" ]]; then
        echo "SUCCESS: gVisor Sandbox is RUNNING!"
        # 终极验证：在容器内检查内核版本 (gVisor 通常显示特定的内核版本，如 4.4.0)
        # kubectl exec -it ... -- uname -a (TODO)
        exit 0
    fi
    sleep 2
done

echo "TIMEOUT: gVisor Sandbox failed to reach running state."
kubectl logs $AGENT_POD --tail=50
exit 1
