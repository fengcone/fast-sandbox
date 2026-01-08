# Node Janitor 设计方案

## 1. 背景
在 `Fast Sandbox` 系统中，Agent Pod 负责直接管理宿主机上的 OCI 容器。当 Agent Pod 意外崩溃（如 OOMKilled）或所在节点发生故障导致 Pod 被 K8s 控制面移除时，其在宿主机上创建的 Sandbox 容器将变成“孤儿”。这些孤儿资源会造成：
1. 内存与 CPU 资源被非法占用。
2. 调度 Slot 统计不准。
3. 宿主机磁盘空间（Snapshots）和文件描述符（FIFOs）泄露。

## 2. 核心逻辑

### 2.1 强一致性标签 (Labels)
Agent 在创建容器时必须注入以下标签作为“所有权证明”：
- `fast-sandbox.io/agent-name`: 标识所属 Agent Pod 名称。
- `fast-sandbox.io/agent-uid`: 标识所属 Agent Pod 的唯一 UID。
- `fast-sandbox.io/managed`: 设为 `true`，标识由本项目管理。

### 2.2 资源回收策略
Janitor 采用 **Informer 事件监听** 与 **周期性轮询扫描** 相结合的策略。

#### A. 事件监听 (Informer)
- 监听本节点（`spec.nodeName`）的 Pod 删除事件。
- 一旦监听到 `agent-uid` 对应的 Pod 彻底从集群消失，立即将该 UID 关联的所有 Sandbox ID 放入清理队列。

#### B. 周期扫描 (Periodic Scanner)
- 默认每 120 秒扫描一次本地 Containerd 容器。
- 对比 K8s API (via PodLister)，如果容器关联的 `agent-uid` 在 K8s 中不存在，则标记为孤儿。
- **防止误删**: 仅处理创建时间超过 2 分钟的容器，避免与正在进行的调度流程冲突。

### 2.3 清理流程
1. **停止任务**: 发送 `SIGKILL` 并删除 Containerd Task。
2. **销毁容器**: 删除 Containerd 容器元数据。
3. **空间回收**: 清理对应的快照 (Snapshots)。
4. **清理遗留文件**:
   - 清理 `/run/containerd/fifo/` 目录下相关的命名管道。
   - (未来支持) 扫描并卸载 `/var/lib/kubelet/pods/<uid>` 相关的残留挂载点。

## 3. 组件架构与部署

### 3.1 独立组件声明
`Node Janitor` 是一个全新的独立二进制应用，与 `Agent` 和 `Controller` 并列。
- **Agent**: 运行在 SandboxPool 管理的 Pod 中，负责按需创建沙箱。
- **Janitor**: 以 **DaemonSet** 模式运行在宿主机节点（Node）上，拥有宿主机文件系统和 Containerd Socket 的特权访问权限，负责“扫尾”。

### 3.2 目录结构
```text
.
├── cmd/
│   └── janitor/            # 应用程序入口
│       └── main.go         # 初始化配置、启动循环
├── internal/
│   └── janitor/            # 核心业务逻辑
│       ├── controller.go   # 基于 Informer 的事件处理
│       ├── scanner.go      # 周期性扫描逻辑
│       └── cleanup.go      # 具体执行 Containerd/FIFO 清理的方法
└── test/e2e/core/manifests/
    └── janitor-deploy.yaml # DaemonSet 部署清单
```

### 3.3 部署形态
- **容器镜像**: `fast-sandbox/janitor:dev`
- **权限需求**: 
  - `privileged: true` (用于 Unmount)。
  - `hostPath` 挂载 `/run/containerd/containerd.sock`。
  - `hostPath` 挂载 `/run/containerd/fifo/`。
  - `hostPath` 挂载 `/proc`。

## 4. 实现路线
1. **Phase 1**: 更新 Agent 逻辑，注入 `agent-uid` 标签（`internal/agent/runtime`）。
2. **Phase 2**: 创建 `cmd/janitor` 入口，集成 Containerd 与 K8s Client。
3. **Phase 3**: 实现基于 Workqueue 的多线程清理逻辑。
4. **Phase 4**: 编写 DaemonSet 部署清单并进行 E2E 故障模拟测试。
