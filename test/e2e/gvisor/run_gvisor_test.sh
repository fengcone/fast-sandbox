#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../../../" && pwd)"
MANIFEST_DIR="$SCRIPT_DIR/manifests"
KUBECONFIG_FILE="$SCRIPT_DIR/minikube-gvisor.yaml"

AGENT_IMAGE="fast-sandbox/agent:dev"
CONTROLLER_IMAGE="fast-sandbox/controller:dev"
REMOTE_IP="11.123.71.34"

echo "=== 1. Building and Syncing Images to Remote ECS ==="
cd "$ROOT"
make docker-agent AGENT_IMAGE=$AGENT_IMAGE
make docker-controller CONTROLLER_IMAGE=$CONTROLLER_IMAGE

docker save $AGENT_IMAGE | ssh fengjianhui.fjh@$REMOTE_IP "sudo ctr -n k8s.io images import -"
docker save $CONTROLLER_IMAGE | ssh fengjianhui.fjh@$REMOTE_IP "sudo ctr -n k8s.io images import -"

echo "=== 2. Deploying Control Plane & Pool ==="
# 使用特定的 kubeconfig 操作远程集群
KCMD="kubectl --kubeconfig $KUBECONFIG_FILE"

$KCMD apply -f "$MANIFEST_DIR/controller-deploy.yaml"
$KCMD rollout restart deployment/fast-sandbox-controller
$KCMD rollout status deployment/fast-sandbox-controller --timeout=60s

cat <<EOF > "$MANIFEST_DIR/pool-gvisor-remote.yaml"
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
$KCMD apply -f "$MANIFEST_DIR/pool-gvisor-remote.yaml"

echo "=== 3. Running gVisor Sandbox Test ==="
cat <<EOF > "$MANIFEST_DIR/sandbox-gvisor-remote.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: gvisor-test
  namespace: default
spec:
  image: registry.cn-hangzhou.aliyuncs.com/google_containers/pause:3.9
  command: ["/pause"]
  poolRef: gvisor-pool
EOF
$KCMD apply -f "$MANIFEST_DIR/sandbox-gvisor-remote.yaml"

echo "=== 4. Verifying Status ==="
for i in {1..20}; do
    PHASE=$($KCMD get sandbox gvisor-test -o jsonpath="{.status.phase}" 2>/dev/null || echo "")
    echo "Check $i: Phase=$PHASE"
    if [[ "$PHASE" == "running" ]]; then
        echo "🎉 SUCCESS: gVisor Sandbox is Running on Remote ECS!"
        exit 0
    fi
    sleep 5
done

echo "FAILURE: Sandbox failed to reach running state."
exit 1