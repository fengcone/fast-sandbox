#!/bin/bash
set -e

CLUSTER_NAME="fast-sandbox"
AGENT_IMAGE="fast-sandbox/agent:dev"
CONTROLLER_IMAGE="fast-sandbox/controller:dev"

echo "=== 1. Preparing Local KIND Environment ==="
cd "$(dirname "$0")/../../"
# 必须重新构建 Docker 镜像，不仅仅是编译二进制
make docker-controller
make docker-agent

# 重新加载镜像
kind load docker-image $CONTROLLER_IMAGE --name $CLUSTER_NAME
kind load docker-image $AGENT_IMAGE --name $CLUSTER_NAME

# 强制重启部署，确保 imagePullPolicy: IfNotPresent 能拿到刚刚 load 的新镜像
kubectl rollout restart deployment/fast-sandbox-controller
kubectl rollout status deployment/fast-sandbox-controller --timeout=60s

# 预先创建 Pool，确保 Sandbox 能被成功调度（增加真实感）
cat <<EOF > test/e2e/manifests/pool-expiry-test.yaml
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
kubectl apply -f test/e2e/manifests/pool-expiry-test.yaml
echo "Waiting for autoscale-pool pods to appear..."
for i in {1..10}; do
    if kubectl get pod -l fast-sandbox.io/pool=autoscale-pool 2>/dev/null | grep -q "agent"; then
        break
    fi
    sleep 2
done
kubectl wait --for=condition=ready pod -l fast-sandbox.io/pool=autoscale-pool --timeout=60s

echo "=== 2. Creating Sandbox with Expiry (in 20 seconds) ==="
# 计算到期时间 (UTC RFC3339)
EXPIRY_TIME=$(date -u -v+20S +"%Y-%m-%dT%H:%M:%SZ")
echo "Target Expiry Time: $EXPIRY_TIME"

cat <<EOF > test/e2e/manifests/sandbox-expiry.yaml
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

kubectl apply -f test/e2e/manifests/sandbox-expiry.yaml

echo "=== 3. Verifying Sandbox Status ==="
# 给予足够时间进入 Bound/Running
sleep 5
PHASE=$(kubectl get sandbox expiry-sandbox -o jsonpath="{.status.phase}" 2>/dev/null || echo "NotFound")
echo "Current Phase: $PHASE"

echo "=== 4. Waiting for Expiration (30 seconds) ==="
sleep 30

echo "=== 5. Verifying Sandbox is Deleted ==="
FINAL_STATUS=$(kubectl get sandbox expiry-sandbox 2>&1 || echo "Deleted")

if [[ "$FINAL_STATUS" == *"NotFound"* || "$FINAL_STATUS" == "Deleted" ]]; then
    echo "🎉 SUCCESS: Sandbox was automatically garbage collected on KIND!"
else
    echo "❌ FAILURE: Sandbox still exists after expiry time."
    kubectl get sandbox expiry-sandbox -o yaml
    kubectl logs -l control-plane=controller-manager --tail=20
    exit 1
fi
