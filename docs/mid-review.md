# Fast-Sandbox 中期代码审查报告

**审查日期**: 2026-01-18
**审查范围**: Controller, Agent, CTL 核心代码
**版本**: v0.1.0-alpha

---

## 概览

本报告基于详细代码审查，识别出当前实现中的关键问题和改进建议。主要问题分类：

| 模块 | 严重问题 | 中等问题 | 一般问题 |
|------|----------|----------|----------|
| Controller | 3 | 3 | 1 |
| CTL (fsb-ctl) | 2 | 1 | 0 |
| Agent | 3 | 2 | 1 |
| 日志系统 | 1 | 0 | 0 |

---

## 一、Controller 问题

### 1.1 [严重] ExpireTime 逻辑不正确 ✅ FIXED

**文件**: `internal/controller/sandbox_controller.go:36-51`

**问题描述**:
- 当前逻辑直接删除整个 CRD，包括底层 Sandbox
- **预期行为**: 应该只删除底层 Sandbox，保留 CRD 用于查询历史记录

**修复方案**:
- 过期时删除底层 Sandbox（调用 Agent）
- 更新 CRD 状态为 "Expired"
- 保留 CRD 用于查询历史

**验证**: E2E 测试 `test/e2e/04-cleanup-janitor/auto-expiry.sh` 通过

**优先级**: P0
**状态**: ✅ 已完成 (2026-01-18)

---

### 1.2 [严重] Finalizer 逻辑错误忽略删除错误 ✅ FIXED

**文件**: `internal/controller/sandbox_controller.go:72-94`

**问题描述**:
- `deleteFromAgent` 的返回值被忽略
- 如果 Agent 删除失败，Registry 仍然会被释放
- 最后 CRD Finalizer 被移除，但底层 Sandbox 可能还存在

**修复方案**:
- 同步删除，检查 `deleteFromAgent` 返回值
- 删除失败时返回错误触发重试（controller-runtime 指数退避）
- 删除成功后才移除 Finalizer

**验证**:
- 单元测试 `internal/controller/sandbox_controller_test.go` 通过
- E2E 测试 `test/e2e/03-fault-recovery/finalizer-cleanup.sh` 通过

**优先级**: P0
**状态**: ✅ 已完成 (2026-01-18)

---

### 1.3 [严重] InMemoryRegistry 全局锁性能问题

**文件**: `internal/controller/agentpool/registry.go:56-283`

**问题描述**:
```go
type InMemoryRegistry struct {
    mu     sync.RWMutex  // 全局锁
    agents map[AgentID]AgentInfo
}
```

**并发分析**:

| 操作 | 频率 | 持锁时间 | 影响 |
|------|------|----------|------|
| `RegisterOrUpdate` | 每个心跳 (2s) | O(1) | 写锁阻塞所有读 |
| `GetAllAgents` | 每次 Allocate | O(N) | 阻塞所有写 |
| `Allocate` | 每次 Sandbox 创建 | O(N) | 长时间持锁遍历 |
| `Release` | 每次 Sandbox 删除 | O(1) | 写锁阻塞所有读 |

**问题**:
- 当有 100 个 Agent，每 2 秒 100 次心跳更新
- 每次更新持写锁，阻塞所有 Allocate 操作
- Allocate 遍历所有 Agent 时持读锁，阻止心跳更新

**建议优化方案**:

```go
// 细粒度锁：每个 Agent 一个锁
type InMemoryRegistry struct {
    agents map[AgentID]*agentSlot
    mu     sync.RWMutex  // 仅保护 agents map 结构
}

type agentSlot struct {
    mu      sync.RWMutex
    info    AgentInfo
}

func (r *InMemoryRegistry) RegisterOrUpdate(info AgentInfo) {
    r.mu.Lock()
    slot, exists := r.agents[info.ID]
    if !exists {
        slot = &agentSlot{
            info: info,
        }
        r.agents[info.ID] = slot
    }
    r.mu.Unlock()

    // 只锁单个 Agent
    slot.mu.Lock()
    defer slot.mu.Unlock()

    // 保留 Allocated 和 UsedPorts
    slot.info.PoolName = info.PoolName
    slot.info.PodIP = info.PodIP
    slot.info.Images = info.Images
    slot.info.SandboxStatuses = info.SandboxStatuses
    slot.info.LastHeartbeat = info.LastHeartbeat
}

func (r *InMemoryRegistry) Allocate(sb *apiv1alpha1.Sandbox) (*AgentInfo, error) {
    // 两阶段分配
    r.mu.RLock()
    candidates := make([]*agentSlot, 0, len(r.agents))
    for _, slot := range r.agents {
        candidates = append(candidates, slot)
    }
    r.mu.RUnlock()

    var bestID AgentID
    var minScore = 1000000
    var bestSlot *agentSlot

    for _, slot := range candidates {
        slot.mu.RLock()
        // 检查基础条件
        if slot.info.PoolName != sb.Spec.PoolRef {
            slot.mu.RUnlock()
            continue
        }
        // ... 端口和镜像检查 ...
        score := slot.info.Allocated
        if !hasImage {
            score += 1000
        }
        slot.mu.RUnlock()

        if score < minScore {
            minScore = score
            bestID = slot.info.ID
            bestSlot = slot
        }
    }

    if bestSlot == nil {
        return nil, fmt.Errorf("no available agent")
    }

    // 原子分配
    bestSlot.mu.Lock()
    defer bestSlot.mu.Unlock()
    bestSlot.info.Allocated++
    // ... 更新端口 ...
    return &bestSlot.info, nil
}
```

**优化效果**:
- 心跳更新只锁单个 Agent，不阻塞其他操作
- Allocate 只在最终分配时持写锁
- 吞吐量可提升 10-100 倍（取决于 Agent 数量）

**优先级**: P1

---

### 1.4 [中等] Namespace 隔离缺失 ✅ FIXED

**文件**: `internal/controller/agentpool/registry.go:128-203`

**问题描述**:
- Sandbox 和 SandboxPool 可以在不同 Namespace
- 但 Sandbox 和 Agent 必须在同一 Namespace（网络/存储限制）
- 当前 `Allocate` 没有检查 Namespace 匹配

**修复**:
- 在 `Allocate` 中添加 Namespace 强制校验
- 跨 Namespace 的 Sandbox 将无法被调度，保持 Pending 状态

**验证**:
- E2E 测试 `test/e2e/01-basic-validation/namespace-isolation.sh` 通过

**优先级**: P1
**状态**: ✅ 已完成 (2026-01-18)

---

### 1.5 [中等] Loop 调度效率低

**文件**: `internal/controller/agentcontrol/loop.go:31,83`

**问题描述**:
```go
Interval: 2 * time.Second,  // 全局 2 秒
perAgentTimeout = 2 * time.Second  // 单个 Agent 2 秒
```

**问题分析**:
- 当前是全局 Loop，顺序探测所有 Agent
- 当有 100 个 Agent 时，完整一轮需要 200 秒
- 心跳间隔仅 2 秒，但探测延迟可达 200 秒

**建议优化**:
```go
// 基于 Pool 维度的独立 Loop
type PoolLoop struct {
    PoolName string
    Agents   map[AgentID]AgentInfo
    Interval time.Duration
    ticker   *time.Ticker
}

// 每个池独立调度，心跳 10 秒足够
const perPoolHeartbeatInterval = 10 * time.Second
const perAgentTimeout = 5 * time.Second  // 增加到 5 秒

type MultiPoolLoopManager struct {
    poolLoops map[string]*PoolLoop
    mu        sync.RWMutex
}

func (m *MultiPoolLoopManager) GetOrCreatePool(poolName string) *PoolLoop {
    m.mu.Lock()
    defer m.mu.Unlock()
    if loop, exists := m.poolLoops[poolName]; exists {
        return loop
    }
    loop := &PoolLoop{
        PoolName: poolName,
        Agents:   make(map[AgentID]AgentInfo),
        Interval: perPoolHeartbeatInterval,
    }
    m.poolLoops[poolName] = loop
    go loop.Start()
    return loop
}
```

**优先级**: P2

---

### 1.6 [中等] SandboxPool Agent 版本更新无方案

**文件**: `internal/controller/sandboxpool_controller.go:116-244`

**问题描述**:
- 当前 Agent Pod 使用固定镜像 `pool.Spec.AgentTemplate.Spec.Containers[0].Image`
- 没有版本滚动更新机制
- 更新 Agent 镜像后，需要手动删除所有 Pod

**建议方案**:
```go
// 在 SandboxPool Spec 中添加
type SandboxPoolSpec struct {
    // ...
    AgentVersion string `json:"agentVersion,omitempty"`
    RollingUpdatePolicy *RollingUpdatePolicy `json:"rollingUpdatePolicy,omitempty"`
}

type RollingUpdatePolicy struct {
    MaxUnavailable int32 `json:"maxUnavailable,omitempty"`
    MaxSurge       int32 `json:"maxSurge,omitempty"`
}

// 在 Reconcile 中检查版本
if pool.Spec.AgentVersion != "" {
    for _, pod := range childPods.Items {
        currentVersion := pod.Labels["fast-sandbox.io/agent-version"]
        if currentVersion != pool.Spec.AgentVersion {
            // 触发滚动更新
            r.RollingUpdateAgentPod(ctx, &pool, &pod)
        }
    }
}
```

**优先级**: P2

---

### 1.7 [一般] FastPath Server 缺少 Namespace 校验

**文件**: `internal/controller/fastpath/server.go`

**建议**: 添加 Namespace 隔离校验，确保跨 Namespace 请求被拒绝。

**优先级**: P3

---

## 二、CTL (fsb-ctl) 问题

### 2.1 [严重] PB 接口字段缺失

**文件**: `api/proto/v1/fastpath.proto:58-67`

**问题对比**:

| 字段 | CRD (sandbox_types.go) | PB (fastpath.proto) | 状态 |
|------|------------------------|---------------------|------|
| Image | ✓ | ✓ | OK |
| Command | ✓ | ✓ | OK |
| Args | ✓ | ✓ | OK |
| Envs | ✓ | ❌ | **缺失** |
| WorkingDir | ✓ | ❌ | **缺失** |
| ExpireTime | ✓ | ❌ | **缺失** |
| ExposedPorts | ✓ | ✓ | OK |
| PoolRef | ✓ | ✓ | OK |
| FailurePolicy | ✓ | ❌ | **缺失** |
| ResetRevision | ✓ | ❌ | **缺失** |

**建议修复**:
```protobuf
message CreateRequest {
    string image = 1;
    string pool_ref = 2;
    repeated int32 exposed_ports = 3;
    repeated string command = 4;
    repeated string args = 5;
    string namespace = 6;
    ConsistencyMode consistency_mode = 7;
    string name = 8;

    // 新增字段
    repeated EnvVar envs = 9;
    string working_dir = 10;
    int64 expire_time_seconds = 11;  // Unix timestamp
    FailurePolicy failure_policy = 12;
}

message EnvVar {
    string name = 1;
    string value = 2;
}

enum FailurePolicy {
    MANUAL = 0;
    AUTO_RECREATE = 1;
}
```

**优先级**: P0

---

### 2.2 [严重] 缺少 UpdateSandbox 接口 ✅ FIXED

**文件**: `api/proto/v1/fastpath.proto:7-19`

**问题**:
- 无法通过 CLI 更新 `ExpireTime` 延长 Sandbox 生命周期
- 无法通过 CLI 触发 `ResetRevision` 重启 Sandbox

**修复**:
- 添加了 UpdateSandbox RPC 到 fastpath.proto
- 实现了 fsb-ctl update 和 reset 命令
- 支持: --expire-time, --labels, --failure-policy, --recovery-timeout

**验证**:
- E2E 测试 `test/e2e/05-advanced-features/update-reset.sh` 通过

**优先级**: P1
**状态**: ✅ 已完成 (2026-01-18)

---

## 三、Agent 问题

### 3.1 [严重] WorkingDir 未生效

**文件**: `internal/agent/runtime/containerd_runtime.go:249-327`

**问题**:
- CRD 定义了 `WorkingDir` 字段
- PB 接口未传递 `WorkingDir`
- `prepareSpecOpts` 中没有使用 `oci.WithProcessCwd`

**建议修复**:
```go
func (r *ContainerdRuntime) prepareSpecOpts(config *SandboxConfig, image containerd.Image) []oci.SpecOpts {
    specOpts := []oci.SpecOpts{
        oci.WithImageConfig(image),
        oci.WithProcessArgs(finalArgs...),
        oci.WithEnv(envMapToSlice(config.Env)),
    }

    // 添加工作目录支持
    if config.WorkingDir != "" {
        specOpts = append(specOpts, oci.WithProcessCwd(config.WorkingDir))
    }

    // ...
}
```

同时在 `SandboxConfig` 结构中添加：
```go
type SandboxConfig struct {
    SandboxID   string
    ClaimUID    string
    ClaimName   string
    Image       string
    Command     []string
    Args        []string
    Env         map[string]string
    WorkingDir  string  // 新增
    ExposedPorts []int32
}
```

**优先级**: P0

---

### 3.2 [严重] DeleteSandbox 缺少优雅关闭

**文件**: `internal/agent/runtime/containerd_runtime.go:406-438`

**问题**:
- 当前直接发送 `SIGKILL`，进程无法优雅退出
- 参考 kubelet 的优雅关闭实现

**kubelet 方案**:
1. 发送 `SIGTERM` (或 `SIGINT` 让容器知道停止请求)
2. 等待 `gracePeriod` (默认 30 秒，可通过 `terminationGracePeriodSeconds` 配置)
3. 超时后发送 `SIGKILL` 强制终止

**建议修复**:
```go
const (
    defaultGracePeriod = 30 * time.Second
)

func (r *ContainerdRuntime) DeleteSandbox(ctx context.Context, sandboxID string) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    ctx = namespaces.WithNamespace(ctx, "k8s.io")
    container, err := r.client.LoadContainer(ctx, sandboxID)
    if err != nil {
        // 容器不存在，仍需尝试清理快照
        delete(r.sandboxes, sandboxID)
        snapshotName := sandboxID + "-snapshot"
        if err := r.client.SnapshotService("k8s.io").Remove(ctx, snapshotName); err != nil {
            log.Printf("Snapshot cleanup for %s: %v\n", snapshotName, err)
        }
        return nil
    }

    task, err := container.Task(ctx, nil)
    if err == nil {
        // 阶段 1: 发送 SIGTERM 请求优雅退出
        _ = task.Kill(ctx, syscall.SIGTERM)

        // 阶段 2: 等待优雅退出（超时后 SIGKILL）
        graceCtx, cancel := context.WithTimeout(ctx, defaultGracePeriod)
        defer cancel()

        // 等待任务退出
        _, err = task.Wait(graceCtx)
        if err != nil {
            // 超时或其他错误，强制 SIGKILL
            log.Printf("Graceful shutdown timeout for %s, sending SIGKILL\n", sandboxID)
            _ = task.Kill(ctx, syscall.SIGKILL)
            _, _ = task.Delete(ctx, containerd.WithProcessKill)
        } else {
            // 优雅退出成功，清理任务
            _, _ = task.Delete(ctx)
        }
    }

    // 删除容器及其快照
    if err := container.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
        log.Printf("Container delete error for %s: %v\n", sandboxID, err)
    }
    delete(r.sandboxes, sandboxID)
    return nil
}
```

**异步优化** (更高性能):
```go
// 使用工作池异步删除，不阻塞 API 响应
func (r *ContainerdRuntime) DeleteSandboxAsync(ctx context.Context, sandboxID string) error {
    go func() {
        r.DeleteSandbox(context.Background(), sandboxID)
    }()
    return nil
}
```

**优先级**: P0

---

### 3.3 [中等] gVisor Runtime 在 KIND 未测试

**问题描述**:
- 当前只有 containerd runtime 在 KIND 中测试通过
- gVisor runtime 需要额外配置和测试

**需要验证**:
1. KIND 节点是否支持 gVisor (`runsc`)
2. 网络 namespace 配置是否兼容
3. 性能对比数据

**建议**:
- 添加 `test/e2e/06-gvisor-validation/` 测试套件
- 在 KIND 中预装 gVisor runtime

**优先级**: P2

---

### 3.4 [一般] 日志使用 fmt.Printf 不规范

**统计**:
- `fmt.Printf` / `fmt.Println`: 28 处
- `log.*`: 50 处

**问题文件**:
- `internal/agent/runtime/containerd_runtime.go`: 7 处 `fmt.Printf`
- `internal/agent/infra/manager.go`: 2 处
- `cmd/agent/main.go`: 7 处

**建议**: 统一使用结构化日志（见第四章）

**优先级**: P3

---

## 四、日志系统问题

### 4.1 [严重] 缺少统一日志配置

**问题描述**:
- Controller 使用 `ctrl.Log` (controller-runtime log)
- Agent 使用标准 `log` 包
- Janitor 使用标准 `log` 包
- 大量使用 `fmt.Printf` (28 处)

**建议统一方案**:

```go
// internal/pkg/log/logger.go
package log

import (
    "io"
    "os"
    "path/filepath"

    "go.uber.org/zap"
    "go.uber.org/zap/zapcore"
)

var (
    globalLogger *zap.Logger
)

// Config 日志配置
type Config struct {
    Level       string `json:"level" env:"LOG_LEVEL" default:"info"`
    Format      string `json:"format" env:"LOG_FORMAT" default:"json"` // json or console
    Output      string `json:"output" env:"LOG_OUTPUT" default:"stdout"` // stdout, stderr, or file path
    Directory   string `json:"directory" env:"LOG_DIR" default:"/var/log/fast-sandbox"`
    MaxSize     int    `json:"maxSize" env:"LOG_MAX_SIZE" default:"100"` // MB
    MaxBackups  int    `json:"maxBackups" env:"LOG_MAX_BACKUPS" default:"3"`
    MaxAge      int    `json:"maxAge" env:"LOG_MAX_AGE" default:"7"` // days
    Compress    bool   `json:"compress" env:"LOG_COMPRESS" default:"true"`
}

// Init 初始化全局日志
func Init(cfg Config) error {
    // 解析日志级别
    level := zapcore.InfoLevel
    if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
        return err
    }

    // 配置编码器
    var encoder zapcore.Encoder
    encoderConfig := zap.NewProductionEncoderConfig()
    encoderConfig.TimeKey = "timestamp"
    encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

    if cfg.Format == "console" {
        encoder = zapcore.NewConsoleEncoder(encoderConfig)
    } else {
        encoder = zapcore.NewJSONEncoder(encoderConfig)
    }

    // 配置输出
    var writer io.Writer
    switch cfg.Output {
    case "stdout":
        writer = os.Stdout
    case "stderr":
        writer = os.Stderr
    default:
        if cfg.Directory != "" {
            if err := os.MkdirAll(cfg.Directory, 0755); err != nil {
                return err
            }
            logFile := filepath.Join(cfg.Directory, "fast-sandbox.log")
            var err error
            writer, err = os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
            if err != nil {
                return err
            }
        } else {
            writer = os.Stdout
        }
    }

    // 创建 Core
    core := zapcore.NewCore(
        encoder,
        zapcore.AddSync(writer),
        level,
    )

    globalLogger = zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
    return nil
}

// L 获取全局 logger
func L() *zap.Logger {
    if globalLogger == nil {
        // 默认配置
        _ = Init(Config{})
    }
    return globalLogger
}

// With 创建带字段的 logger
func With(fields ...zap.Field) *zap.Logger {
    return L().With(fields...)
}
```

**使用示例**:
```go
// Controller
import "fast-sandbox/internal/pkg/log"

logger := log.L().With(
    zap.String("component", "controller"),
    zap.String("sandbox", sandbox.Name),
)
logger.Info("Creating sandbox", zap.String("image", sandbox.Spec.Image))

// Agent
logger := log.L().With(
    zap.String("component", "agent"),
    zap.String("pod", podName),
)
logger.Error("Failed to create container", zap.Error(err))
```

**优先级**: P1

---

## 五、修复进度汇总

### P0 - 必须立即修复 (阻碍功能)
| ID | 问题 | 状态 | 完成日期 |
|----|------|------|----------|
| 1.1 | ExpireTime 逻辑错误 | ✅ 已完成 | 2026-01-18 |
| 1.2 | Finalizer 忽略删除错误 | ✅ 已完成 | 2026-01-18 |
| 2.1 | PB 接口字段缺失 (Envs, WorkingDir) | ✅ 已完成 | 2026-01-18 |
| 3.1 | WorkingDir 未生效 | ✅ 已完成 | 2026-01-18 |
| 3.2 | 缺少优雅关闭 (SIGTERM → wait → SIGKILL) | ✅ 已完成 | 2026-01-18 |

### 额外修复
| ID | 问题 | 状态 |
|----|------|------|
| - | NAMESPACE 环境变量未传递给 Agent Pod | ✅ 已完成 |

### P1 - 高优先级 (影响生产可用性)
| ID | 问题 | 影响 | 状态 |
|----|------|------|------|
| 1.3 | Registry 全局锁 | 扩展性差 | 待修复 |
| 1.4 | Namespace 隔离缺失 | 安全风险 | ✅ 已完成 |
| 2.2 | 缺少 Update 接口 | 运维不便 | ✅ 已完成 |
| 4.1 | 日志系统不统一 | 难以排查问题 | 待修复 |

### P2 - 中优先级 (改进体验)
| ID | 问题 | 影响 |
|----|------|------|
| 1.5 | Loop 调度效率 | 大规模时延迟高 |
| 1.6 | Agent 版本更新 | 需要手动操作 |
| 3.3 | gVisor 未测试 | 功能未验证 |

### P3 - 低优先级 (代码质量)
| ID | 问题 | 影响 |
|----|------|------|
| 3.4 | fmt.Printf 混用 | 代码不规范 |

---

## 六、建议实施计划

### Phase 1: 核心修复 (1-2 周)
1. 修复 1.1, 1.2, 3.1, 3.2 - 删除和生命周期相关
2. 修复 2.1 - PB 接口补全
3. 实现 4.1 - 统一日志系统

### Phase 2: 扩展性优化 (2-3 周)
1. 实现 1.3 - Registry 细粒度锁
2. 实现 1.4 - Namespace 隔离
3. 实现 2.2 - UpdateSandbox 接口

### Phase 3: 高级特性 (3-4 周)
1. 实现 1.5 - Pool 维度 Loop
2. 实现 1.6 - Agent 滚动更新
3. 实现 3.3 - gVisor 支持

---

## 附录：文件清单

**Controller 相关**:
- `internal/controller/sandbox_controller.go` (257 行)
- `internal/controller/sandboxpool_controller.go` (291 行)
- `internal/controller/agentpool/registry.go` (283 行)
- `internal/controller/agentcontrol/loop.go` (164 行)
- `internal/controller/fastpath/server.go`

**Agent 相关**:
- `cmd/agent/main.go` (64 行)
- `internal/agent/runtime/containerd_runtime.go` (590 行)
- `internal/agent/runtime/sandbox_manager.go`

**API 相关**:
- `api/proto/v1/fastpath.proto` (83 行)
- `api/v1alpha1/sandbox_types.go` (118 行)

**CTL 相关**:
- `cmd/fsb-ctl/cmd/run.go`
- `cmd/fsb-ctl/cmd/delete.go`
- `cmd/fsb-ctl/cmd/logs.go`

**日志相关**:
- 28 处 `fmt.Printf/Println`
- 50 处 `log.*`

---

*报告生成时间: 2026-01-18*
*审查人: Claude (AI Assistant)*
