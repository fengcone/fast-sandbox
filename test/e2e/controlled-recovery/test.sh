#!/bin/bash
set -e

# --- 1. 配置与环境初始化 ---
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

trap cleanup_all EXIT

echo "=== [Setup] Building and Installing Infrastructure ==="
setup_env "controller agent"
install_infra

# --- 2. 准备测试环境 ---
mkdir -p "$SCRIPT_DIR/manifests"
cat <<EOF > "$SCRIPT_DIR/manifests/pool.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata: { name: recovery-pool }
spec:
  capacity: { poolMin: 1, poolMax: 2 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/pool.yaml"
wait_for_pod "fast-sandbox.io/pool=recovery-pool"

# --- 3. 测试 1: 手动重置 (ResetRevision) ---
echo "=== [Test 1] Verifying Manual Reset ==="
cat <<EOF > "$SCRIPT_DIR/manifests/sandbox.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-recovery }
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: recovery-pool
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/sandbox.yaml"

# 等待运行
sleep 15
OLD_ID=$(kubectl get sandbox sb-recovery -o jsonpath='{.status.sandboxID}')
OLD_POD=$(kubectl get sandbox sb-recovery -o jsonpath='{.status.assignedPod}')
echo "Original SandboxID: $OLD_ID on $OLD_POD"

# 触发重置：更新 resetRevision
NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
echo "Patching Sandbox with resetRevision: $NOW"
kubectl patch sandbox sb-recovery --type='merge' -p "{\"spec\": {\"resetRevision\": \"$NOW\"}}"

echo "Waiting for reset execution..."
for i in {1..20}; do
    # 检查 Status 中的 AcceptedResetRevision 是否对齐
    ACCEPTED=$(kubectl get sandbox sb-recovery -o jsonpath='{.status.acceptedResetRevision}' 2>/dev/null || echo "")
    if [[ "$ACCEPTED" == "$NOW" ]]; then
        echo "🎉 SUCCESS: Sandbox reset acknowledged by controller!"
        break
    fi
    echo "Check $i: Still waiting for status update (Got: $ACCEPTED)..."
    sleep 3
    if [ $i -eq 20 ]; then echo "❌ FAILURE: Reset not acknowledged."; exit 1; fi
done

# --- 4. 测试 2: 自动自愈 (AutoRecreate) ---
echo "=== [Test 2] Verifying Auto Recovery (Timeout=15s) ==="
# 设置策略
kubectl patch sandbox sb-recovery --type='merge' -p '{"spec": {"failurePolicy": "AutoRecreate", "recoveryTimeoutSeconds": 15}}'

echo "Deleting Agent Pod to trigger disconnection..."
kubectl delete pod "$OLD_POD" --force --grace-period=0

echo "Waiting for AutoRecreate to trigger..."
for i in {1..30}; do
    PHASE=$(kubectl get sandbox sb-recovery -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    ASSIGNED=$(kubectl get sandbox sb-recovery -o jsonpath='{.status.assignedPod}' 2>/dev/null || echo "")
    # 如果 assignedPod 变了且非空，说明触发了重调度
    if [[ "$ASSIGNED" != "" && "$ASSIGNED" != "$OLD_POD" ]]; then
        echo "🎉 SUCCESS: Auto recovery triggered! Rescheduled to $ASSIGNED"
        exit 0
    fi
    echo "Check $i: Phase=$PHASE, Pod=$ASSIGNED (Waiting for movement...)"
    sleep 5
done

echo "❌ FAILURE: Auto recovery failed to trigger."
exit 1