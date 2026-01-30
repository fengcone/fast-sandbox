# Fast Sandbox 架构设计

## 1. 概述

**Fast Sandbox** 是一个基于 Kubernetes 的高性能沙箱管理系统。其核心目标是提供毫秒级的容器启动速度，主要用于 serverless 函数、代码沙箱执行等对启动延迟高度敏感的场景。

系统的核心设计理念是：**Fast-Path 优先** + **资源预热 (Resource Pooling)** + **镜像缓存亲和 (Image Affinity)**。

## 2. 核心架构

系统采用 **Controller-Agent** 分离架构，建立在 Kubernetes 之上。

![ARCHITECTURE](ARCHITECTURE.png)

### 2.1 通信链路

| 链路 | 协议 | 用途 |
|------|------|------|
| **CLI → Controller** | gRPC | Fast-Path API，<50ms 延迟 |
| **Controller → Agent** | HTTP | 沙箱创建/删除请求 |
| **CLI → Agent** | HTTP (隧道) | 日志流、未来的 exec |
| **控制面** | K8s CRD | 持久化存储和最终一致性 |

## 3. 核心组件

### 3.1 Fast-Path Server (gRPC)

**位置**: `internal/controller/fastpath/server.go`

**端口**: `9090`

**服务**:
- `CreateSandbox` - 快速创建沙箱
- `DeleteSandbox` - 快速删除沙箱
- `UpdateSandbox` - 更新沙箱配置（过期时间、重启、策略）
- `ListSandboxes` - 列出命名空间内的沙箱
- `GetSandbox` - 获取沙箱详情

**一致性模式**:
- **FAST** (默认): Agent 先创建 → 异步写 CRD。延迟 <50ms
- **STRONG**: 先写 CRD (Pending) → Watch 触发 → Agent 创建。延迟 ~200ms

### 3.2 Registry (内存态)

**位置**: `internal/controller/agentpool/registry.go`

**职责**:
- 维护所有 Agent 的实时负载和镜像缓存列表
- 原子分配，使用互斥锁保证并发安全
- 镜像亲和性评分（优先选择有缓存的 Agent）

**分配算法**:
1. 按池、命名空间、容量、端口冲突过滤候选
2. 评分: `score = allocated + (无镜像 ? 1000 : 0)`
3. 选择最低分（有镜像则胜出）

**性能**: 100 Agent 时 ~1.3ms，1000 Agent 时 ~14ms

### 3.3 SandboxController

**位置**: `internal/controller/sandbox_controller.go`

**职责**:
- CRD 状态机管理
- Finalizer 资源回收
- 与 Registry 同步状态
- 故障策略处理（Manual/AutoRecreate）

**状态转换**:
```
Pending → Creating → Running → Deleting → Gone
                ↓               ↓
             Failed         Lost
```

### 3.4 SandboxPoolController

**位置**: `internal/controller/sandboxpool_controller.go`

**职责**:
- 管理 Agent Pod 生命周期（Min/Max 容量）
- 注入 Containerd 访问所需的特权配置
- 通过心跳维持 Registry 状态

### 3.5 Agent (数据面)

**位置**: `internal/agent/`

**组件**:
- **Sandbox Manager**: 生命周期管理（创建/删除/状态）
- **Containerd Runtime**: 直接集成宿主机 containerd socket
- **HTTP Server**: 端口 `5758` 上的 API 端点

**HTTP 端点**:
```
POST /api/v1/agent/create
POST /api/v1/agent/delete
GET  /api/v1/agent/status
GET  /api/v1/agent/logs?follow=true
```

**核心特性**:
- Host Containerd 集成实现零镜像拉取
- 日志持久化到宿主机文件系统供流式读取
- 优雅关闭，完整的 SIGTERM → SIGKILL 流程

### 3.6 Node Janitor

**位置**: `internal/janitor/`

**职责**:
- 扫描孤儿容器（无对应 CRD）
- Agent Pod 消失时清理
- 删除 FIFO 文件和 containerd 快照

**孤儿判定标准**:
1. Agent Pod 消失（UID 不在 pod lister 中）
2. Sandbox CRD 不存在
3. 容器与 CRD 的 UID 不匹配

**保护窗口**: 10 秒（可配置），为 Fast-Path 异步 CRD 写入留出时间

### 3.7 CLI (fsb-ctl)

**位置**: `cmd/fsb-ctl/`

**功能**:
- 交互式 YAML 编辑创建沙箱
- 自动 port-forward 隧道连接 Agent Pod
- 流式日志查看
- 配置分层: Flags > File > Interactive

## 4. 关键流程

### 4.1 创建沙箱 (Fast Mode)

```
用户                    控制器                   Agent
  │                         │                         │
  ├─ run my-sb ────────────>│                         │
  │                         │                         │
  │                         ├─ Allocate() ──────────>│
  │                         │  (Registry 选择)        │
  │                         │<────────────────────────┤
  │                         │  (Agent 已选择)         │
  │                         │                         │
  │                         ├─ HTTP POST /create ───>│
  │                         │                         │
  │                         │                         ├─ containerd.Create()
  │                         │                         │
  │                         │<────────────────────────┤
  │                         │  (ContainerID)          │
  │                         │                         │
  │<─────────────────────────┤                         │
  │  (成功, Endpoints)       │                         │
  │                         │                         │
  │                         ├─ async: 创建 CRD ──────>│ (K8s)
```

**延迟分解**:
- Registry Allocate: ~1.3ms (100 agents)
- Agent HTTP RPC: ~10-30ms
- Containerd create: <10ms (镜像已缓存)
- **总计**: <50ms

### 4.2 创建沙箱 (Strong Mode)

```
用户                    控制器              K8s                 Agent
  │                         │                    │                    │
  ├─ run my-sb ────────────>│                    │                    │
  │                         │                    │                    │
  │                         ├─ 创建 CRD ────────>│                    │
  │                         │  (Phase: Pending)   │                    │
  │                         │<────────────────────┤                    │
  │                         │                    │                    │
  │                         ├─ Allocate() ──────>│                    │
  │                         │<────────────────────┤                    │
  │                         │                    │                    │
  │                         │                    ├─ Watch 触发 ──────>│
  │                         │                    │                    │
  │                         ├─ HTTP POST /create ─────────────────────>│
  │                         │                    │                    │
  │                         │<─────────────────────────────────────────┤
  │                         │                    │                    │
  │                         ├─ 更新 CRD ────────>│                    │
  │                         │  (Phase: Running)   │                    │
  │<─────────────────────────┤                    │                    │
  │  (成功)                 │                    │                    │
```

**延迟**: ~200ms (主要消耗在 ETCD + Watch)

### 4.3 日志流 (Logs)

```
CLI                      控制器                Agent
  │                         │                      │
  ├─ logs my-sb ───────────>│                      │
  │                         │                      │
  │<─ Agent Pod IP ──────────┤                      │
  │                         │                      │
  ├─ kubectl port-forward ──────────────────────────>│
  │                         │                      │
  ├─ GET /api/v1/agent/logs?follow=true ────────────>│
  │<─ 分块日志流 ─────────────────────────────────────┤
```

## 5. 配置项

### 5.1 Controller 参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--agent-port` | `5758` | Agent HTTP 服务器端口 |
| `--metrics-bind-address` | `:9091` | Prometheus 指标端点 |
| `--health-probe-bind-address` | `:5758` | 健康检查端点 |
| `--fastpath-consistency-mode` | `fast` | 一致性模式: fast/strong |
| `--fastpath-orphan-timeout` | `10s` | Fast 模式孤儿清理超时 |

### 5.2 Agent 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `AGENT_CAPACITY` | `5` | 每个 Agent 最大沙箱数 |
| `CONTAINERD_SOCKET` | `/run/containerd/containerd.sock` | Containerd socket 路径 |

### 5.3 Sandbox CRD Spec

```yaml
spec:
  image: string              # 容器镜像
  poolRef: string            # 目标资源池名称
  exposedPorts: []int32      # 暴露的端口
  command: []string          # 入口命令
  args: []string             # 命令参数
  envs: map[string]string    # 环境变量
  workingDir: string         # 工作目录
  consistencyMode: fast|strong  # 一致性模式
  failurePolicy: manual|autoRecreate  # 故障恢复策略
  expireTimeSeconds: int64   # 可选的过期时间
```

## 6. 日志

Fast Sandbox 使用 [klog](https://github.com/kubernetes/klog)，这是 Kubernetes 生态系统的标准日志库。

### 日志级别

| 级别 | 用途 |
|------|------|
| `klog.InfoS()` | 信息性消息 |
| `klog.ErrorS()` | 错误消息（始终记录）|
| `klog.V(2).InfoS()` | 详细信息（通过 `-v=2` 启用）|
| `klog.V(4).InfoS()` | 调试信息（通过 `-v=4` 启用）|

### 启用调试日志

```bash
# Controller
./bin/controller -v=2

# Agent
./bin/agent -v=4

# CLI
fsb-ctl -v=4 list
```