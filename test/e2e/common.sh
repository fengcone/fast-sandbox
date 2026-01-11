#!/bin/bash

# --- 通用路径定义 ---
COMMON_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$COMMON_DIR/../../" && pwd)"

CLUSTER_NAME="fast-sandbox"
CONTROLLER_IMAGE="fast-sandbox/controller:dev"
AGENT_IMAGE="fast-sandbox/agent:dev"
JANITOR_IMAGE="fast-sandbox/janitor:dev"

# 环境变量：设置为 "true" 时跳过构建镜像（用于快速重测）
export SKIP_BUILD=${SKIP_BUILD:-""}

# --- 1. 清理测试资源 (每次测试前运行) ---
function cleanup_test_resources() {
    local test_namespace=$1
    echo "=== [Cleanup] 清理测试资源 ==="

    # 删除指定的测试命名空间
    if [ -n "$test_namespace" ]; then
        kubectl delete namespace "$test_namespace" --ignore-not-found=true --timeout=60s 2>/dev/null || true
    fi

    # 清理所有 e2e- 开头的测试命名空间
    kubectl get namespaces -o jsonpath='{.items[*].metadata.name}' | grep -o 'e2e-[^[:space:]]*' 2>/dev/null | while read -r ns; do
        kubectl delete namespace "$ns" --ignore-not-found=true --timeout=30s 2>/dev/null || true
    done

    # 删除所有 Sandbox 和 SandboxPool
    kubectl delete sandbox --all --all-namespaces --force --grace-period=0 --ignore-not-found=true 2>/dev/null || true
    kubectl delete sandboxpool --all --all-namespaces --force --grace-period=0 --ignore-not-found=true 2>/dev/null || true

    echo "✓ 测试资源清理完成"
}

# --- 2. 环境初始化 (构建与导入) ---
function setup_env() {
    local components=$1 # e.g. "controller agent janitor"
    echo "=== [Setup] Building and Loading Images: $components ==="
    cd "$ROOT_DIR"

    # 预拉取基础镜像以防 InitContainer 失败
    docker pull alpine:latest >/dev/null 2>&1
    kind load docker-image alpine:latest --name "$CLUSTER_NAME" >/dev/null 2>&1

    for comp in $components; do
        if [ -z "$SKIP_BUILD" ]; then
            make "docker-$comp"
        fi
        kind load docker-image "fast-sandbox/$comp:dev" --name "$CLUSTER_NAME"
    done
}

# --- 3. 部署基础架构 (CRD, RBAC, Controller) ---
function install_infra() {
    local force_refresh=$1  # 设置为 "true" 时强制重装 Controller

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

    # 如果强制刷新，先删除旧的 Controller
    if [ "$force_refresh" = "true" ]; then
        kubectl delete deployment fast-sandbox-controller --ignore-not-found=true 2>/dev/null || true
    fi

    kubectl apply -f config/manager/controller.yaml

    kubectl rollout status deployment/fast-sandbox-controller --timeout=60s
}

# --- 4. 部署 Janitor (强制刷新) ---
function install_janitor() {
    echo "=== [Setup] Refreshing Node Janitor ==="
    
    # 先删除可能存在的旧实例（无论名字是 janitor-e2e 还是 fast-sandbox-janitor）
    kubectl delete ds -l app=fast-sandbox-janitor --ignore-not-found=true --force --grace-period=0
    kubectl delete ds janitor-e2e --ignore-not-found=true --force --grace-period=0

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
    echo "Waiting for pod with label $label in namespace $namespace to be ready..."
    for i in $(seq 1 20); do
        if kubectl get pod -l "$label" -n "$namespace" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null | grep -q "."; then
            break
        fi
        sleep 3
    done
    kubectl wait --for=condition=ready pod -l "$label" -n "$namespace" --timeout="${timeout}s"
}

# --- 6. 完全清理 (包括基础设施，用于手动清理) ---
function cleanup_all_infra() {
    echo "=== [Cleanup] 清理所有基础设施 ==="

    # 清理测试资源
    cleanup_test_resources

    # 删除 Controller
    kubectl delete deployment fast-sandbox-controller --ignore-not-found=true 2>/dev/null || true

    # 清理所有 Janitor 变体
    kubectl delete ds -l app=fast-sandbox-janitor --ignore-not-found=true 2>/dev/null || true
    kubectl delete ds fast-sandbox-janitor-e2e --ignore-not-found=true 2>/dev/null || true
    kubectl delete ds janitor-e2e --ignore-not-found=true 2>/dev/null || true

    # 删除 RBAC
    kubectl delete clusterrolebinding fast-sandbox-manager-rolebinding --ignore-not-found=true 2>/dev/null || true
    kubectl delete clusterrole fast-sandbox-manager-role --ignore-not-found=true 2>/dev/null || true
    kubectl delete serviceaccount fast-sandbox-manager-role --ignore-not-found=true 2>/dev/null || true

    # 删除 CRD
    kubectl delete crd sandboxes.sandbox.fast.io --ignore-not-found=true 2>/dev/null || true
    kubectl delete crd sandboxpools.sandbox.fast.io --ignore-not-found=true 2>/dev/null || true

    echo "✓ 基础设施清理完成"
}

# --- 7. Case 测试辅助函数 ---

# 断言相等
assert_equals() {
    local expected=$1
    local actual=$2
    local msg=${3:-"assertion failed"}

    if [ "$expected" != "$actual" ]; then
        echo "  ❌ $msg: expected='$expected', actual='$actual'"
        return 1
    fi
    return 0
}

# 断言包含
assert_contains() {
    local haystack=$1
    local needle=$2
    local msg=${3:-"assertion failed"}

    if echo "$haystack" | grep -q "$needle"; then
        return 0
    fi
    echo "  ❌ $msg: '$needle' not found in '$haystack'"
    return 1
}

# 断言命令成功
assert_success() {
    local cmd=$1
    local msg=${2:-"command failed"}

    if eval "$cmd" > /dev/null 2>&1; then
        return 0
    fi
    echo "  ❌ $msg: command '$cmd' failed"
    return 1
}

# 断言命令失败
assert_fails() {
    local cmd=$1
    local msg=${2:-"command should fail"}

    if eval "$cmd" > /dev/null 2>&1; then
        echo "  ❌ $msg: command '$cmd' should have failed"
        return 1
    fi
    return 0
}

# 等待条件满足（带超时）
wait_for_condition() {
    local condition=$1
    local timeout=${2:-30}
    local msg=${3:-"condition not met"}

    local elapsed=0
    while [ $elapsed -lt $timeout ]; do
        if eval "$condition"; then
            return 0
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done

    echo "  ❌ $msg: timeout after ${timeout}s"
    return 1
}
