# Fast Sandbox

Fast Sandbox 是一个高性能、云原生（Kubernetes-native）的沙箱管理系统，旨在提供**毫秒级的容器冷启动**能力。

通过预热 "Agent Pod" 资源池并直接集成宿主机层面的容器管理能力，Fast Sandbox 绕过了传统 Kubernetes Pod 创建的巨大开销，非常适合短生命周期的无状态任务（如 Serverless 函数、CI/CD Job、代码沙箱等）。

## 🚀 核心特性

*   **零拉取启动 (Zero-Pull Startup)**: 利用 **Host Containerd 集成** 技术，Agent 直接在宿主机上启动容器，利用 K8s 节点已有的镜像缓存，彻底消除镜像拉取延迟。
*   **镜像亲和调度**: 自研调度器能智能识别节点镜像缓存，优先将任务调度到拥有所需镜像的 Agent 上。
*   **资源预热池**: 通过 `SandboxPool` CRD 定义热备 Agent 池，确保资源随时可用。
*   **直接 Pod 管控**: `SandboxPoolController` 直接管理 Pod 生命周期（不依赖 Deployment），实现极其精准的扩缩容和定向调度。

## 🏗 系统架构

系统由两个核心组件组成：

1.  **控制面 (Control Plane)**:
    *   **SandboxPoolController**: 根据资源池定义自动预热 Agent Pod。
    *   **SandboxController**: 负责 Sandbox 的调度决策、状态同步和生命周期协调。
    *   **Registry (内存注册表)**: 实时维护所有 Agent 和 Sandbox 的状态，支持高频调度。

2.  **数据面 (Data Plane - Agent)**:
    *   作为特权 Pod 运行在 K8s 节点上。
    *   连接宿主机的 `containerd.sock`。
    *   提供 HTTP API 接收调度指令。
    *   在宿主机上直接管理 "微容器" (Sandboxes) 的生命周期。

![架构图](ARCHITECTURE.png)

## 🛠 快速开始

### 前置条件
*   Kubernetes 集群 (推荐使用 Kind 进行本地测试)
*   Go 1.22+
*   Docker

### 运行全链路测试
最直观的了解方式是运行端到端测试套件：

```bash
# 在本地 Kind 集群上运行完整生命周期测试
./test/e2e/run_full_test.sh
```

### 手动部署步骤

1.  **构建并加载镜像**
    ```bash
    make docker-agent
    make docker-controller
    kind load docker-image fast-sandbox/agent:dev --name fast-sandbox
    kind load docker-image fast-sandbox/controller:dev --name fast-sandbox
    ```

2.  **部署控制器**
    ```bash
    kubectl apply -f test/e2e/manifests/controller-deploy.yaml
    ```

3.  **创建资源池 (Pool)**
    ```yaml
    apiVersion: sandbox.fast.io/v1alpha1
    kind: SandboxPool
    metadata:
      name: default-pool
    spec:
      capacity:
        poolMin: 1
        poolMax: 5
      agentTemplate:
        spec:
          containers:
          - name: agent
            image: fast-sandbox/agent:dev
    ```
    ```bash
    kubectl apply -f test/e2e/manifests/pool.yaml
    ```

4.  **创建沙箱 (Sandbox)**
    ```yaml
    apiVersion: sandbox.fast.io/v1alpha1
    kind: Sandbox
    metadata:
      name: my-sandbox
    spec:
      image: docker.io/fast-sandbox/agent:dev # 使用节点已有的镜像
      command: ["/bin/sleep", "100"]
      poolRef: default-pool
    ```
    ```bash
    kubectl apply -f test/e2e/manifests/sandbox.yaml
    ```

5.  **查看状态**
    ```bash
    kubectl get sandbox my-sandbox
    # 状态最终应变为 'running'
    ```

## ⚠️ 当前局限性 (Alpha 阶段)

*   **资源隔离**: Sandbox 容器目前运行在宿主机上，尚未严格受到 Agent Pod Cgroup 的限制（计划于后续 Phase 5 实现）。
*   **网络隔离**: 目前默认使用 Host Network 以简化网络互通，存在端口冲突风险。
*   **安全性**: Agent 运行在特权模式并挂载了宿主机关键路径。

## 📄 许可证

[MIT](LICENSE)