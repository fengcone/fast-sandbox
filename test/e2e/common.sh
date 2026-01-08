#!/bin/bash

# --- 通用路径定义 ---
# 注意：假设从 test/e2e/<case>/test.sh 调用，脚本所在目录是 $SCRIPT_DIR
COMMON_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$COMMON_DIR/../../" && pwd)"

CLUSTER_NAME="fast-sandbox"
CONTROLLER_IMAGE="fast-sandbox/controller:dev"
AGENT_IMAGE="fast-sandbox/agent:dev"
JANITOR_IMAGE="fast-sandbox/janitor:dev"

# --- 1. 环境初始化 (构建与导入) ---
function setup_env() {
    local components=$1 # e.g. "controller agent"
    echo "=== [Setup] Building and Loading Images: $components ==="
    cd "$ROOT_DIR"
    
    for comp in $components; do
        make "docker-$comp"
        kind load docker-image "fast-sandbox/$comp:dev" --name "$CLUSTER_NAME"
    done
}

# --- 2. 部署基础架构 (CRD, RBAC, Controller) ---
function install_infra() {
    echo "=== [Setup] Installing Infrastructure (CRDs, RBAC, Controller) ==="
    cd "$ROOT_DIR"
    
    # 始终使用 config/ 下的唯一真理
    kubectl apply -f config/crd/
    kubectl apply -f config/rbac/base.yaml
    kubectl apply -f config/manager/controller.yaml
    
    kubectl rollout status deployment/fast-sandbox-controller --timeout=60s
}

# --- 3. 部署 Janitor (可选) ---
function install_janitor() {
    echo "=== [Setup] Installing Node Janitor ==="
    # 临时从 common 目录或 manifests 生成
    cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: DaemonSet
metadata: { name: janitor-e2e }
spec:
  selector: { matchLabels: { app: janitor-e2e } }
  template:
    metadata: { labels: { app: janitor-e2e } }
    spec:
      serviceAccountName: fast-sandbox-manager-role
      tolerations: [{ operator: Exists }]
      containers:
      - name: janitor
        image: $JANITOR_IMAGE
        securityContext: { privileged: true }
        env: [{ name: NODE_NAME, valueFrom: { fieldRef: { fieldPath: spec.nodeName } } }]
        volumeMounts:
        - { name: sock, mountPath: /run/containerd/containerd.sock }
        - { name: fifo, mountPath: /run/containerd/fifo }
      volumes:
      - { name: sock, hostPath: { path: /run/containerd/containerd.sock, type: Socket } }
      - { name: fifo, hostPath: { path: /run/containerd/fifo, type: Directory } }
EOF
    kubectl rollout status daemonset/janitor-e2e --timeout=60s
}

# --- 4. 辅助工具：等待 Pod Ready ---
function wait_for_pod() {
    local label=$1
    local timeout=${2:-120}
    echo "Waiting for pod with label $label to be ready..."
    for i in $(seq 1 20); do
        if kubectl get pod -l "$label" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null | grep -q "."; then
            break
        fi
        sleep 3
    done
    kubectl wait --for=condition=ready pod -l "$label" --timeout="${timeout}s"
}

# --- 5. 环境清理 (Teardown) ---
function cleanup_all() {
    echo "=== [Teardown] Cleaning up all resources ==="
    kubectl delete sandboxpool --all --force --grace-period=0 --ignore-not-found=true || true
    kubectl delete sandbox --all --force --grace-period=0 --ignore-not-found=true || true
    kubectl delete deployment fast-sandbox-controller --ignore-not-found=true || true
    kubectl delete ds janitor-e2e --ignore-not-found=true || true
    kubectl delete clusterrolebinding fast-sandbox-manager-rolebinding --ignore-not-found=true || true
    kubectl delete clusterrole fast-sandbox-manager-role --ignore-not-found=true || true
    kubectl delete serviceaccount fast-sandbox-manager-role --ignore-not-found=true || true
    # 注意：根据原则，这里可以选择是否删除 CRD。为了彻底独立，我们选择删除。
    kubectl delete -f config/crd/ --ignore-not-found=true || true
}
