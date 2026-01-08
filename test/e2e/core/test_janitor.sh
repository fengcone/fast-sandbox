#!/bin/bash
set -e

# 定义路径
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../../../" && pwd)"
MANIFEST_DIR="$SCRIPT_DIR/manifests"

CLUSTER_NAME="fast-sandbox"
AGENT_IMAGE="fast-sandbox/agent:dev"
CONTROLLER_IMAGE="fast-sandbox/controller:dev"
JANITOR_IMAGE="fast-sandbox/janitor:dev"

echo "=== 0. Deep Cleanup ==="
kubectl delete sandboxpool --all --force --grace-period=0 || true
kubectl delete sandbox --all --force --grace-period=0 || true
kubectl delete deployment fast-sandbox-controller --ignore-not-found=true
kubectl delete ds fast-sandbox-janitor --ignore-not-found=true
kubectl delete pod -l app=sandbox-agent --force --grace-period=0 || true
sleep 5

echo "=== 1. Preparing Environment ==="
cd "$ROOT"
make docker-agent docker-controller docker-janitor
kind load docker-image $CONTROLLER_IMAGE --name $CLUSTER_NAME
kind load docker-image $AGENT_IMAGE --name $CLUSTER_NAME
kind load docker-image $JANITOR_IMAGE --name $CLUSTER_NAME

kubectl apply -f "$MANIFEST_DIR/controller-deploy.yaml"
kubectl apply -f "$MANIFEST_DIR/janitor-deploy.yaml"
kubectl rollout status deployment/fast-sandbox-controller --timeout=60s
kubectl rollout status daemonset/fast-sandbox-janitor --timeout=60s

echo "=== 2. Creating Sandbox and Waiting for Running ==="
cat <<EOF > "$MANIFEST_DIR/pool-janitor-test.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: janitor-pool
  namespace: default
spec:
  capacity:
    poolMin: 1
    poolMax: 1
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers:
      - name: agent
        image: $AGENT_IMAGE
        imagePullPolicy: IfNotPresent
EOF
kubectl apply -f "$MANIFEST_DIR/pool-janitor-test.yaml"

echo "Waiting for janitor-pool agent pod to appear..."
for i in {1..30}; do
    POD_NAME=$(kubectl get pod -l fast-sandbox.io/pool=janitor-pool -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    if [[ "$POD_NAME" != "" ]]; then
        echo "Found agent pod: $POD_NAME"
        break
    fi
    echo "Check $i: Still waiting for pod..."
    sleep 3
done
kubectl wait --for=condition=ready pod -l fast-sandbox.io/pool=janitor-pool --timeout=120s

# 创建一个 Sandbox
cat <<EOF > "$MANIFEST_DIR/sb-janitor.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-janitor-target
  namespace: default
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: janitor-pool
EOF
kubectl apply -f "$MANIFEST_DIR/sb-janitor.yaml"

echo "Waiting for sandbox to be running..."
for i in {1..20}; do
    PHASE=$(kubectl get sandbox sb-janitor-target -o jsonpath="{.status.phase}" 2>/dev/null || echo "")
    if [[ "$PHASE" == "running" ]]; then
        echo "Sandbox is RUNNING."
        break
    fi
    sleep 3
done

AGENT_POD=$(kubectl get sandbox sb-janitor-target -o jsonpath="{.status.assignedPod}")
echo "Sandbox is running on Agent: $AGENT_POD"

# 在宿主机确认容器存在
echo "Confirming container exists in containerd..."
docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io container list | grep sb-janitor-target

echo "=== 3. Simulating Agent Crash (Force Delete) ==="
# 强制删除 Pod，模拟 Agent 崩溃，容器变成孤儿
kubectl delete pod $AGENT_POD --force --grace-period=0

# 等待一小会儿，确认 Pod 已经从 K8s 消失
sleep 5
echo "Checking K8s Pod status..."
kubectl get pod $AGENT_POD 2>&1 || echo "Pod is gone."

echo "=== 4. Waiting for Janitor Cleanup (Checking every 10s) ==="
for i in {1..30}; do
    COUNT=$(docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io container list -q | grep sb-janitor-target | wc -l || echo "0")
    if [ "$COUNT" -eq 0 ]; then
        echo "🎉 SUCCESS: Janitor has recycled the orphan container!"
        break
    fi
    echo "Attempt $i: Container still exists..."
    sleep 10
    if [ $i -eq 30 ]; then
        echo "❌ FAILURE: Janitor failed to cleanup the container."
        kubectl logs -l app=sandbox-janitor --tail=100
        exit 1
    fi
done

# 清理测试资源
kubectl delete sandboxpool janitor-pool --ignore-not-found=true
kubectl delete sandbox sb-janitor-target --ignore-not-found=true
