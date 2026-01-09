# Fast-Path API 详细设计方案

## 1. 背景与动机
Kubernetes 原生声明式 API (CRD) 经过 `User -> API Server -> ETCD -> Controller -> Agent` 的链路，存在显著的性能瓶颈：
- **ETCD 写入延迟**: 100ms~200ms。
- **Watch 事件分发**: 50ms~500ms。
- **序列化开销**: 多次 JSON 编解码。
Fast-Path API 旨在将启动延迟压低至 **50ms 级别**。

## 2. 核心流程 (极速路径)

1. **客户端请求**: 用户通过 gRPC 调用 Controller 的 `CreateSandbox`。
2. **内存分配 (Atomic)**: Controller 调用 `Registry.Allocate()`。此时插槽和端口在内存中被瞬间锁定。
3. **直连下发**: Controller 立即向目标 Agent 发送 `StartContainer` gRPC 请求。
4. **即时返回**: Agent 返回成功，Controller 立即给用户返回 `SandboxID` 和 `Endpoints`。
5. **异步对账 (Audit)**: Controller 启动后台协程创建 `Sandbox` CRD。

## 3. 可靠性挑战：如果异步创建 CRD 失败怎么办？

你提出的问题非常关键：**如果 Agent 跑起来了，但 CRD 没写成功且 Controller 崩溃了，系统会发生什么？**

### 3.1 为什么不先写 CR？
如果先写 CR，我们必须等待 ETCD 落盘成功。这意味着 Fast-Path 的性能上限将被 ETCD 的磁盘 I/O 锁死，无法实现 50ms 启动。

### 3.2 方案：孤儿容器的“物理级”闭环
我们不需要依靠 CRD 的存在来防止泄露，因为我们已经实现了 **1.3 Node Janitor**。

**故障场景模拟：**
1. **状态**: Agent 已启动容器，Controller 还没写 CR 就 OOM 了。
2. **后果**: 
   - 内存中的 `Registry` 丢失了这一条记录。
   - K8s 中没有对应的 `Sandbox` CR。
   - 宿主机上多出了一个“微容器”。
3. **自愈路径**:
   - **Node Janitor 介入**: Janitor 定期（或通过监听）扫描宿主机容器。
   - **对账判定**: Janitor 发现一个容器带有 `fast-sandbox.io/managed=true` 标签，但它查询 API Server 发现**没有对应的 Sandbox CR**。
   - **强制回收**: Janitor 判定其为孤儿资源，直接在宿主机执行物理销毁。

### 3.3 增强方案：内存写前日志 (Optional Write-Ahead Memory)
为了减少 Janitor 的回收压力，Controller 可以在下发给 Agent 前，先将该请求缓存在一个 **本地持久化日志（如简易的 BboltDB）** 或 **具备 HA 的分布式缓存（如 Redis）** 中。但这会增加复杂度。

**当前结论**: 优先利用已有的 **Janitor 扫描机制** 作为安全底座，允许“短时间的孤儿存在”，换取“极致的冷启动速度”。

## 4. 接口定义 (gRPC)

```proto
service FastPath {
  rpc CreateSandbox(CreateRequest) returns (CreateResponse);
  rpc DeleteSandbox(DeleteRequest) returns (Empty);
}

message CreateRequest {
  string image = 1;
  string pool_ref = 2;
  repeated int32 ports = 3;
}

message CreateResponse {
  string sandbox_id = 1;
  string agent_pod = 2;
  string endpoint = 3; // IP:Port
}
```

## 5. 实施步骤
1. **Phase 1**: 在 Controller 中集成 gRPC Server 框架。
2. **Phase 2**: 实现 `CreateSandbox` 逻辑，调用 `Registry.Allocate`。
3. **Phase 3**: 实现异步任务池（Worker Pool）负责补齐 CRD。
4. **Phase 4**: 优化 Janitor 的“无 CR 孤儿清理”逻辑，缩短扫描间隔。
