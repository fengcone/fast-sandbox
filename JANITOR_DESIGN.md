# Node Janitor 设计方案 (完整版)

## 1. 背景与动机
在 `Fast Sandbox` 系统中，Agent Pod 负责直接管理宿主机上的 OCI 容器。由于这些容器是绕过 K8s 原生 Pod 管理的，当发生以下异常时，会产生“孤儿资源”：
1. **Agent 崩溃 (Pod-level Orphan)**: Agent Pod 意外消失（如 OOM/宿主机宕机），但其创建的微容器仍在运行。
2. **控制面失步 (CRD-level Orphan)**: Agent 已启动容器，但对应的 `Sandbox` CRD 由于网络或控制器故障未能创建或被误删（常见于 Fast-Path API 的异步场景）。

孤儿资源会导致 CPU/内存占用泄露、调度 Slot 统计失效以及宿主机磁盘空间溢出。

## 2. 核心逻辑

### 2.1 强一致性标签 (Labels)
Agent 在创建容器时必须注入以下标签，作为 Janitor 判定的唯一事实依据：
- `fast-sandbox.io/agent-uid`: 所属 Agent Pod 的唯一 UID。
- `fast-sandbox.io/sandbox-name`: 对应的 Sandbox CRD 逻辑名称。
- `fast-sandbox.io/managed`: 设为 `true`，标识此资源受 Janitor 监管。

### 2.2 资源回收策略 (双重对账)
Janitor 采用 **Informer 事件监听** 与 **深度对账扫描** 相结合的策略。

#### A. Pod 监听模式 (快速响应)
- 监听本节点（`spec.nodeName`）的 Pod 删除事件。
- 一旦监听到某个 `agent-uid` 对应的 Pod 彻底消失，立即将该 UID 关联的所有物理容器放入清理队列。

#### B. 逻辑对账模式 (最终闭环)
- **周期**: 默认每 60 秒执行一次全量物理扫描。
- **保护窗口**: 仅处理创建时间超过 60 秒的容器，为 Fast-Path 的异步写入留出时间。
- **判定准则**: 
  - 扫描所有 `managed=true` 的容器。
  - 通过 K8s Lister 检查容器对应的 `Sandbox` CRD。
  - **如果 CRD 不存在（且已过保护期），则判定为孤儿，强制物理回收。**

### 2.3 判定矩阵 (Decision Matrix)

| 物理容器状态 | Agent Pod 状态 | Sandbox CRD 状态 | Janitor 动作 | 场景说明 |
| :--- | :--- | :--- | :--- | :--- |
| Running | 存活 | 存在 | 保持现状 | 正常业务运行 |
| Running | **消失** | 存在/不存在 | **立即清理** | Agent 进程或 Pod 意外挂掉 |
| Running | 存活 | **不存在** | **过期清理** | Fast-Path 异步对账失败（逻辑丢失） |
| Running | 存活 | 存在但 UID 错配 | **立即清理** | 旧沙箱被重置后残留的物理脏数据 |

## 3. 清理流程 (Cleanup Flow)
1. **停止任务**: 发送 `SIGKILL` 并删除 Containerd Task。
2. **销毁容器**: 删除 Containerd 容器元数据。
3. **资源回收**:
   - 清理对应的快照 (Snapshots)。
   - 清理 `/run/containerd/fifo/` 目录下相关的命名管道。
   - (未来支持) 扫描并卸载宿主机上的挂载残留。

## 4. 组件架构与部署

### 4.1 独立组件形态
`Node Janitor` 是一个独立的 Go 应用，以 **DaemonSet** 模式部署在所有工作节点上。
- **权限需求**: `privileged: true`。
- **核心挂载**: 
  - `/run/containerd/containerd.sock` (控制运行时)。
  - `/run/containerd/fifo/` (清理管道)。
  - `/proc` (访问宿主机命名空间)。

### 4.2 目录结构
```text
.
├── cmd/janitor/            # 程序入口
├── internal/janitor/
│   ├── controller.go       # Pod Informer 监听逻辑
│   ├── scanner.go          # 定期 CRD 对账逻辑 (核心)
│   ├── cleanup.go          # Containerd 物理清理工具
│   └── types.go            # 配置与状态定义
```

## 5. 实施路线
1. **Phase 1**: 更新 Agent 注入 `sandbox-name` 标签。
2. **Phase 2**: 增强 `internal/janitor/scanner.go`，引入 Sandbox Lister 和对账判定算法。
3. **Phase 4**: 编写 E2E 测试用例 `node-janitor-recovery` 进行故障模拟验证。
