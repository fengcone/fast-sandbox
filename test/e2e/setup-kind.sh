#!/bin/bash
#
# setup-kind.sh - 初始化 KIND 集群并部署 Fast-Sandbox 组件
#
# 用法:
#   ./setup-kind.sh           # 完整初始化（构建镜像 + 部署）
#   ./setup-kind.sh --skip-build   # 跳过镜像构建（使用已有镜像）
#   ./setup-kind.sh --recreate     # 强制重建集群
#   ./setup-kind.sh --clean        # 仅清理资源
#
# 环境变量:
#   SKIP_BUILD=true        - 跳过镜像构建
#   FORCE_RECREATE_CLUSTER=true - 强制重建 KIND 集群
#

set -e

# 加载公共函数
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/common.sh"

# === 参数解析 ===
CLEAN_ONLY=false
for arg in "$@"; do
    case $arg in
        --skip-build)
            export SKIP_BUILD=true
            ;;
        --recreate)
            export FORCE_RECREATE_CLUSTER=true
            ;;
        --clean)
            CLEAN_ONLY=true
            ;;
        --help|-h)
            echo "用法: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --skip-build     跳过镜像构建，使用已有镜像"
            echo "  --recreate       强制删除并重建 KIND 集群"
            echo "  --clean          仅清理资源（不部署）"
            echo "  --help, -h       显示帮助信息"
            echo ""
            echo "环境变量:"
            echo "  SKIP_BUILD=true              跳过镜像构建"
            echo "  FORCE_RECREATE_CLUSTER=true  强制重建集群"
            exit 0
            ;;
    esac
done

# === 清理模式 ===
if [ "$CLEAN_ONLY" = "true" ]; then
    echo "🧹 执行清理..."
    cleanup_all
    echo "✅ 清理完成"
    exit 0
fi

# === 主流程 ===
echo ""
echo "╔════════════════════════════════════════════════════════════╗"
echo "║          Fast-Sandbox KIND 集群初始化脚本                  ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo ""
echo "配置:"
echo "  集群名称:     $CLUSTER_NAME"
echo "  跳过构建:     ${SKIP_BUILD:-false}"
echo "  强制重建:     ${FORCE_RECREATE_CLUSTER:-false}"
echo ""

# --- Step 1: 检查依赖 ---
echo "=== [Step 1/5] 检查依赖 ==="
for cmd in docker kind kubectl; do
    if ! command -v $cmd &> /dev/null; then
        echo "❌ 缺少依赖: $cmd"
        exit 1
    fi
done
echo "✅ 依赖检查通过"

# --- Step 2: 确保集群存在 ---
echo ""
echo "=== [Step 2/5] 确保 KIND 集群存在 ==="

if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    if [ "$FORCE_RECREATE_CLUSTER" = "true" ]; then
        echo "⚠️  强制重建模式：删除现有集群..."
        kind delete cluster --name "$CLUSTER_NAME"
    else
        echo "✅ 集群 $CLUSTER_NAME 已存在"
        # 确保 kubectl 上下文正确
        kubectl config use-context "kind-$CLUSTER_NAME" || true
    fi
fi

if ! kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    echo "创建 KIND 集群: $CLUSTER_NAME"
    kind create cluster --name "$CLUSTER_NAME" --image kindest/node:v1.27.3
    echo "等待节点就绪..."
    kubectl wait --for=condition=Ready node/"$CLUSTER_NAME-control-plane" --timeout=120s
fi

echo "✅ 集群就绪"

# --- Step 3: 构建和加载镜像 ---
echo ""
echo "=== [Step 3/5] 构建和加载镜像 ==="

cd "$ROOT_DIR"

# 预加载基础镜像
echo "预加载基础镜像..."
for base_image in alpine:latest docker.io/library/alpine:latest; do
    if ! docker image inspect "$base_image" >/dev/null 2>&1; then
        echo "  拉取 $base_image..."
        docker pull "$base_image" || true
    fi
done

# 加载到 KIND
kind load docker-image alpine:latest --name "$CLUSTER_NAME" 2>/dev/null || true

# 构建并加载组件镜像
COMPONENTS="controller agent janitor"
for comp in $COMPONENTS; do
    echo "加载 $comp 到 KIND..."
    make kind-load-"$comp"
done

echo "✅ 镜像就绪"

# --- Step 4: 部署 CRD 和 RBAC ---
echo ""
echo "=== [Step 4/5] 部署 CRD 和 RBAC ==="

cd "$ROOT_DIR"

# 部署 CRD
echo "部署 CRD..."
kubectl apply -f config/crd/

echo "等待 CRD 就绪..."
kubectl wait --for=condition=Established crd/sandboxes.sandbox.fast.io --timeout=30s
kubectl wait --for=condition=Established crd/sandboxpools.sandbox.fast.io --timeout=30s

# 等待 OpenAPI schema 同步
echo "等待 OpenAPI schema 同步..."
sleep 3
count=0
while ! kubectl get crd sandboxes.sandbox.fast.io -o jsonpath='{.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties}' 2>/dev/null | grep -q "poolRef"; do
    if [ $count -gt 15 ]; then
        echo "⚠️  OpenAPI schema 同步超时，继续..."
        break
    fi
    sleep 2
    count=$((count+1))
done

# 部署 RBAC
echo "部署 RBAC..."
kubectl apply -f config/rbac/base.yaml

echo "✅ CRD 和 RBAC 就绪"

# --- Step 5: 部署 Controller 和 Janitor ---
echo ""
echo "=== [Step 5/5] 部署 Controller 和 Janitor ==="

# 清理可能存在的旧部署
kubectl delete deployment fast-sandbox-controller --ignore-not-found=true 2>/dev/null || true
kubectl delete ds -l app=fast-sandbox-janitor --ignore-not-found=true --force --grace-period=0 2>/dev/null || true

# 部署 Controller
echo "部署 Controller..."
kubectl apply -f config/manager/controller.yaml
kubectl rollout status deployment/fast-sandbox-controller --timeout=120s

# 部署 Janitor
echo "部署 Janitor..."
install_janitor

echo "✅ Controller 和 Janitor 就绪"

# --- 完成 ---
echo ""
echo "╔════════════════════════════════════════════════════════════╗"
echo "║                    ✅ 初始化完成                           ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo ""
echo "集群状态:"
kubectl get nodes
echo ""
echo "组件状态:"
kubectl get pods -l app=fast-sandbox-controller
kubectl get pods -l app=fast-sandbox-janitor-e2e
echo ""
echo "下一步:"
echo "  1. 创建 SandboxPool: kubectl apply -f config/samples/pool.yaml"
echo "   - forward port:  kubectl port-forward deployment/fast-sandbox-controller -n default 9090:9090 &"
echo "   - run sandbox:  ./bin/fsb-ctl run fsb-s"
echo "  2. 运行 E2E 测试:    cd test/e2e && ./01-basic-validation/test.sh"
echo ""
