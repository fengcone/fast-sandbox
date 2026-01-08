#!/bin/bash
set -e

# 定义路径
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../../../" && pwd)"
MANIFEST_DIR="$SCRIPT_DIR/manifests"

CLUSTER_NAME="fast-sandbox"
AGENT_IMAGE="fast-sandbox/agent:dev"
CONTROLLER_IMAGE="fast-sandbox/controller:dev"

echo "=== 0. Cleanup Old Resources ==="
kubectl delete sandboxpool --all --ignore-not-found=true || true
kubectl delete sandbox --all --ignore-not-found=true || true
kubectl delete pods -l app=sandbox-agent --force --grace-period=0 --ignore-not-found=true || true

echo "=== 0.1 Cleanup KIND Node Residue ==="
if kind get clusters | grep -q "^$CLUSTER_NAME$"; then
    echo "Cleaning up containerd residue..."
    docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io task kill -s SIGKILL sb-1 >/dev/null 2>&1 || true
    docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io task delete sb-1 >/dev/null 2>&1 || true
    docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io container delete sb-1 >/dev/null 2>&1 || true
    docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io task kill -s SIGKILL sb-2 >/dev/null 2>&1 || true
    docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io task delete sb-2 >/dev/null 2>&1 || true
    docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io container delete sb-2 >/dev/null 2>&1 || true
fi

echo "=== 1. Building Images ==="
cd "$ROOT"
make docker-agent AGENT_IMAGE=$AGENT_IMAGE
make docker-controller CONTROLLER_IMAGE=$CONTROLLER_IMAGE

echo "=== 2. Loading Images into Kind ==="
if ! kind get clusters | grep -q "^$CLUSTER_NAME$"; then
    echo "Cluster $CLUSTER_NAME not found. Creating..."
    kind create cluster --name $CLUSTER_NAME --image kindest/node:v1.35.0
fi
kind load docker-image $AGENT_IMAGE --name $CLUSTER_NAME
kind load docker-image $CONTROLLER_IMAGE --name $CLUSTER_NAME

echo "=== 3. Deploying Controller ==="
kubectl apply -f "$MANIFEST_DIR/controller-deploy.yaml"
# 强制重启以确保使用最新加载的镜像
kubectl rollout restart deployment/fast-sandbox-controller
kubectl rollout status deployment/fast-sandbox-controller --timeout=60s

echo "=== 4. Applying SandboxPool (maxSandboxesPerPod=1) ==="
cat <<EOF > "$MANIFEST_DIR/pool-autoscale.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: autoscale-pool
  namespace: default
spec:
  capacity:
    poolMin: 1
    poolMax: 2
    bufferMin: 0
  maxSandboxesPerPod: 1
  runtimeType: container
  agentTemplate:
    spec:
      containers:
      - name: agent
        image: $AGENT_IMAGE
        imagePullPolicy: IfNotPresent
EOF
kubectl apply -f "$MANIFEST_DIR/pool-autoscale.yaml"

echo "=== 5. Waiting for Initial Agent Pod ==="
sleep 5
kubectl wait --for=condition=ready pod -l fast-sandbox.io/pool=autoscale-pool --timeout=60s
echo "Initial Agent Pod is Ready."

echo "=== 6. Applying Sandboxes (Demand for 2 slots) ==="
cat <<EOF > "$MANIFEST_DIR/sb-1.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-1
  namespace: default
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: autoscale-pool
EOF
cat <<EOF > "$MANIFEST_DIR/sb-2.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-2
  namespace: default
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: autoscale-pool
EOF

kubectl apply -f "$MANIFEST_DIR/sb-1.yaml"
sleep 2 # 减少调度冲突
kubectl apply -f "$MANIFEST_DIR/sb-2.yaml"

echo "=== 7. Verifying Autoscaling ==="
echo "Waiting for pool to scale up to 2 pods..."
for i in {1..30}; do
    COUNT=$(kubectl get pods -l fast-sandbox.io/pool=autoscale-pool --no-headers 2>/dev/null | wc -l)
    echo "Check $i: Agent Pod Count=$COUNT"
    if [ "$COUNT" -ge 2 ]; then
        echo "SUCCESS: Pool scaled up to $COUNT pods due to demand!"
        break
    fi
    if [ $i -eq 30 ]; then
        echo "FAILURE: Pool failed to scale up."
        exit 1
    fi
    sleep 3
done

echo "Giving new agent 15s to register..."
sleep 15

echo "=== 8. Verifying Running Status ==="
for i in {1..30}; do
    P1=$(kubectl get sandbox sb-1 -o jsonpath="{.status.phase}" 2>/dev/null || echo "")
    P2=$(kubectl get sandbox sb-2 -o jsonpath="{.status.phase}" 2>/dev/null || echo "")
    echo "Check $i: sb-1 phase=$P1, sb-2 phase=$P2"
    if [[ "$P1" == "running" && "$P2" == "running" ]]; then
        echo "SUCCESS: Both sandboxes are RUNNING on separate agents!"
        exit 0
    fi
    sleep 5
done

echo "FAILURE: Sandboxes failed to reach running state."
kubectl get sandbox
exit 1
