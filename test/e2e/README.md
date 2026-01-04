# 端到端 (E2E) 测试

本目录包含 Fast Sandbox 的端到端测试套件。测试旨在验证系统的整个生命周期，从构建容器镜像到在 Kubernetes 集群上调度和运行 Sandbox。

## 环境准备

在运行测试之前，请确保已安装以下工具：

*   **Docker**: 正常运行且可访问。
*   **Kind**: Kubernetes in Docker 工具 (推荐 v0.20.0+)。
*   **Kubectl**: Kubernetes 命令行工具。
*   **Make**: 构建工具。
*   **Go**: Go 语言环境 (推荐 1.22+)。

## 测试脚本

### 1. `run_full_test.sh` (推荐)

这是主要的集成测试脚本。它验证了完整的系统闭环：SandboxPool Controller -> Agent 预热 -> Sandbox 调度 -> Agent 执行 -> 状态回馈。

**执行流程：**
1.  **资源清理**: 强制删除旧的 Deployment、Pod 和自定义资源 (CR)。
2.  **Containerd 清理**: 清理 Kind 节点 (`fast-sandbox-control-plane`) 中的残留容器和任务，确保测试环境纯净。
3.  **镜像构建**: 编译 Agent 和 Controller 二进制文件，并构建 Docker 镜像 (`fast-sandbox/agent:dev`, `fast-sandbox/controller:dev`)。
4.  **集群准备**: 创建名为 `fast-sandbox` 的 Kind 集群（如果不存在）并加载镜像。
5.  **部署控制面**: 应用 RBAC 权限、CRD 定义和 Controller Deployment。
6.  **创建资源池**: 应用 `SandboxPool` CR (`default-pool`)。
7.  **验证预热**: 等待 Controller 自动创建 Agent Pod。
8.  **创建 Sandbox**: 应用 `Sandbox` CR (`test-sandbox`)。
9.  **验证调度**: 检查 Sandbox 是否已正确分配给 Agent Pod。
10. **验证执行**: 持续轮询 Sandbox 状态，直到其达到 `running` 阶段。

**如何运行：**
```bash
# 在项目根目录下执行
./test/e2e/run_full_test.sh
```

### 2. `run_agent_test.sh` (单机/基础)

该脚本仅关注 **Agent** 组件。它手动部署一个 Agent Pod（跳过 Pool Controller）并直接向其发送 HTTP 请求。适用于在不受 Controller 干扰的情况下调试 Agent 的运行时逻辑。

**如何运行：**
```bash
# 在项目根目录下执行
./test/e2e/run_agent_test.sh
```

## 故障排查

如果测试失败，脚本将输出 Controller 和 Agent Pod 的日志。

**常见问题：**
*   **"Sandbox failed to reach running state" (无法达到运行状态)**:
    *   检查 Agent 日志：是否收到了请求？(`Creating sandbox...`)
    *   检查 Controller 日志：是否执行了同步？(`Syncing agent...`)
    *   检查 `poolRef` 是否与 Agent 的 Pool 标签匹配。
*   **"bind: address already in use" (端口冲突)**:
    *   Agent 使用 `hostNetwork: true` 并绑定 `8081` 端口。请确保没有遗留的 Agent Pod 在运行。
*   **Containerd 错误**:
    *   如果你看到 `failed to mount ... overlay ... no such file or directory`，通常意味着 Agent Pod 缺少必要的 HostPath 挂载 (`/var/lib/containerd`, `/run/containerd`, `/tmp`)。

## 手动调试

由于系统在 Kind 上运行，你可以直接进入节点进行底层运行时调试：

```bash
# 进入 Kind 节点
docker exec -it fast-sandbox-control-plane bash

# 列出所有容器 (k8s.io 命名空间)
ctr -n k8s.io c ls

# 查看任务状态
ctr -n k8s.io task list
```