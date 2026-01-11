#!/bin/bash
set -e

# --- Finalizer 清理测试 ---
# 测试目标：
# 1. 删除 Sandbox 时 finalizer 被正确移除
# 2. 资源被正确释放
# 3. 删除后 Registry 插槽被释放，可以调度新的 Sandbox

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

trap cleanup_all EXIT

echo "=== [Setup] Building and Installing Infrastructure ==="
setup_env "controller agent"
install_infra

# --- 1. 准备 Pool (容量为 1，用于验证资源释放) ---
cat <<EOF > "$SCRIPT_DIR/manifests/pool.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata: { name: finalizer-test-pool }
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 2
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/pool.yaml"
wait_for_pod "fast-sandbox.io/pool=finalizer-test-pool"

# 获取 Agent Pod 名称
AGENT_POD=$(kubectl get pod -l fast-sandbox.io/pool=finalizer-test-pool -o jsonpath='{.items[0].metadata.name}')
echo "Agent Pod: $AGENT_POD"

# --- 2. 创建第一个 Sandbox，占用插槽 ---
echo "=== [Test] Creating Sandbox A to consume slot ==="
cat <<EOF > "$SCRIPT_DIR/manifests/sb-a.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-finalizer-a }
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: finalizer-test-pool
  exposedPorts: [8080]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/sb-a.yaml"

# 等待 A 运行
sleep 10
PHASE_A=$(kubectl get sandbox sb-finalizer-a -o jsonpath='{.status.phase}')
echo "Sandbox A Phase: $PHASE_A"

# 转换为小写进行比较（phase 值可能是 running 或 Running）
PHASE_A_LOWER=$(echo "$PHASE_A" | tr '[:upper:]' '[:lower:]')
if [[ "$PHASE_A_LOWER" != "running" && "$PHASE_A_LOWER" != "bound" ]]; then
    echo "❌ FAILURE: Sandbox A not running, phase: $PHASE_A"
    kubectl get sandbox sb-finalizer-a -o yaml
    exit 1
fi

# --- 3. 验证 finalizer 存在 ---
echo "=== [Test] Verifying finalizer exists ==="
FINALIZERS=$(kubectl get sandbox sb-finalizer-a -o jsonpath='{.metadata.finalizers}')
if [[ "$FINALIZERS" != *"sandbox.fast.io/cleanup"* ]]; then
    echo "❌ FAILURE: Finalizer not found"
    echo "Finalizers: $FINALIZERS"
    exit 1
fi
echo "✓ Finalizer present: sandbox.fast.io/cleanup"

# --- 4. 删除 Sandbox A ---
echo "=== [Test] Deleting Sandbox A ==="
kubectl delete sandbox sb-finalizer-a

# 等待删除完成
echo "Waiting for Sandbox to be deleted..."
for i in {1..30}; do
    if ! kubectl get sandbox sb-finalizer-a >/dev/null 2>&1; then
        echo "✓ Sandbox deleted successfully"
        break
    fi
    if [[ $i -eq 30 ]]; then
        echo "❌ FAILURE: Sandbox deletion timeout"
        kubectl get sandbox sb-finalizer-a -o yaml
        exit 1
    fi
    sleep 2
done

# --- 5. 创建第二个 Sandbox，验证插槽已释放 ---
echo "=== [Test] Creating Sandbox B to verify slot released ==="
cat <<EOF > "$SCRIPT_DIR/manifests/sb-b.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-finalizer-b }
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: finalizer-test-pool
  exposedPorts: [8081]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/sb-b.yaml"

# 等待 B 运行
sleep 10
PHASE_B=$(kubectl get sandbox sb-finalizer-b -o jsonpath='{.status.phase}')
ASSIGNED_POD_B=$(kubectl get sandbox sb-finalizer-b -o jsonpath='{.status.assignedPod}')

echo "Sandbox B Phase: $PHASE_B"
echo "Sandbox B Assigned Pod: $ASSIGNED_POD_B"

# 转换为小写进行比较
PHASE_B_LOWER=$(echo "$PHASE_B" | tr '[:upper:]' '[:lower:]')
if [[ "$PHASE_B_LOWER" != "running" && "$PHASE_B_LOWER" != "bound" ]]; then
    echo "❌ FAILURE: Sandbox B not running, phase: $PHASE_B"
    kubectl get sandbox sb-finalizer-b -o yaml
    exit 1
fi

# 验证 B 被分配到了同一个 Agent Pod（说明插槽被正确释放）
if [[ "$ASSIGNED_POD_B" != "$AGENT_POD" ]]; then
    echo "❌ FAILURE: Slot not properly released. B assigned to $ASSIGNED_POD_B, expected $AGENT_POD"
    exit 1
fi

echo "✓ Slot was properly released after Sandbox A deletion"

# --- 6. 清理测试资源 ---
kubectl delete sandbox sb-finalizer-b

echo "🎉 SUCCESS: Finalizer cleanup test passed!"
echo "- Finalizer was correctly applied"
echo "- Resources were properly released on deletion"
echo "- Registry slot was available for new Sandbox"
