#!/bin/bash

# --- 通用路径定义 ---
COMMON_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$COMMON_DIR/../../" && pwd)"

CLUSTER_NAME="fast-sandbox"
CONTROLLER_IMAGE="fast-sandbox/controller:dev"
AGENT_IMAGE="fast-sandbox/agent:dev"
JANITOR_IMAGE="fast-sandbox/janitor:dev"

# 环境变量支持
export SKIP_BUILD=${SKIP_BUILD:-""}
export FORCE_RECREATE_CLUSTER=${FORCE_RECREATE_CLUSTER:-"false"}

# --- 0. 集群管理 (强制重建模式) ---
function ensure_cluster() {
    if [ "$FORCE_RECREATE_CLUSTER" = "true" ]; then
        echo "⚠️ [FORCE_RECREATE_CLUSTER] 正在物理销毁并重建 KIND 集群: $CLUSTER_NAME"
        kind delete cluster --name "$CLUSTER_NAME" || true
        # 强制使用本地镜像，避免 pull 失败
        kind create cluster --name "$CLUSTER_NAME" --image kindest/node:v1.35.0
        echo "等待节点就绪..."
        kubectl wait --for=condition=Ready node/"$CLUSTER_NAME-control-plane" --timeout=60s
    fi
}

# --- 1. 清理测试资源 ---
function cleanup_test_resources() {
    local test_namespace=$1
    echo "=== [Cleanup] 清理测试资源 ==="

    if [ "$FORCE_RECREATE_CLUSTER" = "true" ]; then
        echo "由于开启了强制重建模式，跳过细粒度清理，由 ensure_cluster 处理。"
        return
    fi

    if [ -n "$test_namespace" ]; then
        kubectl delete namespace "$test_namespace" --ignore-not-found=true --timeout=60s 2>/dev/null || true
    fi

    kubectl get namespaces -o jsonpath='{.items[*].metadata.name}' | grep -o 'e2e-[^[:space:]]*' 2>/dev/null | while read -r ns; do
        kubectl delete namespace "$ns" --ignore-not-found=true --timeout=30s 2>/dev/null || true
    done

    kubectl delete sandbox --all --all-namespaces --force --grace-period=0 --ignore-not-found=true 2>/dev/null || true
    kubectl delete sandboxpool --all --all-namespaces --force --grace-period=0 --ignore-not-found=true 2>/dev/null || true
}

# --- 2. 环境初始化 (构建与导入) ---
function setup_env() {
    local components=$1 
    echo "=== [Setup] Building and Loading Images: $components ==="
    
    # 确保集群存在
    ensure_cluster

    cd "$ROOT_DIR"
    docker pull alpine:latest >/dev/null 2>&1
    kind load docker-image alpine:latest --name "$CLUSTER_NAME" >/dev/null 2>&1

    for comp in $components; do
        if [ "$SKIP_BUILD" != "true" ]; then
            make "docker-$comp"
        fi
        echo "Loading image fast-sandbox/$comp:dev into $CLUSTER_NAME..."
        kind load docker-image "fast-sandbox/$comp:dev" --name "$CLUSTER_NAME"
    done
}

# --- 3. 部署基础架构 ---
function install_infra() {
    local force_refresh=$1
    echo "=== [Setup] Installing Infrastructure (CRDs, RBAC, Controller) ==="
    cd "$ROOT_DIR"
    
    kubectl apply -f config/crd/
    kubectl wait --for=condition=Established crd/sandboxes.sandbox.fast.io --timeout=30s
    kubectl wait --for=condition=Established crd/sandboxpools.sandbox.fast.io --timeout=30s

    echo "Waiting for OpenAPI schema synchronization..."
    sleep 5
    local count=0
    while ! kubectl get crd sandboxes.sandbox.fast.io -o jsonpath='{.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties}' | grep -q "resetRevision"; do
        if [ $count -gt 20 ]; then exit 1; fi
        sleep 2
        count=$((count+1))
    done

    kubectl apply -f config/rbac/base.yaml
    if [ "$force_refresh" = "true" ] || [ "$FORCE_RECREATE_CLUSTER" = "true" ]; then
        kubectl delete deployment fast-sandbox-controller --ignore-not-found=true 2>/dev/null || true
    fi
    kubectl apply -f config/manager/controller.yaml
    kubectl rollout status deployment/fast-sandbox-controller --timeout=60s
}

# --- 4. 部署 Janitor ---
function install_janitor() {
    echo "=== [Setup] Refreshing Node Janitor ==="
    kubectl delete ds -l app=fast-sandbox-janitor --ignore-not-found=true --force --grace-period=0
    
    cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: DaemonSet
metadata: 
  name: fast-sandbox-janitor-e2e
  labels: { app: fast-sandbox-janitor }
spec:
  selector: { matchLabels: { app: fast-sandbox-janitor-e2e } }
  template:
    metadata: { labels: { app: fast-sandbox-janitor-e2e } }
    spec:
      serviceAccountName: fast-sandbox-manager-role
      tolerations: [{ operator: Exists }]
      containers:
      - name: janitor
        image: $JANITOR_IMAGE
        imagePullPolicy: IfNotPresent
        securityContext: { privileged: true }
        env: [{ name: NODE_NAME, valueFrom: { fieldRef: { fieldPath: spec.nodeName } } }]
        volumeMounts:
        - { name: sock, mountPath: /run/containerd/containerd.sock }
        - { name: fifo, mountPath: /run/containerd/fifo }
      volumes:
      - { name: sock, hostPath: { path: /run/containerd/containerd.sock, type: Socket } }
      - { name: fifo, hostPath: { path: /run/containerd/fifo, type: Directory } }
EOF
    kubectl rollout status daemonset/fast-sandbox-janitor-e2e --timeout=60s
}

# --- 5. 辅助工具：等待 Pod Ready ---
function wait_for_pod() {
    local label=$1
    local timeout=${2:-120}
    local namespace=${3:-default}
    echo "Waiting for pod with label $label in namespace $namespace to exist..."
    
    # 阶段 1: 等待 Pod 对象在 API Server 中出现
    local found=false
    for i in $(seq 1 30); do
        if kubectl get pod -l "$label" -n "$namespace" --no-headers 2>/dev/null | grep -q "."; then
            found=true
            break
        fi
        sleep 2
    done

    if [ "$found" = "false" ]; then
        echo "❌ FAILURE: Pod with label $label never appeared in namespace $namespace"
        return 1
    fi

        # 阶段 2: 等待 Pod 达到 Ready 状态
    echo "Pod appeared, waiting for Ready condition..."
    kubectl wait --for=condition=ready pod -l "$label" -n "$namespace" --timeout="${timeout}s"

    # 关键点：给 Controller 的心跳同步留一点缓冲时间 (默认同步周期是 2s)
    # 确保 Registry 已经感知到该 Agent
    echo "Pod is Ready, waiting for Controller heartbeat sync..."
    sleep 5
}

# --- 6. 环境清理 ---
function cleanup_all() {
    echo "=== [Teardown] Cleaning up all resources ==="
    if [ "$FORCE_RECREATE_CLUSTER" = "true" ]; then
        echo "跳过细粒度清理，环境由下次 ensure_cluster 重置。"
        return
    fi
    kubectl delete sandboxpool --all --force --grace-period=0 --ignore-not-found=true || true
    kubectl delete sandbox --all --force --grace-period=0 --ignore-not-found=true || true
    kubectl delete deployment fast-sandbox-controller --ignore-not-found=true || true
    kubectl delete ds -l app=fast-sandbox-janitor --ignore-not-found=true --force --grace-period=0 || true
    kubectl delete clusterrolebinding fast-sandbox-manager-rolebinding --ignore-not-found=true || true
    kubectl delete clusterrole fast-sandbox-manager-role --ignore-not-found=true || true
    kubectl delete serviceaccount fast-sandbox-manager-role --ignore-not-found=true || true
    kubectl delete -f config/crd/ --ignore-not-found=true || true
}

# 辅助函数
wait_for_condition() {
    local condition=$1; local timeout=${2:-30}; local msg=${3:-"condition not met"}
    local elapsed=0
    while [ $elapsed -lt $timeout ]; do
        if eval "$condition"; then return 0; fi
        sleep 1; elapsed=$((elapsed + 1))
    done
    echo "❌ $msg: timeout"; return 1
}