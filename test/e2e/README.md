# E2E Tests

This directory contains end-to-end tests for the fast-sandbox project using Ginkgo and Gomega.

## 概述

E2E 测试完全自动化，外部依赖只需要一个 KIND 集群。测试会自动：

1. **构建并加载 Agent 镜像**到 KIND 集群
2. **构建并加载 Controller 镜像**到 KIND 集群
3. **部署 Controller 为 Deployment**（包括 RBAC、Service 等资源）
4. **从 YAML 文件读取 CR 定义**并创建测试资源
5. **执行测试用例**验证功能
6. **自动清理**所有测试资源

## 前置条件

1. **KIND 集群**：确保有一个名为 `fast-sandbox` 的 KIND 集群正在运行

   ```bash
   # 创建 KIND 集群（如果还没有）
   kind create cluster --name fast-sandbox
   ```

2. **已部署 CRD**：确保 SandboxClaim 和 SandboxPool CRD 已部署

   ```bash
   kubectl apply -f config/crd/
   ```

仅此而已！测试会自动处理所有其他事情。

## 运行测试

### 使用 Make 命令（推荐）

```bash
make e2e
```

### 直接使用 go test

```bash
go test -v ./test/e2e/... -ginkgo.v
```

## 测试流程

### 1. BeforeSuite（测试前准备）

- 构建 Agent 镜像（`make docker-agent`）
- 加载 Agent 镜像到 KIND（`make kind-load-agent`）
- 构建 Controller 镜像（`make docker-controller`）
- 加载 Controller 镜像到 KIND（`make kind-load-controller`）
- 部署 Controller 到集群（从 `fixtures/controller-deployment.yaml`）
- 等待 Controller Deployment 就绪
- 连接到 KIND 集群
- 初始化 Kubernetes 客户端

### 2. 测试用例

#### Test 1: SandboxPool 创建

- 从 `fixtures/sandboxpool.yaml` 读取 SandboxPool 定义
- 创建 SandboxPool
- 验证 Agent Pods 被成功创建
- 验证至少 2 个 Agent Pods 达到 Ready 状态
- 验证 SandboxPool Status 更新正确

#### Test 2: SandboxClaim 调度

- 从 `fixtures/sandboxpool.yaml` 创建 SandboxPool
- 等待 Agent Pods 就绪
- 从 `fixtures/sandboxclaim.yaml` 读取 SandboxClaim 定义
- 创建 SandboxClaim
- 验证 SandboxClaim 被成功调度
- 验证分配的 Agent Pod 属于指定的 Pool

### 3. AfterSuite（测试后清理）

- 删除 Controller Deployment 和相关资源
- 清理测试环境

## 测试文件结构

```
test/e2e/
├── README.md                    # 本文档
├── e2e_suite_test.go           # 测试套件入口和辅助函数
├── sandboxclaim_test.go        # SandboxClaim 相关测试用例
└── fixtures/                   # YAML 文件
    ├── controller-deployment.yaml  # Controller 部署配置
    ├── sandboxpool.yaml            # SandboxPool CR 定义
    └── sandboxclaim.yaml           # SandboxClaim CR 定义
```

## YAML Fixtures

### controller-deployment.yaml

包含 Controller 部署所需的所有资源：
- ServiceAccount
- ClusterRole 和 ClusterRoleBinding（RBAC）
- Service（HTTP API 和 Metrics）
- Deployment（Controller 容器）

### sandboxpool.yaml

定义测试用的 SandboxPool：
- 容量配置（poolMin: 2, poolMax: 5）
- Agent Pod 模板（使用 `fast-sandbox-agent:dev` 镜像）

### sandboxclaim.yaml

定义测试用的 SandboxClaim：
- 指定容器镜像（nginx:latest）
- 资源配置（CPU: 100m, Memory: 128Mi）
- Pool 引用（test-sandbox-pool）

## 辅助函数

### buildAndLoadAgentImage()

自动构建 Agent 镜像并加载到 KIND 集群：
```go
err := buildAndLoadAgentImage()
```

### buildAndLoadControllerImage()

自动构建 Controller 镜像并加载到 KIND 集群：
```go
err := buildAndLoadControllerImage()
```

### deployControllerToCluster()

从 YAML 文件部署 Controller 到集群：
```go
err := deployControllerToCluster()
```

### LoadYAMLToObject(filename, obj)

从 fixtures 目录加载 YAML 文件到 Kubernetes 对象：
```go
sandboxPool := &sandboxv1alpha1.SandboxPool{}
err := LoadYAMLToObject("sandboxpool.yaml", sandboxPool)
```

## 故障排查

### 镜像构建失败

```bash
# 手动构建镜像测试
make docker-agent AGENT_IMAGE=fast-sandbox-agent:dev
make docker-controller CONTROLLER_IMAGE=fast-sandbox/controller:dev
```

### 镜像加载失败

```bash
# 检查 KIND 集群是否运行
kind get clusters

# 手动加载镜像
make kind-load-agent AGENT_IMAGE=fast-sandbox-agent:dev
make kind-load-controller CONTROLLER_IMAGE=fast-sandbox/controller:dev
```

### Controller 部署失败

```bash
# 检查 Controller Deployment 状态
kubectl get deployment fast-sandbox-controller -n default
kubectl describe deployment fast-sandbox-controller -n default
kubectl logs -l app=fast-sandbox-controller -n default
```

### Agent Pods 未就绪

```bash
# 检查 Agent Pods 状态
kubectl get pods -l sandbox.fast.io/pool=test-sandbox-pool
kubectl describe pod <pod-name>
kubectl logs <pod-name>
```

## 最佳实践

1. **使用 YAML 文件定义资源**：便于维护和复用
2. **自动化构建和部署**：减少手动操作，提高可靠性
3. **使用 Eventually() 等待异步操作**：确保测试稳定性
4. **每个测试用例前清理旧资源**：避免资源冲突
5. **测试后自动清理**：保持集群整洁

## 扩展测试

### 添加新的测试用例

在 `sandboxclaim_test.go` 中添加新的 `It()` 块：

```go
It("Should handle new scenario", func() {
    By("Step 1")
    // 测试代码
    
    By("Step 2")
    // 验证代码
})
```

### 添加新的 CR 定义

在 `fixtures/` 目录下添加新的 YAML 文件，然后使用 `LoadYAMLToObject()` 加载。

## 性能考虑

E2E 测试需要构建镜像和部署容器，因此运行时间较长（约 2-5 分钟）。这是正常的，因为我们在测试真实的 Kubernetes 环境。

## 与旧的 Shell 测试对比

| 特性 | Ginkgo E2E | Shell 测试 |
|------|-----------|----------|
| 语言 | Go | Shell |
| 类型安全 | ✅ | ❌ |
| IDE 支持 | ✅ | ❌ |
| 自动化程度 | 完全自动 | 需要手动准备 |
| 可维护性 | 高 | 低 |
| 调试体验 | 好 | 一般 |
| BDD 风格 | ✅ | ❌ |

旧的 Shell 测试仍然保留在 `make e2e-shell` 命令中，用于参考。
