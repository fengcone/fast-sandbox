#!/bin/bash
set -e

# 定义路径
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../../../" && pwd)"
MANIFEST_DIR="$SCRIPT_DIR/manifests"

CLUSTER_NAME="fast-sandbox"
AGENT_IMAGE="fast-sandbox/agent:dev"
CONTROLLER_IMAGE="fast-sandbox/controller:dev"

echo "=== 1. Preparing Environment ==="
cd "$ROOT"
make docker-agent docker-controller
kind load docker-image $CONTROLLER_IMAGE --name $CLUSTER_NAME
kind load docker-image $AGENT_IMAGE --name $CLUSTER_NAME

kubectl apply -f "$MANIFEST_DIR/controller-deploy.yaml"
kubectl rollout restart deployment/fast-sandbox-controller
kubectl rollout status deployment/fast-sandbox-controller --timeout=60s

echo "=== 2. Creating Pool with Explicit Resources (2000m CPU, 2 Slots) ==="
cat <<EOF > "$MANIFEST_DIR/pool-resource-test.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: resource-pool
  namespace: default
spec:
  capacity:
    poolMin: 1
    poolMax: 1
  maxSandboxesPerPod: 2
  runtimeType: container
  agentTemplate:
    spec:
      containers:
      - name: agent
        image: $AGENT_IMAGE
        imagePullPolicy: IfNotPresent
        resources:
          limits:
            cpu: "2000m"
            memory: "1Gi"
EOF
kubectl apply -f "$MANIFEST_DIR/pool-resource-test.yaml"

echo "Waiting for agent pod..."
sleep 15
kubectl wait --for=condition=ready pod -l fast-sandbox.io/pool=resource-pool --timeout=120s

# 创建一个 Sandbox
cat <<EOF > "$MANIFEST_DIR/sb-resource.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-resource-target
  namespace: default
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: resource-pool
EOF
kubectl apply -f "$MANIFEST_DIR/sb-resource.yaml"

echo "Waiting for slot allocation log in Agent..."
# 我们不看容器运行状态（因为 KIND 嵌套 Cgroup 可能会挂），我们看 Agent 是否计算出了正确的资源
for i in {1..20}; do
    LOGS=$(kubectl logs -l fast-sandbox.io/pool=resource-pool --tail=100 2>/dev/null || echo "")
    if echo "$LOGS" | grep -q "RESOURCES_VERIFY: Slot allocated for sb-resource-target: CPU=1000m, Memory=536870912 bytes"; then
        echo "🎉 SUCCESS: Resource Slot Calculation verified! (1000m CPU, 512Mi Memory)"
        exit 0
    fi
    echo "Check $i: Waiting for allocation log..."
    sleep 5
done

echo "❌ FAILURE: Resource allocation log not found in Agent."
kubectl logs -l fast-sandbox.io/pool=resource-pool --tail=50
exit 1