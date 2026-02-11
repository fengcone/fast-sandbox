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
#   USE_CONTAINERD_RUNTIME=true  - 使用 containerd 替代 docker (无 systemd 环境)
#

set -e

# 加载公共函数
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/common.sh"

# === 环境检测函数 ===
function detect_environment() {
    echo "=== [Env] 检测运行环境 ==="

    # 检测 Cgroup 版本
    if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
        # cgroup v2: 单一 unified hierarchy
        CGROUP_VERSION="v2"
        echo "  Cgroup: v2 (unified hierarchy)"
    else
        # cgroup v1: 多个独立 hierarchy
        CGROUP_VERSION="v1"
        echo "  Cgroup: v1 (legacy hierarchy)"
    fi

    # 检测 Systemd
    if [ -d /run/systemd/system ]; then
        SYSTEMD_AVAILABLE="true"
        echo "  Systemd: 可用"
    else
        SYSTEMD_AVAILABLE="false"
        echo "  Systemd: 不可用 (可能使用 containerd 作为 PID 1)"
    fi

    # 检测 Docker 权限
    if docker info >/dev/null 2>&1; then
        DOCKER_AVAILABLE="true"
        echo "  Docker: 可用"
    else
        DOCKER_AVAILABLE="false"
        echo "  Docker: 不可用"

        # 检查权限问题
        if [ -S /var/run/docker.sock ]; then
            USER_IN_DOCKER_GROUP=$(groups 2>/dev/null | grep -qw docker && echo "true" || echo "false")
            if [ "$USER_IN_DOCKER_GROUP" = "false" ]; then
                echo "  ⚠️  用户不在 docker 组中"
                echo "     解决方案: newgrp docker 或 logout 后重新登录"
            else
                echo "  ⚠️  Docker socket 权限问题"
                echo "     解决方案: newgrp docker"
            fi
        fi
    fi

    # 检测 containerd
    if [ -S /run/containerd/containerd.sock ]; then
        CONTAINERD_AVAILABLE="true"
        echo "  Containerd: 可用 (socket: /run/containerd/containerd.sock)"
    else
        CONTAINERD_AVAILABLE="false"
        echo "  Containerd: 不可用"
    fi

    echo ""

    # 确定运行时策略
    if [ "$DOCKER_AVAILABLE" = "true" ]; then
        RUNTIME="docker"
        echo "→ 使用 Docker 作为容器运行时"
    elif [ "$CONTAINERD_AVAILABLE" = "true" ] && [ "$CGROUP_VERSION" = "v1" ]; then
        RUNTIME="containerd"
        export USE_CONTAINERD_RUNTIME=true
        echo "→ 使用 Containerd 作为容器运行时 (无 systemd 环境)"
    else
        echo "❌ 错误: 无法找到可用的容器运行时"
        exit 1
    fi

    echo ""
}

# === 权限检查与修复 ===
function check_and_fix_permissions() {
    if [ "$RUNTIME" = "docker" ]; then
        # 检查 Docker 是否可访问
        if ! docker info >/dev/null 2>&1; then
            echo "⚠️  Docker 权限问题检测"

            # 检查用户是否在 docker 组
            if ! groups 2>/dev/null | grep -qw docker; then
                echo ""
                echo "❌ 错误: 用户不在 docker 组中"
                echo ""
                echo "请执行以下步骤之一:"
                echo "  1. 运行: newgrp docker"
                echo "  2. 或 logout 后重新登录"
                echo "  3. 或运行: sudo usermod -aG docker \$USER"
                exit 1
            fi

            # 用户在组中但权限未生效
            echo ""
            echo "⚠️  用户已在 docker 组，但权限可能未更新"
            echo ""
            echo "请执行: newgrp docker"
            echo ""
            read -p "是否立即执行 newgrp docker 并继续? [y/N] " -n 1 -r
            echo
            if [[ $REPLY =~ ^[Yy]$ ]]; then
                exec newgrp docker
            else
                exit 1
            fi
        fi
    fi
}

# === KIND 集群配置生成 ===
function get_kind_config() {
    # KIND 会自动检测 cgroup driver，不需要手动配置
    # KIND 节点内部使用 systemd，所以 kubelet 会自动使用 systemd cgroup driver
    local config=$(cat <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
EOF
)
    echo "$config"
}

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
            echo "  USE_CONTAINERD_RUNTIME=true  使用 containerd 运行时"
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
echo "  运行时:       ${RUNTIME:-未检测}"
echo ""

# --- Step 0: 环境检测 ---
echo "=== [Step 0/6] 环境检测 ==="
detect_environment
check_and_fix_permissions

# --- Step 1: 检查依赖 ---
echo ""
echo "=== [Step 1/6] 检查依赖 ==="
for cmd in kind kubectl; do
    if ! command -v $cmd &> /dev/null; then
        echo "❌ 缺少依赖: $cmd"
        exit 1
    fi
done

# Docker 在 containerd 模式下不是必须的
if [ "$RUNTIME" = "docker" ]; then
    if ! command -v docker &> /dev/null; then
        echo "❌ 缺少依赖: docker"
        exit 1
    fi
fi

if [ "$RUNTIME" = "containerd" ]; then
    if ! command -v ctr &> /dev/null; then
        echo "❌ 缺少依赖: ctr (containerd CLI)"
        exit 1
    fi
fi

echo "✅ 依赖检查通过"

# --- Step 2: 确保集群存在 ---
echo ""
echo "=== [Step 2/6] 确保 KIND 集群存在 ==="

# 先清理可能存在的旧集群（如果强制重建）
if [ "$FORCE_RECREATE_CLUSTER" = "true" ]; then
    echo "⚠️  强制重建模式：删除现有集群..."
    kind delete cluster --name "$CLUSTER_NAME" 2>/dev/null || true
fi

if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    echo "✅ 集群 $CLUSTER_NAME 已存在"
    kubectl config use-context "kind-$CLUSTER_NAME" 2>/dev/null || true
else
    echo "创建 KIND 集群: $CLUSTER_NAME"

    # 生成 KIND 配置
    KIND_CONFIG_FILE=$(mktemp)
    trap "rm -f $KIND_CONFIG_FILE" EXIT
    get_kind_config > "$KIND_CONFIG_FILE"

    echo "  Cgroup 版本: $CGROUP_VERSION"
    echo "  Systemd: $SYSTEMD_AVAILABLE"

    # 根据运行时选择创建方式
    if [ "$RUNTIME" = "containerd" ]; then
        echo "  使用 containerd 运行时..."
        kind create cluster --name "$CLUSTER_NAME" --config "$KIND_CONFIG_FILE" --image kindest/node:v1.27.3 --retain
    else
        kind create cluster --name "$CLUSTER_NAME" --config "$KIND_CONFIG_FILE" --image kindest/node:v1.27.3
    fi

    echo "等待节点就绪..."
    kubectl wait --for=condition=Ready node/"$CLUSTER_NAME-control-plane" --timeout=120s
fi

echo "✅ 集群就绪"

# --- Step 3: 构建和加载镜像 ---
echo ""
echo "=== [Step 3/6] 构建和加载镜像 ==="

cd "$ROOT_DIR"

# 预加载基础镜像
echo "预加载基础镜像..."
if [ "$RUNTIME" = "docker" ]; then
    for base_image in alpine:latest docker.io/library/alpine:latest; do
        if ! docker image inspect "$base_image" >/dev/null 2>&1; then
            echo "  拉取 $base_image..."
            docker pull "$base_image" || true
        fi
    done
    kind load docker-image alpine:latest --name "$CLUSTER_NAME" 2>/dev/null || true
else
    # containerd 模式使用 ctr
    echo "  使用 ctr 拉取 alpine..."
    ctr -n k8s.io images pull docker.io/library/alpine:latest 2>/dev/null || echo "  ⚠️  拉取失败，继续..."
    ctr -n k8s.io images tag docker.io/library/alpine:latest alpine:latest 2>/dev/null || true
    kind load docker-image alpine:latest --name "$CLUSTER_NAME" 2>/dev/null || true
fi

# 构建并加载组件镜像
COMPONENTS="controller agent janitor"
for comp in $COMPONENTS; do
    if [ "$SKIP_BUILD" != "true" ]; then
        echo "构建 $comp..."
        make "docker-$comp"
    fi

    echo "加载 $comp 到 KIND..."
    if [ "$RUNTIME" = "docker" ]; then
        kind load docker-image "fast-sandbox/$comp:dev" --name "$CLUSTER_NAME"
    else
        # containerd 模式下，kind load 仍然需要 docker
        # 如果不可用，跳过（用户需要手动导入）
        if command -v docker >/dev/null 2>&1; then
            kind load docker-image "fast-sandbox/$comp:dev" --name "$CLUSTER_NAME"
        else
            echo "  ⚠️  无法加载镜像 (Docker 不可用，Kind 需要 Docker 导入镜像)"
            echo "  ⚠️  请确保集群节点可访问镜像仓库"
        fi
    fi
done

echo "✅ 镜像就绪"

# --- Step 4: 部署 CRD 和 RBAC ---
echo ""
echo "=== [Step 4/6] 部署 CRD 和 RBAC ==="

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
echo "=== [Step 5/6] 部署 Controller 和 Janitor ==="

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

# --- Step 6: 验证 ---
echo ""
echo "=== [Step 6/6] 验证部署 ==="

echo "验证 Pod 状态..."
kubectl get pods -l app=fast-sandbox-controller
kubectl get pods -l app=fast-sandbox-janitor-e2e

echo ""
echo "验证节点状态..."
kubectl get nodes

# --- 完成 ---
echo ""
echo "╔════════════════════════════════════════════════════════════════════╗"
echo "║                    ✅ 初始化完成                                   ║"
echo "╚════════════════════════════════════════════════════════════════════╝"
echo ""
echo "环境信息:"
echo "  Cgroup 版本: $CGROUP_VERSION"
echo "  容器运行时: $RUNTIME"
echo "  Systemd: $SYSTEMD_AVAILABLE"
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
