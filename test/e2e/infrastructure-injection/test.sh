#!/bin/bash
set -e

# --- 1. 配置与环境初始化 ---
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

# trap cleanup_all EXIT

echo "=== [Setup] Building and Installing Infrastructure ==="
setup_env "controller agent"
install_infra

# --- 2. 准备 Pool (注入 InitContainer 已经在代码里默认实现了) ---
cat <<EOF > "$SCRIPT_DIR/manifests/pool.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata: { name: injection-pool }
spec:
  capacity: { poolMin: 1, poolMax: 1 }
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers: [{ name: agent, image: "$AGENT_IMAGE" }]
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/pool.yaml"
if ! wait_for_pod "fast-sandbox.io/pool=injection-pool"; then
    echo "❌ Pod failed to become ready. Debugging info:"
    POD_NAME=$(kubectl get pod -l fast-sandbox.io/pool=injection-pool -o jsonpath='{.items[0].metadata.name}')
    kubectl describe pod "$POD_NAME"
    kubectl logs "$POD_NAME" -c infra-init
    kubectl logs "$POD_NAME" -c agent
    exit 1
fi

# --- 3. 执行核心测试 ---
echo "=== [Test] Creating Sandbox with command 'sleep 3600' ==="
cat <<EOF > "$SCRIPT_DIR/manifests/sandbox.yaml"
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata: { name: sb-injected }
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: injection-pool
EOF
kubectl apply -f "$SCRIPT_DIR/manifests/sandbox.yaml"

echo "Waiting for logs..."
for i in {1..20}; do
    # 通过 Agent 的 API 或 kubectl logs 查看（注意：我们需要获取沙箱内进程的 stdout）
    # 目前我们的 Agent 还没实现 logs API，我们通过 docker exec 直接看 KIND 节点的 containerd 日志
    # 或者简单起见，我们查看沙箱状态，确认它运行成功
    PHASE=$(kubectl get sandbox sb-injected -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    if [[ "$PHASE" == "running" ]]; then
        echo "Sandbox is RUNNING."
        break
    fi
    sleep 5
done

# 核心验证：检查容器内的文件系统和执行流
echo "Checking for injected helper binary..."
if ! docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io container list -q | grep -q "sb-injected"; then
    echo "❌ Container sb-injected not found in containerd. Agent logs:"
    POD_NAME=$(kubectl get pod -l fast-sandbox.io/pool=injection-pool -o jsonpath='{.items[0].metadata.name}')
    kubectl logs "$POD_NAME"
    exit 1
fi
docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io task exec --exec-id check-helper sb-injected ls -l /.fs/helper

echo "Checking execution output (Wrapper prefix)..."
# 注意：ctr task logs 不好拿，我们通过 exec 模拟一次运行
OUT=$(docker exec $CLUSTER_NAME-control-plane ctr -n k8s.io task exec --exec-id test-exec sb-injected /.fs/helper sh -c "echo 'Verified'")
echo "Output: $OUT"

if echo "$OUT" | grep -q "Helper Initiated"; then
    echo "🎉 SUCCESS: Infrastructure helper injected and wrapped successfully!"
else
    echo "❌ FAILURE: Injected helper not executed or output mismatch."
    exit 1
fi
