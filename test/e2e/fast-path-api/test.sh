#!/bin/bash
set -e

# --- 1. 配置与环境初始化 ---
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

trap cleanup_all EXIT

echo "=== [Setup] Building Infrastructure ==="
setup_env "controller agent"
install_infra

# --- 2. 准备测试环境 ---
mkdir -p "$SCRIPT_DIR/manifests"
cat <<EOF > "$SCRIPT_DIR/manifests/pool.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata: { name: fast-path-pool }
spec:
  capacity: { poolMin: 1, poolMax: 2 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/pool.yaml"
wait_for_pod "fast-sandbox.io/pool=fast-path-pool"

# --- 3. 运行 Fast-Path 客户端 ---
echo "=== [Test] Invoking Fast-Path gRPC API ==="

# 建立端口转发，让本地可以访问 Controller 的 gRPC 端口 (9090)
kubectl port-forward deployment/fast-sandbox-controller 9090:9090 &
PF_PID=$!
sleep 5 # 等待转发建立

# 运行 Go 客户端
go run "$SCRIPT_DIR/client/main.go"

# 验证异步 CRD 补齐
echo "Waiting for async CRD creation..."
sleep 5
kubectl get sandboxes

# 检查结果
if kubectl get sandbox | grep -q "sb-"; then
    echo "🎉 SUCCESS: Sandbox CRD found after Fast-Path creation."
else
    echo "❌ FAILURE: Sandbox CRD missing."
    kill $PF_PID
    exit 1
fi

kill $PF_PID
echo "=== [Test Passed] ==="
