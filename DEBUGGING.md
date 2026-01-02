# Fast-Sandbox 测试集群调试指南

本文档介绍如何在 KIND 测试集群中调试 fast-sandbox 项目。

## 前提条件

- ✅ 已安装 Docker
- ✅ 已安装 kubectl
- ✅ 已安装 kind
- ✅ 已安装 Go 1.22+

## 快速开始

### 1. 创建 KIND 测试集群（如果还没有）

```bash
kind create cluster --name fast-sandbox --image kindest/node:v1.27.3
```

查看集群状态：
```bash
kubectl cluster-info --context kind-fast-sandbox
```

### 2. 部署 CRD 和 RBAC

```bash
# 部署 SandboxClaim CRD
kubectl apply -f config/crd/sandboxclaim.yaml

# 部署 RBAC 配置
kubectl apply -f config/rbac/rbac.yaml

# 验证 CRD 创建成功
kubectl get crd sandboxclaims.sandbox.fast.io
```

### 3. 编译 Controller 和 Agent

```bash
# 编译 Controller
go build -o bin/controller cmd/controller/main.go

# 编译 Agent
go build -o bin/agent cmd/agent/main.go
```

### 4. 启动 Controller（本地运行）

在终端 1 中启动 Controller：

```bash
./bin/controller
```

你应该看到类似输出：
```
Starting agent HTTP server on :9090
2025-12-31T09:11:48+08:00       INFO    setup   starting manager
2025-12-31T09:11:48+08:00       INFO    Starting EventSource    {"controller": "sandboxclaim", ...}
2025-12-31T09:11:48+08:00       INFO    Starting Controller     {"controller": "sandboxclaim", ...}
2025-12-31T09:11:48+08:00       INFO    Starting workers        {"controller": "sandboxclaim", ...}
```

**Controller 的功能：**
- 监听 `:9090` 端口接收 Agent 注册和心跳
- Watch SandboxClaim CRD 资源
- 调度 SandboxClaim 到合适的 Agent
- 通过 HTTP 调用 Agent 创建 Sandbox

### 5. 启动 Agent（本地运行）

在终端 2 中启动 Agent：

```bash
# 设置环境变量
export CONTROLLER_URL="http://localhost:9090"
export AGENT_ID="agent-local-test"
export POD_NAME="test-agent-pod"
export POD_IP="127.0.0.1"
export NODE_NAME="local-node"
export NAMESPACE="default"
export AGENT_PORT=":8081"

# 启动 Agent
./bin/agent
```

你应该看到类似输出：
```
2025-12-31T09:12:00 starting sandbox agent
2025-12-31T09:12:00 Registering agent agent-local-test with controller at http://localhost:9090
2025-12-31T09:12:00 Registration successful: Agent registered successfully
2025-12-31T09:12:00 Starting agent HTTP server on :8081
2025-12-31T09:12:00 Agent started successfully, waiting...
2025-12-31T09:12:10 Heartbeat sent successfully
```

**Agent 的功能：**
- 向 Controller 注册（报告节点信息、镜像列表、容量）
- 监听 `:8081` 端口接收 Controller 的创建 Sandbox 请求
- 每 10 秒发送一次心跳

### 6. 创建测试 SandboxClaim

在终端 3 中创建测试资源：

```bash
# 创建一个 SandboxClaim
kubectl apply -f config/samples/sandboxclaim_sample.yaml
```

### 7. 查看调度结果

```bash
# 查看 SandboxClaim 列表（带自定义列）
kubectl get sandboxclaim

# 查看详细信息
kubectl get sandboxclaim test-sandbox -o yaml

# 查看 Controller 日志（终端 1）
# 你应该看到调度和创建 sandbox 的日志

# 查看 Agent 日志（终端 2）
# 你应该看到接收到创建请求的日志
```

### 8. 验证完整流程

如果一切正常，你会看到：

**Controller 日志：**
```
INFO    No available agent, requeuing    {"claim": "test-sandbox"}
INFO    Creating sandbox on agent         {"agentIP": "127.0.0.1", "claim": "test-sandbox"}
INFO    Sandbox created successfully      {"claim": "test-sandbox", "sandboxID": "sandbox-fc2d4e35"}
```

**Agent 日志：**
```
Creating sandbox for claim test-sandbox, image: nginx:latest
Heartbeat sent successfully
```

**SandboxClaim Status：**
```bash
kubectl get sandboxclaim test-sandbox -o jsonpath='{.status}' | jq
```

输出示例：
```json
{
  "phase": "Running",
  "assignedAgentPod": "test-agent-pod",
  "nodeName": "local-node",
  "sandboxID": "sandbox-fc2d4e35",
  "address": "127.0.0.1:8080"
}
```

## 测试场景

### 场景 1: 创建多个 Sandbox

```bash
# 复制示例文件并修改名称
cat config/samples/sandboxclaim_sample.yaml | sed 's/test-sandbox/test-sandbox-2/' | kubectl apply -f -
cat config/samples/sandboxclaim_sample.yaml | sed 's/test-sandbox/test-sandbox-3/' | kubectl apply -f -

# 查看所有 SandboxClaim
kubectl get sandboxclaim
```

### 场景 2: 测试镜像亲和

创建使用不同镜像的 SandboxClaim：

```bash
cat <<EOF | kubectl apply -f -
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxClaim
metadata:
  name: test-redis-sandbox
  namespace: default
spec:
  image: "redis:latest"
  cpu: "500m"
  memory: "1Gi"
  port: 6379
EOF
```

Agent 上报的镜像列表包含 `redis:latest`，所以这个 Sandbox 会被优先调度。

### 场景 3: 测试无可用 Agent

停止 Agent（Ctrl+C），然后创建新的 SandboxClaim：

```bash
cat <<EOF | kubectl apply -f -
apiVersion: sandbox.fast.io/v1alpha1
kind: SandboxClaim
metadata:
  name: test-no-agent
  namespace: default
spec:
  image: "nginx:latest"
  cpu: "500m"
  memory: "1Gi"
EOF
```

Controller 会每 5 秒重试一次，日志显示：
```
INFO    No available agent, requeuing    {"claim": "test-no-agent"}
```

重新启动 Agent 后，会自动调度成功。

### 场景 4: 删除 SandboxClaim

```bash
# 删除 Sandbox
kubectl delete sandboxclaim test-sandbox

# 查看状态
kubectl get sandboxclaim
```

**注意：** 当前版本还未实现 Finalizer 和清理逻辑，删除 SandboxClaim 不会自动清理 Agent 上的容器。

## 调试技巧

### 1. 查看 Controller 详细日志

Controller 使用 controller-runtime 的日志，可以通过环境变量控制日志级别：

```bash
# 开发模式（详细日志）
./bin/controller --zap-devel=true

# 自定义日志级别
./bin/controller --zap-log-level=debug
```

### 2. 查看 Agent 注册信息

Controller 内存中维护了 Agent 注册表，可以通过日志查看：

在 Controller 日志中搜索：
```
"Agent registered successfully"
```

### 3. 测试 HTTP API

你可以直接使用 curl 测试 HTTP 接口：

**测试 Agent 注册：**
```bash
curl -X POST http://localhost:9090/api/v1/agent/register \
  -H "Content-Type: application/json" \
  -d '{
    "agentId": "test-agent-manual",
    "namespace": "default",
    "podName": "manual-agent",
    "podIp": "192.168.1.100",
    "nodeName": "test-node",
    "capacity": 5,
    "images": ["nginx:latest", "redis:latest"]
  }'
```

**测试 Agent 心跳：**
```bash
curl -X POST http://localhost:9090/api/v1/agent/heartbeat \
  -H "Content-Type: application/json" \
  -d '{
    "agentId": "agent-local-test",
    "runningSandboxCount": 2,
    "timestamp": 1735632000
  }'
```

**测试创建 Sandbox（调用 Agent）：**
```bash
curl -X POST http://localhost:8081/api/v1/sandbox/create \
  -H "Content-Type: application/json" \
  -d '{
    "claimUid": "test-uid-123",
    "claimName": "manual-test",
    "image": "nginx:latest",
    "cpu": "500m",
    "memory": "1Gi",
    "port": 8080
  }'
```

### 4. 清理测试数据

```bash
# 删除所有 SandboxClaim
kubectl delete sandboxclaim --all

# 删除 CRD（会删除所有 SandboxClaim）
kubectl delete crd sandboxclaims.sandbox.fast.io

# 删除 KIND 集群
kind delete cluster --name fast-sandbox
```

## 当前限制与 TODO

当前版本是基础实现，有以下限制：

### ✅ 已实现
- Controller 与 Agent 的 HTTP 通信
- Agent 注册与心跳机制
- 基于镜像亲和的调度算法
- SandboxClaim 状态管理（Pending → Scheduling → Running）
- 内存版 AgentRegistry

### 🔨 待实现（Mock 阶段）
- **Agent 返回 Mock 响应**：当前 Agent 创建 Sandbox 只是返回 mock 数据，并没有真正创建容器
- **无真实容器**：没有集成 containerd，无法创建真实的 sandbox 容器
- **无 TTL 清理**：SandboxClaim 到期不会自动清理
- **无 Finalizer**：删除 SandboxClaim 不会清理 Agent 上的资源
- **无动态扩缩容**：不会根据负载自动创建/删除 Agent Pod

### 🚀 下一步开发
1. **集成 containerd**：Agent 侧真正创建容器（最高优先级）
2. **实现 TTL 和清理逻辑**
3. **添加 Finalizer**
4. **完善错误处理**
5. **支持 Agent Pod 部署到集群**

## 常见问题

### Q1: Controller 启动报错 "unable to start manager"

**原因：** 无法连接到 K8s 集群

**解决：**
```bash
# 确保 kubeconfig 正确
export KUBECONFIG=~/.kube/config
kubectl cluster-info

# 或者指定 context
kubectl config use-context kind-fast-sandbox
```

### Q2: Agent 注册失败

**原因：** Controller 未启动或端口不对

**解决：**
```bash
# 检查 Controller 是否在运行
lsof -i :9090

# 确保 CONTROLLER_URL 正确
export CONTROLLER_URL="http://localhost:9090"
```

### Q3: SandboxClaim 一直处于 Pending

**原因：** 没有可用的 Agent

**解决：**
```bash
# 检查 Agent 是否注册成功（查看 Agent 日志）
# 检查 Controller 日志中是否有 "No available agent" 信息

# 手动重启 Agent
./bin/agent
```

### Q4: Agent 心跳失败 "Agent not found" 或 404 错误

**原因：** Controller 重启后内存中的 AgentRegistry 被清空，但 Agent 没有重新注册

**现象：**
```
Heartbeat failed: heartbeat failed with status: 404
```

**解决：**

方法 1（推荐）：Agent 已实现自动重新注册，心跳失败时会自动尝试重新注册
```
Heartbeat failed: heartbeat failed with status: 404
Attempting to re-register agent...
Re-registration successful: Agent registered successfully
```

方法 2：手动重启 Agent
```bash
# Ctrl+C 停止当前 Agent
# 重新启动
export CONTROLLER_URL="http://localhost:9090"
./bin/agent
```

方法 3：避免 Controller 重启（开发时使用 `--zap-devel` 但不要频繁重启）

### Q5: 如何在集群内部署 Agent Pod？

当前版本 Agent 在本地运行，要在集群内部署需要：

1. 创建 Agent 的容器镜像
2. 编写 Deployment/DaemonSet YAML
3. 配置 hostPath 挂载 containerd socket
4. 配置必要的权限（特权容器或 capabilities）

这部分功能在下一阶段实现。

## 架构图

```
┌─────────────────────────────────────────────────────────────┐
│  测试集群（KIND）                                              │
│                                                                │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  K8s API Server                                       │   │
│  │  - SandboxClaim CRD                                   │   │
│  │  - RBAC 配置                                           │   │
│  └──────────────────────────────────────────────────────┘   │
│                            ▲                                  │
└────────────────────────────┼──────────────────────────────────┘
                             │ Watch SandboxClaim
                             │
┌────────────────────────────┼──────────────────────────────────┐
│  本地运行                   │                                  │
│                            │                                  │
│  ┌─────────────────────────▼────────────────────────┐        │
│  │  Controller (:9090)                               │        │
│  │  - Watch SandboxClaim                             │        │
│  │  - AgentRegistry（内存）                          │        │
│  │  - Scheduler（镜像亲和）                          │        │
│  │  - HTTP Server（接收 Agent 注册/心跳）             │        │
│  └───────────────────┬───────────────┬───────────────┘        │
│                      │               │                        │
│                      │ HTTP          │ HTTP                   │
│                      │ Register/     │ CreateSandbox          │
│                      │ Heartbeat     │                        │
│                      │               │                        │
│  ┌───────────────────▼───────────────▼───────────────┐        │
│  │  Agent (:8081)                                     │        │
│  │  - HTTP Client（向 Controller 注册）                │        │
│  │  - HTTP Server（接收创建 Sandbox 请求）             │        │
│  │  - SandboxManager（Mock 实现）                      │        │
│  └────────────────────────────────────────────────────┘        │
│                                                                │
└────────────────────────────────────────────────────────────────┘
```

## 贡献与反馈

如有问题或建议，请提 Issue 或 PR。
