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
    # 预拉取基础镜像以防 InitContainer 失败
    if ! docker image inspect alpine:latest >/dev/null 2>&1; then
        echo "Pulling alpine:latest..."
        docker pull alpine:latest || true
    else
        echo "Image alpine:latest found locally, skipping pull."
    fi
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
        command: ["/janitor"]
        args: ["--scan-interval=10s", "--orphan-timeout=10s"]
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

# --- 5. 辅助工具 ---
function wait_for_pod() {
    local label=$1
    local timeout=${2:-300} # 增加默认超时到 300s
    local namespace=${3:-default}
    echo "Waiting for pod with label $label in namespace $namespace..."
    
    # 增加等待对象出现的轮询次数
    for i in $(seq 1 60); do
        if kubectl get pod -l "$label" -n "$namespace" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null | grep -q "."; then
            break
        fi
        sleep 3
    done
    kubectl wait --for=condition=ready pod -l "$label" -n "$namespace" --timeout="${timeout}s"

    # 关键点：给 Controller 的心跳同步留一点缓冲时间 (默认同步周期是 2s)
    # 确保 Registry 已经感知到该 Agent
    echo "Pod is Ready, waiting for Controller heartbeat sync..."
    sleep 15
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

# --- 7. 测试运行框架 (Test Runner) ---

# 全局变量
PASSED=()
FAILED=()

# 运行单个 Case 文件
function run_case() {
    local case_file=$1
    local case_name=$(basename "$case_file" .sh)

    # 清理之前 case 可能遗留的函数
    unset -f describe precondition run 2>/dev/null || true

    echo ""
    echo "========================================"
    echo "📋 Case: $case_name"
    source "$case_file"

    if declare -f describe > /dev/null; then
        echo "📝 $(describe)"
    fi

    if declare -f precondition > /dev/null; then
        if ! precondition; then
            echo "⏭️  跳过 (前置条件不满足)"
            unset -f describe precondition run 2>/dev/null || true
            return 0
        fi
    fi

    if declare -f run > /dev/null; then
        if run; then
            echo "✅ PASSED: $case_name"
            PASSED+=("$case_name")
        else
            echo "❌ FAILED: $case_name"
            FAILED+=("$case_name")
            
            # 自动 Dump 现场日志
            echo "--- [DEBUG] Controller Logs (Tail 50) ---"
            kubectl logs -l app=fast-sandbox-controller -n default --tail=50 || true
            echo "--- [DEBUG] Agent Logs (Tail 50) ---"
            kubectl logs -l app=sandbox-agent -n "$TEST_NS" --all-containers --tail=50 || true
            echo "--- [DEBUG] Janitor Logs (Tail 50) ---"
            kubectl logs -l app=fast-sandbox-janitor -n default --tail=50 || true
        fi
    fi

    # 清理当前 case 的函数
    unset -f describe precondition run 2>/dev/null || true
}

# 打印结果汇总
function cleanup_and_report() {
    local exit_code=$?
    echo ""
    echo "========================================"
    echo "📊 测试结果汇总"
    echo "----------------------------------------"
    echo "✅ 通过: ${#PASSED[@]}"
    echo "❌ 失败: ${#FAILED[@]}"

    if [ ${#FAILED[@]} -gt 0 ]; then
        echo ""
        echo "失败的测试:"
        for name in "${FAILED[@]}"; do
            echo "  - $name"
        done
        exit 1
    fi
    exit $exit_code
}

# 通用测试套件入口
# 参数:
#   $1: 套件目录 (SCRIPT_DIR)
#   $2: 过滤参数 (FILTER)
#   $3: 初始化回调函数名 (可选)
function run_test_suite() {
    local suite_dir=$1
    local filter=$2
    local setup_func=$3

    echo "🚀 E2E 测试套件: $(basename "$suite_dir")"
    echo "========================================"

    # 注册退出清理
    trap cleanup_and_report EXIT

    # 执行初始化
    if [ -n "$setup_func" ] && declare -f "$setup_func" > /dev/null; then
        $setup_func
    fi

    # 扫描并运行测试
    for case in "${suite_dir}"/*.sh; do
        local case_name=$(basename "$case" .sh)
        local case_file=$(basename "$case")
        
        # 跳过入口脚本本身 (test.sh)
        if [ "$case_file" = "test.sh" ]; then
            continue
        fi

        # 过滤逻辑
        if [ -n "$filter" ]; then
            if [[ "$case_name" != *"$filter"* ]]; then
                continue
            fi
        fi

        if [ -f "$case" ]; then
            run_case "$case"
        fi
    done
}

# --- 8. Case 测试辅助函数 ---
wait_for_condition() {
    local condition=$1; local timeout=${2:-30}; local msg=${3:-"condition not met"}
    local elapsed=0
    while [ $elapsed -lt $timeout ]; do
        if eval "$condition"; then return 0; fi
        sleep 1; elapsed=$((elapsed + 1))
    done
    echo "❌ $msg: timeout"; return 1
}