# Fast-Sandbox 本地开发环境初始化指南

本文档介绍如何使用 KIND (Kubernetes in Docker) 快速搭建 Fast-Sandbox 的本地开发环境。

## 前置要求

- Docker (>= 20.10)
- Go (>= 1.25)
- Make
- kubectl (>= 1.27)
- kind (>= 0.20)

## 快速开始

### 1. 创建 KIND 集群

```bash
# 创建名为 fast-sandbox 的 KIND 集群
kind create cluster --name fast-sandbox --image kindest/node:v1.35.0

# 验证集群状态
kubectl cluster-info --context kind-fast-sandbox
kubectl get nodes
```

### 2. 构建并加载镜像

```bash
# 进入项目目录
cd /path/to/fast-sandbox

# 构建所有组件镜像
make docker-controller
make docker-agent
make docker-janitor

# 加载镜像到 KIND 集群
kind load docker-image fast-sandbox/controller:dev --name fast-sandbox
kind load docker-image fast-sandbox/agent:dev --name fast-sandbox
kind load docker-image fast-sandbox/janitor:dev --name fast-sandbox
```

### 3. 安装 CRD 和 Controller

```bash
# 安装 CRD
kubectl apply -f config/crd/

# 等待 CRD 就绪
kubectl wait --for=condition=Established crd/sandboxes.sandbox.fast.io --timeout=30s
kubectl wait --for=condition=Established crd/sandboxpools.sandbox.fast.io --timeout=30s

# 安装 RBAC
kubectl apply -f config/rbac/base.yaml

# 部署 Controller
kubectl apply -f config/manager/controller.yaml

# 等待 Controller 就绪
kubectl rollout status deployment/fast-sandbox-controller --timeout=60s
```

### 4. 安装 Janitor（可选但推荐）

Janitor 是核心组件，负责清理孤儿容器和资源。

```bash
# 部署 Janitor DaemonSet
kubectl apply -f config/janitor/janitor.yaml

# 等待 Janitor 就绪
kubectl rollout status daemonset/fast-sandbox-janitor --timeout=60s
```

### 5. 验证安装

```bash
# 检查 Controller Pod
kubectl get pods -l control-plane=controller-manager

# 检查 Janitor DaemonSet
kubectl get pods -l app=fast-sandbox-janitor

# 检查 CRD
kubectl get crd | grep sandbox

# 查看 Controller 日志
kubectl logs -l control-plane=controller-manager --tail=20

# 查看 Janitor 日志
kubectl logs -l app=fast-sandbox-janitor --tail=20
```

---

## 一键初始化脚本

### 完整初始化（推荐）

```bash
#!/bin/bash
set -e

CLUSTER_NAME="fast-sandbox"

echo "=== Step 1: 创建 KIND 集群 ==="
kind create cluster --name "$CLUSTER_NAME" --image kindest/node:v1.35.0 || echo "集群已存在"

echo "=== Step 2: 构建镜像 ==="
make docker-controller
make docker-agent
make docker-janitor

echo "=== Step 3: 加载镜像到 KIND ==="
kind load docker-image fast-sandbox/controller:dev --name "$CLUSTER_NAME"
kind load docker-image fast-sandbox/agent:dev --name "$CLUSTER_NAME"
kind load docker-image fast-sandbox/janitor:dev --name "$CLUSTER_NAME"

echo "=== Step 4: 安装 CRD ==="
kubectl apply -f config/crd/
kubectl wait --for=condition=Established crd/sandboxes.sandbox.fast.io --timeout=30s
kubectl wait --for=condition=Established crd/sandboxpools.sandbox.fast.io --timeout=30s

echo "=== Step 5: 安装 RBAC 和 Controller ==="
kubectl apply -f config/rbac/base.yaml
kubectl apply -f config/manager/controller.yaml
kubectl rollout status deployment/fast-sandbox-controller --timeout=60s

echo "=== Step 6: 安装 Janitor ==="
kubectl apply -f config/janitor/janitor.yaml
kubectl rollout status daemonset/fast-sandbox-janitor --timeout=60s

echo "=== 初始化完成 ==="
kubectl get pods -A
```

---

## 使用 Make 命令

项目提供了便捷的 Make 目标：

```bash
# 一键初始化（包含集群创建、镜像构建、CRD 安装）
make init-e2e

# 仅构建镜像
make docker-controller
make docker-agent
make docker-janitor

# 加载镜像到 KIND
make kind-load-controller
make kind-load-agent
make kind-load-janitor
```

---

## 创建测试 Sandbox

### 方式一：使用 kubectl

```bash
# 创建 SandboxPool
cat <<EOF | kubectl apply -f -
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxPool
metadata:
  name: demo-pool
  namespace: default
spec:
  capacity:
    poolMin: 1
    poolMax: 2
  maxSandboxesPerPod: 5
  runtimeType: container
  agentTemplate:
    spec:
      containers:
      - name: agent
        image: fast-sandbox/agent:dev
EOF

# 等待 Agent Pod 就绪
kubectl get pods -l fast-sandbox.io/pool=demo-pool -w

# 创建 Sandbox
cat <<EOF | kubectl apply -f -
apiVersion: sandbox.fast.io/v1alpha1
kind: Sandbox
metadata:
  name: hello-sandbox
  namespace: default
spec:
  image: docker.io/library/alpine:latest
  command: ["/bin/sleep", "3600"]
  poolRef: demo-pool
EOF

# 查看 Sandbox 状态
kubectl get sandbox hello-sandbox -o yaml
kubectl get sandbox hello-sandbox -o jsonpath='{.status.phase}'
```

### 方式二：使用 fsb-ctl CLI

```bash
# 构建 CLI
make build-cli

# 创建 Sandbox
./bin/fsb-ctl run hello-sandbox \
  --image docker.io/library/alpine:latest \
  --pool demo-pool \
  --namespace default \
  /bin/sleep 3600

# 列出 Sandbox
./bin/fsb-ctl list --namespace default

# 查看日志
./bin/fsb-ctl logs hello-sandbox --namespace default

# 删除 Sandbox
./bin/fsb-ctl delete hello-sandbox --namespace default
```

---

## 清理环境

```bash
# 删除测试资源
kubectl delete sandbox --all --all-namespaces
kubectl delete sandboxpool --all --all-namespaces

# 卸载 Janitor
kubectl delete -f config/janitor/janitor.yaml

# 卸载 CRD 和 Controller
kubectl delete -f config/manager/controller.yaml
kubectl delete -f config/rbac/base.yaml
kubectl delete -f config/crd/

# 删除 KIND 集群
kind delete cluster --name fast-sandbox
```

---

## 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `CLUSTER_NAME` | `fast-sandbox` | KIND 集群名称 |
| `SKIP_BUILD` | `""` | 跳过镜像构建，直接使用已有镜像 |
| `FORCE_RECREATE_CLUSTER` | `false` | 强制重建集群 |

### 使用示例

```bash
# 强制重建集群并运行测试
export FORCE_RECREATE_CLUSTER=true
bash test/e2e/01-basic-validation/test.sh

# 跳过构建，直接使用已有镜像
export SKIP_BUILD=true
make kind-load-controller
```

---

## 故障排查

### Controller 无法启动

```bash
# 查看 Pod 状态
kubectl get pods -l control-plane=controller-manager

# 查看日志
kubectl logs -l control-plane=controller-manager

# 常见问题：镜像未加载到 KIND
docker exec fast-sandbox-control-plane crictl images | grep fast-sandbox
```

### Sandbox 一直 Pending

```bash
# 检查 Pool 是否有可用 Agent
kubectl get sandboxpool -o yaml

# 检查 Agent Pod 状态
kubectl get pods -l app=sandbox-agent

# 查看 Agent 日志
kubectl logs -l app=sandbox-agent
```

### CRD 未生效

```bash
# 等待 OpenAPI 同步
kubectl wait --for=condition=Established crd/sandboxes.sandbox.fast.io

# 验证字段存在
kubectl get crd sandboxes.sandbox.fast.io -o jsonpath='{.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties}'
```

---

## E2E 测试

```bash
# 运行所有 E2E 测试
for suite in 01-basic-validation 02-scheduling-resources 03-fault-recovery 04-cleanup-janitor 05-advanced-features; do
  bash test/e2e/$suite/test.sh
done

# 运行单个测试套件
bash test/e2e/05-advanced-features/test.sh

# 运行特定测试用例
bash test/e2e/05-advanced-features/test.sh fast-path
```
