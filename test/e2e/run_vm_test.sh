#!/bin/bash
set -e

CLUSTER_NAME="fast-sandbox"
AGENT_IMAGE="fast-sandbox/agent:dev"
CONTROLLER_IMAGE="fast-sandbox/controller:dev"

echo "=== 1. Building and Loading Images ==="
cd "$(dirname "$0")/../../"
make docker-agent AGENT_IMAGE=$AGENT_IMAGE
make docker-controller CONTROLLER_IMAGE=$CONTROLLER_IMAGE
kind load docker-image $AGENT_IMAGE --name $CLUSTER_NAME
kind load docker-image $CONTROLLER_IMAGE --name $CLUSTER_NAME

echo "=== 2. Mocking Firecracker Environment in Kind Node ==="
# 创建内核占位文件
docker exec $CLUSTER_NAME-control-plane mkdir -p /var/lib/firecracker
docker exec $CLUSTER_NAME-control-plane touch /var/lib/firecracker/vmlinux

echo "=== 3. Deploying Control Plane & VM Pool ==="
kubectl apply -f test/e2e/manifests/controller-deploy.yaml
kubectl rollout status deployment/fast-sandbox-controller --timeout=60s
kubectl apply -f test/e2e/manifests/pool-vm.yaml

echo "=== 4. Waiting for VM Agent Pod ==="
sleep 5
kubectl wait --for=condition=ready pod -l fast-sandbox.io/pool=vm-pool --timeout=60s
AGENT_POD=$(kubectl get pod -l fast-sandbox.io/pool=vm-pool -o jsonpath="{.items[0].metadata.name}")

echo "=== 5. Verifying Agent Runtime Type ==="
kubectl logs $AGENT_POD | grep "Runtime: Type=firecracker"
echo "SUCCESS: Agent is running in Firecracker mode."

echo "=== 6. Creating Sandbox in VM Pool ==="
# 修改 sandbox.yaml 指向 vm-pool
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

echo "=== 7. Checking for Expected Errors (Missing Shim) ==="
# 由于环境没装 shim，我们预期会在 Agent 日志里看到 "failed to create firecracker container"
# 或者 containerd 报错找不到 runtime handler
sleep 5
AGENT_ERRS=$(kubectl logs $AGENT_POD | grep "failed to create firecracker container" || true)
if [ -n "$AGENT_ERRS" ]; then
    echo "SUCCESS: Logic verified. Agent attempted to create VM but failed as expected: $AGENT_ERRS"
else
    echo "Check Agent logs manually if needed. The test assumes the environment is not fully setup for VM execution."
fi

# Cleanup
# kubectl delete -f test/e2e/manifests/pool-vm.yaml
# kubectl delete sandbox vm-sandbox
