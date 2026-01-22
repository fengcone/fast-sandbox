# Fast Sandbox 架构设计文档

## 1. 概述 (Overview)

**Fast Sandbox** 是一个基于 Kubernetes 的高性能 Sandbox 管理系统。其核心目标是提供毫秒级的容器启动速度，主要用于 serverless 函数、代码沙箱执行等对启动延迟高度敏感的场景。

系统的核心设计理念是：**Fast-Path 优先** + **资源预热 (Resource Pooling)** + **镜像缓存亲和 (Image Affinity)**。

## 2. 核心架构 (Core Architecture)

系统采用 **Controller-Agent** 分离架构，建立在 Kubernetes 之上。

![架构图](ARCHITECTURE.png)

### 2.1 通信链路 (Communication Channels)
*   **Fast-Path (gRPC)**: 
    *   **CLI -> Controller**: 用户指令直达控制器，支持毫秒级响应。
    *   **Controller -> Agent**: 调度指令下发，不再依赖缓慢的 K8s Watch 机制。
*   **Data Plane (HTTP/Streaming)**:
    *   **CLI -> Agent**: 日志流 (`logs`) 和未来的 `exec` 流量通过自动建立的隧道直连 Agent。
*   **Control Plane (K8s CRD)**:
    *   作为持久化存储和状态最终一致性的保障。

## 3. 核心组件设计

### 3.1. SandboxPool (资源缓冲层)
*   **职责**: 维护一组 "热" 资源（Agent Pods）。
*   **机制**: 
    *   Controller 根据 `SandboxPool` CR 定义的容量（Min/Max），向 K8s 申请创建 Agent Pods。
    *   **Pod 构建**: 自动注入 Runtime 所需的特权配置（HostPath 挂载 `/run/containerd/containerd.sock`）。

### 3.2. Agent (数据面)
运行在 Agent Pod 内部的守护进程，是 Sandbox 的实际管理者。

*   **Runtime 架构**: 
    *   **Host Containerd Integration**: Agent 通过 gRPC 直接调用宿主机的 containerd 接口。
    *   **Log Persistence**: 使用 `cio.LogFile` 将容器标准输出重定向到宿主机文件系统，供后续流式读取。
    *   **优势**: 零镜像拉取，高性能。

*   **Infrastructure Injection (基础设施注入)**:
    *   **目的**: 向沙箱容器中透明注入辅助二进制文件（如 `fs-helper`, debug tools），而无需修改用户镜像。
    *   **机制**:
        1.  **分发**: Agent Pod 启动时，通过 `InitContainer` 将工具集下载到宿主机的共享目录（如 `/var/lib/fast-sandbox/tools`）。
        2.  **挂载**: Agent 在创建 Sandbox 容器时，自动将该目录挂载为只读 Volume。
        3.  **执行**: 可通过 `Entrypoint` 劫持或直接调用注入的工具。

*   **服务接口**:
    *   **gRPC**: 接收创建/删除指令。
    *   **HTTP**: 提供 `/logs` 接口，支持 Chunked Transfer Encoding 实现日志流。

### 3.3. Controller (控制面)
负责全局状态的协调和调度。

*   **Fast-Path 调度器**:
    1.  **Registry (内存态)**: 维护所有 Agent 的实时负载和镜像缓存列表。
    2.  **调度算法**: 优先选择有镜像缓存且负载最低的 Agent。
    3.  **双模一致性 (Dual-Mode Consistency)**:
        *   **Fast Mode**: Agent 先创建容器 -> 异步写 CRD。延迟 < 50ms。
        *   **Strong Mode**: 写 CRD (Pending) -> 等待 Watch -> Agent 创建。延迟 ~200ms。

### 3.4. CLI (fsb-ctl)
开发者与系统的主要交互入口。
*   **Config Management**: 支持多层级配置 (Flags > File > Interactive)。
*   **Tunneling**: 自动建立 `kubectl port-forward` 隧道以访问私有网络中的 Agent。

## 4. 关键流程 (Workflows)

### 4.1 创建流 (Fast Mode)
1.  用户执行 `fsb-ctl run my-sb`。
2.  请求到达 Controller gRPC Server。
3.  Registry 选中 Agent A (Score: Image Hit)。
4.  Controller 调用 Agent A 的 `CreateSandbox` gRPC。
5.  Agent A 调用 Containerd 启动容器 (耗时 < 10ms)。
6.  Controller 返回成功给 CLI。
7.  Controller 异步创建 K8s CRD 用于记录。

### 4.2 日志流 (Logs)
1.  用户执行 `fsb-ctl logs my-sb -f`。
2.  CLI 查询 Controller 获取 Sandbox 所在的 Agent Pod IP。
3.  CLI 自动建立本地到 Agent Pod 的端口转发。
4.  CLI 发起 HTTP GET `/api/v1/agent/logs?follow=true`。
5.  Agent 读取宿主机日志文件并流式返回。

## 5. 开发计划

*   [x] **Phase 1**: 核心 Runtime (Containerd) 与 gRPC 框架。
*   [x] **Phase 2**: Fast-Path API 与 Registry 调度。
*   [x] **Phase 3**: CLI (`fsb-ctl`) 与交互式体验。
*   [x] **Phase 4**: 日志流式传输与自动隧道。
*   [ ] **Phase 5**: 容器热迁移 (Checkpoint/Restore)， 初步想法是利用 Janitor P2P 分发。
*   [ ] **Phase 6**: Web 控制台，以及流量Proxy 组件，打通生产环境和开发环境。
*   [ ] **Phase 7**: gVisor 容器支持，解决生产环境安全问题。
*   [ ] **Phase 8**: CLI exec bash 支持，Python SDK 类似 Modal能力。
*   [ ] **Phase 9**: GPU 容器支持。
