# Fast Sandbox

Fast Sandbox 是一个高性能、云原生（Kubernetes-native）的沙箱管理系统，旨在为 AI Agent、Serverless 函数和计算密集型任务提供**毫秒级的容器冷启动**与**受控自愈**能力。

通过预热 "Agent Pod" 资源池并直接集成宿主机层面的容器管理能力，Fast Sandbox 绕过了传统 Kubernetes Pod 创建的巨大开销，实现了极速的任务分发与物理隔离。

## 🚀 核心特性

*   **零拉取启动 (Zero-Pull Startup)**: 利用 **Host Containerd 集成** 技术，直接在宿主机上启动微容器，消除 K8s Pod 层级的网络与挂载开销。
*   **镜像亲和调度 (Image Affinity)**: 调度器智能识别节点镜像缓存，实现“镜像优先”分配，彻底消除镜像拉取延迟。
*   **原子插槽与端口管理**: 支持原子级的资源插槽（Slot）与端口（Port）分配，解决多沙箱共享网络栈时的端口冲突问题。
*   **受控自愈 (Self-healing)**: 引入策略驱动的自愈机制（Manual/AutoRecreate），支持用户通过 `resetRevision` 主动触发沙箱重置。
*   **崩溃无损恢复 (Crash Recovery)**: 控制器具备状态热启动能力，在崩溃重启后能瞬间从 K8s CRD 重建内存注册表，保证调度连续性。
*   **宿主机“清道夫” (Node Janitor)**: 独立 DaemonSet 自动回收因 Agent 崩溃留下的孤儿容器与残留文件。

## 🏗 系统架构

系统采用“控制面集中决策，数据面极速执行”的架构：

1.  **控制面 (Control Plane)**:
    *   **SandboxPoolController**: 维护 Agent Pod 资源池的水位。
    *   **SandboxController**: 负责原子调度、Finalizer 资源回收及生命周期状态机。
    *   **Atomic Registry**: 内存级的状态中心，支持高并发下的互斥分配与镜像权重计算。

2.  **数据面 (Data Plane - Agent)**:
    *   运行在宿主机上的特权 Pod，通过 OCI Spec 注入管理二进制。
    *   **Infrastructure Injection**: 静默注入辅助进程，实现透明的监控与管控。

3.  **治理面 (Maintenance)**:
    *   **Node Janitor**: 定期扫描宿主机，确保物理资源零泄漏。

## 🛠 快速开始

### 运行端到端测试
最直观的了解方式是运行我们工业级的隔离测试套件：

```bash
# 运行端口互斥测试
./test/e2e/port-mutual-exclusion/test.sh

# 运行状态自愈测试
./test/e2e/controlled-recovery/test.sh
```

### 手动创建一个沙箱

1.  **定义资源池**:
    ```yaml
    apiVersion: sandbox.fast.io/v1alpha1
    kind: SandboxPool
    spec:
      capacity: { poolMin: 2, poolMax: 10 }
      maxSandboxesPerPod: 5  # 每个 Agent 承载 5 个插槽
    ```

2.  **启动带端口要求的沙箱**:
    ```yaml
    apiVersion: sandbox.fast.io/v1alpha1
    kind: Sandbox
    spec:
      image: alpine:latest
      exposedPorts: [8080]
      poolRef: default-pool
      failurePolicy: AutoRecreate  # 失联后自动重调度
    ```

## 📄 许可证

[MIT](LICENSE)
