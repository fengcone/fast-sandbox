#!/bin/bash
set -e

CLUSTER_NAME="fast-sandbox"
AGENT_IMAGE="fast-sandbox/agent:dev"
CONTROLLER_IMAGE="fast-sandbox/controller:dev"

echo "=== 0. Cleanup Old Resources ==="
kubectl delete sandboxpool --all --ignore-not-found=true || true
kubectl delete sandbox --all --ignore-not-found=true || true
kubectl delete pods -l app=sandbox-agent --force --grace-period=0 --ignore-not-found=true || true
# Restart controller to clear in-memory registry
kubectl rollout restart deployment/fast-sandbox-controller || true
# kubectl rollout status ... (skip if not exists)

echo "=== 1. Building Images ==="
cd "$(dirname "$0")/../../"
make docker-agent AGENT_IMAGE=$AGENT_IMAGE
make docker-controller CONTROLLER_IMAGE=$CONTROLLER_IMAGE

echo "=== 2. Creating Kind Cluster with KVM support ==="
# 如果集群已存在但配置不对，建议手动删除重启
if ! kind get clusters | grep -q "^$CLUSTER_NAME$"; then
    echo "Cluster $CLUSTER_NAME not found. Creating..."
    # 显式指定镜像名，避免 SHA 校验触发的网络请求
    kind create cluster --name $CLUSTER_NAME --config test/e2e/kind-config-kvm.yaml --image kindest/node:v1.35.0
else
    echo "Cluster $CLUSTER_NAME already exists."
fi
kind load docker-image $AGENT_IMAGE --name $CLUSTER_NAME
kind load docker-image $CONTROLLER_IMAGE --name $CLUSTER_NAME

echo "=== 3. Setting up Firecracker in KIND Node ==="
./test/e2e/setup_firecracker_node.sh

# 清理残留容器
docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io task kill -s SIGKILL vm-sandbox >/dev/null 2>&1 || true
docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io task delete vm-sandbox >/dev/null 2>&1 || true
docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io container delete vm-sandbox >/dev/null 2>&1 || true

echo "=== 4. Deploying Control Plane & VM Pool ==="
kubectl apply -f test/e2e/manifests/controller-deploy.yaml
kubectl rollout status deployment/fast-sandbox-controller --timeout=60s
kubectl apply -f test/e2e/manifests/pool-vm.yaml

echo "=== 5. Waiting for VM Agent Pod ==="
sleep 5
kubectl wait --for=condition=ready pod -l fast-sandbox.io/pool=vm-pool --timeout=60s
AGENT_POD=$(kubectl get pod -l fast-sandbox.io/pool=vm-pool -o jsonpath="{.items[0].metadata.name}")

echo "=== 6. Verifying Agent Runtime Type ==="
kubectl logs $AGENT_POD | grep "Runtime: Type=firecracker"
echo "SUCCESS: Agent is running in Firecracker mode."

echo "=== 7. Creating Sandbox in VM Pool ==="
cat <<EOF > test/e2e/manifests/sandbox-vm.yaml
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: vm-sandbox
  namespace: default
spec:
  image: docker.io/fast-sandbox/agent:dev
  command: ["/bin/sleep", "3600"]
  poolRef: vm-pool
EOF
kubectl apply -f test/e2e/manifests/sandbox-vm.yaml

echo "=== 8. Checking for Sandbox Success ==="
# 轮询状态
for i in {1..30}; do
    PHASE=$(kubectl get sandbox vm-sandbox -o jsonpath="{.status.phase}" 2>/dev/null || echo "")
    echo "Check $i: Phase=$PHASE"
    if [[ "$PHASE" == "running" ]]; then
        echo "SUCCESS: Firecracker Sandbox is RUNNING!"
        exit 0
    fi
    sleep 2
done

echo "TIMEOUT: Sandbox failed to reach running state."
kubectl logs $AGENT_POD --tail=50
exit 1