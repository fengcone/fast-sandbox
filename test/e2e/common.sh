#!/bin/bash

# --- 通用路径定义 ---
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
    
    # 预拉取基础镜像以防 InitContainer 失败
    docker pull alpine:latest
    kind load docker-image alpine:latest --name "$CLUSTER_NAME"

    for comp in $components; do
        make "docker-$comp"
        kind load docker-image "fast-sandbox/$comp:dev" --name "$CLUSTER_NAME"
    done
}

# --- 2. 部署基础架构 (CRD, RBAC, Controller) ---
function install_infra() {
    echo "=== [Setup] Installing Infrastructure (CRDs, RBAC, Controller) ==="
    cd "$ROOT_DIR"
    
    # 1. 安装 CRD 并等待其在 API Server 层面完全就绪
    kubectl apply -f config/crd/
    kubectl wait --for=condition=Established crd/sandboxes.sandbox.fast.io --timeout=30s
    kubectl wait --for=condition=Established crd/sandboxpools.sandbox.fast.io --timeout=30s

    # 强制静默 5 秒，给 API Server 刷新 Discovery 缓存的时间
    echo "Waiting for OpenAPI schema synchronization..."
    sleep 5

    local count=0
    while ! kubectl get crd sandboxes.sandbox.fast.io -o jsonpath='{.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties}' | grep -q "resetRevision"; do
        if [ $count -gt 20 ]; then
            echo "❌ Timeout waiting for CRD schema sync"
            exit 1
        fi
        echo "Still waiting for resetRevision field to appear..."
        sleep 2
        count=$((count+1))
    done
    echo "Schema synced successfully."

    # 2. 安装 RBAC 和 Controller
    kubectl apply -f config/rbac/base.yaml
    kubectl apply -f config/manager/controller.yaml
    
    kubectl rollout status deployment/fast-sandbox-controller --timeout=60s
}

# --- 3. 部署 Janitor (可选) ---
function install_janitor() {
    echo "=== [Setup] Installing Node Janitor ==="
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
    kubectl delete -f config/crd/ --ignore-not-found=true || true
}