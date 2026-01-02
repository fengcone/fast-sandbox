# E2E 测试

## 概述

本项目使用 [Ginkgo](https://onsi.github.io/ginkgo/) 和 [Gomega](https://onsi.github.io/gomega/) 框架编写 E2E 测试，这是 Kubernetes 项目的标准做法。

## 目录结构

```
test/e2e/
├── e2e_suite_test.go           # 测试套件入口
├── sandboxclaim_test.go        # SandboxClaim 调度测试
└── README.md                   # 本文档
```

## 前置条件

1. **运行 KIND 集群**
   ```bash
   kind create cluster --name fast-sandbox
   ```

2. **部署 CRD**
   ```bash
   kubectl apply -f config/crd/
   ```

3. **启动 Controller**（在另一个终端）
   ```bash
   ./bin/controller
   ```

4. **Agent 镜像已加载到 KIND 集群**
   ```bash
   make docker-agent AGENT_IMAGE=fast-sandbox-agent:dev
   make kind-load-agent AGENT_IMAGE=fast-sandbox-agent:dev
   ```

## 运行测试

### 运行所有 E2E 测试

```bash
make e2e
```

或者直接使用 go test：

```bash
go test -v ./test/e2e/... -ginkgo.v
```

### 运行特定测试

使用 Ginkgo 的 focus 功能：

```bash
# 运行包含 "SandboxClaim Scheduling" 的测试
go test -v ./test/e2e/... -ginkgo.focus="SandboxClaim Scheduling"

# 运行包含特定关键字的测试
go test -v ./test/e2e/... -ginkgo.focus="should schedule"
```

### 并行运行测试

```bash
go test -v ./test/e2e/... -ginkgo.v -ginkgo.procs=4
```

### 生成详细报告

```bash
# 生成 JUnit XML 报告
go test -v ./test/e2e/... -ginkgo.v -ginkgo.junit-report=e2e-report.xml

# 生成 JSON 报告
go test -v ./test/e2e/... -ginkgo.v -ginkgo.json-report=e2e-report.json
```

## 测试用例

### SandboxClaim 调度测试

测试文件：`sandboxclaim_test.go`

#### 测试场景 1：创建 SandboxPool
- 创建 SandboxPool CR
- 验证 Agent Pods 被成功创建
- 验证至少有 2 个 Pod 达到 Ready 状态
- 验证 SandboxPool 状态正确更新

#### 测试场景 2：带 poolRef 的 SandboxClaim 调度
- 先创建 SandboxPool 并等待 Agent Pods 就绪
- 创建带 poolRef 的 SandboxClaim
- 验证 SandboxClaim 被调度到 Scheduling 状态
- 验证分配的 Agent Pod 属于指定的 Pool

## 调试测试

### 查看详细输出

```bash
go test -v ./test/e2e/... -ginkgo.v -ginkgo.vv
```

### 在失败时暂停

```bash
go test -v ./test/e2e/... -ginkgo.v -ginkgo.fail-fast
```

### 使用 Delve 调试

```bash
dlv test ./test/e2e -- -ginkgo.v
```

## 与旧的 Shell 测试对比

| 特性 | Shell 测试 | Ginkgo 测试 |
|------|-----------|-------------|
| 语言 | Bash | Go |
| 类型安全 | ❌ | ✅ |
| IDE 支持 | ❌ | ✅ |
| 断言丰富性 | 基础 | 丰富（Gomega）|
| 并行执行 | 难 | 易 |
| 报告生成 | 基础 | JUnit/JSON |
| 可维护性 | 低 | 高 |
| 调试体验 | 困难 | 简单 |

## 最佳实践

1. **使用描述性的测试名称**
   ```go
   It("Should schedule to an Agent Pod from the specified pool", func() {
       // ...
   })
   ```

2. **使用 By() 分步骤描述**
   ```go
   By("Creating a SandboxPool")
   By("Waiting for Agent Pods to be ready")
   ```

3. **使用 Eventually() 处理异步操作**
   ```go
   Eventually(func() int {
       // 获取 ready pod 数量
   }, timeout, interval).Should(BeNumerically(">=", 2))
   ```

4. **清理测试资源**
   ```go
   AfterEach(func() {
       k8sClient.Delete(ctx, sandboxClaim)
       k8sClient.Delete(ctx, sandboxPool)
   })
   ```

## 常见问题

### 1. 测试超时

**问题**：测试一直等待但超时失败

**解决**：
- 检查 Controller 是否在运行
- 检查 Agent 镜像是否正确加载
- 增加超时时间：
  ```go
  const timeout = time.Second * 120  // 增加到 120 秒
  ```

### 2. Pod 创建失败

**问题**：Agent Pod 一直处于 Pending 状态

**解决**：
- 检查镜像是否存在：`docker images | grep fast-sandbox-agent`
- 检查镜像是否加载到 KIND：`docker exec fast-sandbox-control-plane crictl images | grep fast-sandbox`
- 查看 Pod 事件：`kubectl describe pod <pod-name>`

### 3. CRD 未找到

**问题**：测试报错 `no matches for kind "SandboxPool"`

**解决**：
```bash
kubectl apply -f config/crd/
```

## 扩展测试

添加新的测试用例：

1. 在 `test/e2e/` 目录创建新文件，如 `sandboxpool_test.go`
2. 使用相同的测试框架：
   ```go
   package e2e
   
   import (
       . "github.com/onsi/ginkgo/v2"
       . "github.com/onsi/gomega"
   )
   
   var _ = Describe("New Test Suite", func() {
       It("Should do something", func() {
           Expect(true).To(BeTrue())
       })
   })
   ```

## 参考资料

- [Ginkgo 官方文档](https://onsi.github.io/ginkgo/)
- [Gomega 官方文档](https://onsi.github.io/gomega/)
- [Kubernetes controller-runtime 测试](https://book.kubebuilder.io/reference/testing.html)
