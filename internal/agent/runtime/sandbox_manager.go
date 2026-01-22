package runtime

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"fast-sandbox/internal/api"
)

// SandboxManager 管理 sandbox 的生命周期
// 使用 Runtime 接口与底层容器运行时交互
type SandboxManager struct {
	mu       sync.RWMutex
	runtime  Runtime
	capacity int
	// sandboxes 维护 sandboxID -> metadata 的映射（从 runtime 同步）
	sandboxes map[string]*SandboxMetadata
	// sandboxPhases 维护 sandboxID -> phase 的映射（用于状态上报）
	// Phase: running, terminating, terminated
	sandboxPhases map[string]string
}

// NewSandboxManager 创建一个新的 SandboxManager
func NewSandboxManager(runtime Runtime) *SandboxManager {
	capVal := 10
	if capStr := os.Getenv("AGENT_CAPACITY"); capStr != "" {
		if v, err := strconv.Atoi(capStr); err == nil {
			capVal = v
		}
	}

	return &SandboxManager{
		runtime:       runtime,
		capacity:      capVal,
		sandboxes:     make(map[string]*SandboxMetadata),
		sandboxPhases: make(map[string]string),
	}
}

// CreateSandbox 创建单个 sandbox（命令式，幂等）
// 如果 sandbox 已存在，直接返回成功（幂等性）
// 返回创建时间戳供 Janitor 判断孤儿状态
// 优化: 将长耗时的 runtime.CreateSandbox 移出锁，只在更新缓存时持锁
func (m *SandboxManager) CreateSandbox(ctx context.Context, spec api.SandboxSpec) (*api.CreateSandboxResponse, error) {
	// 1. 快速幂等检查 (短暂读锁)
	m.mu.RLock()
	if _, exists := m.sandboxes[spec.SandboxID]; exists {
		m.mu.RUnlock()
		log.Printf("Sandbox %s already exists in cache, returning success (idempotent)", spec.SandboxID)
		return &api.CreateSandboxResponse{
			Success:   true,
			SandboxID: spec.SandboxID,
		}, nil
	}
	m.mu.RUnlock()

	// 也检查 runtime (不持锁)
	if existing, _ := m.runtime.GetSandbox(ctx, spec.SandboxID); existing != nil {
		log.Printf("Sandbox %s already exists in runtime, returning success (idempotent)", spec.SandboxID)
		// 同步到缓存
		m.mu.Lock()
		m.sandboxes[spec.SandboxID] = existing
		m.sandboxPhases[spec.SandboxID] = "running"
		m.mu.Unlock()
		return &api.CreateSandboxResponse{
			Success:   true,
			SandboxID: spec.SandboxID,
			CreatedAt: existing.CreatedAt,
		}, nil
	}

	// 2. 创建容器 (不持锁，可能秒级)
	config := &SandboxConfig{
		SandboxID:  spec.SandboxID,
		ClaimUID:   spec.ClaimUID,
		ClaimName:  spec.ClaimName,
		Image:      spec.Image,
		Command:    spec.Command,
		Args:       spec.Args,
		Env:        spec.Env,
		CPU:        spec.CPU,
		Memory:     spec.Memory,
		WorkingDir: spec.WorkingDir,
	}

	createdAt := time.Now().Unix()
	metadata, err := m.runtime.CreateSandbox(ctx, config)
	if err != nil {
		log.Printf("Failed to create sandbox %s: %v", spec.SandboxID, err)
		return &api.CreateSandboxResponse{
			Success: false,
			Message: fmt.Sprintf("create failed: %v", err),
		}, err
	}

	// 3. 更新缓存 (短暂写锁)
	m.mu.Lock()
	// 双重检查 (防止并发创建)
	if _, exists := m.sandboxes[spec.SandboxID]; exists {
		m.mu.Unlock()
		// 清理刚创建的容器
		log.Printf("Sandbox %s was created concurrently, cleaning up duplicate", spec.SandboxID)
		_ = m.runtime.DeleteSandbox(ctx, spec.SandboxID)
		return &api.CreateSandboxResponse{
			Success:   true,
			SandboxID: spec.SandboxID,
		}, nil
	}
	m.sandboxes[spec.SandboxID] = metadata
	m.sandboxPhases[spec.SandboxID] = "running"
	m.mu.Unlock()

	log.Printf("Created sandbox %s (image: %s)", spec.SandboxID, spec.Image)
	return &api.CreateSandboxResponse{
		Success:   true,
		SandboxID: spec.SandboxID,
		CreatedAt: createdAt,
	}, nil
}

// DeleteSandbox 删除单个 sandbox（命令式，幂等，异步）
// 立即返回成功，后台异步执行优雅关闭（SIGTERM → wait → SIGKILL）
// 如果 sandbox 不存在，直接返回成功（幂等性）
func (m *SandboxManager) DeleteSandbox(ctx context.Context, sandboxID string) (*api.DeleteSandboxResponse, error) {
	m.mu.Lock()

	// 1. 检查是否已经在删除中
	if phase, ok := m.sandboxPhases[sandboxID]; ok && phase == "terminating" {
		m.mu.Unlock()
		log.Printf("Sandbox %s is already terminating, returning success (idempotent)", sandboxID)
		return &api.DeleteSandboxResponse{
			Success: true,
		}, nil
	}

	// 2. 检查是否存在
	_, err := m.runtime.GetSandbox(ctx, sandboxID)
	if err != nil {
		m.mu.Unlock()
		// 不存在，视为删除成功（幂等性）
		log.Printf("Sandbox %s does not exist, returning success (idempotent)", sandboxID)
		return &api.DeleteSandboxResponse{
			Success: true,
		}, nil
	}

	// 3. 标记为 terminating，启动异步删除
	m.sandboxPhases[sandboxID] = "terminating"
	m.mu.Unlock()

	// 异步执行优雅关闭
	go m.asyncDelete(sandboxID)

	log.Printf("Sandbox %s marked for deletion (async graceful shutdown)", sandboxID)
	return &api.DeleteSandboxResponse{
		Success: true,
	}, nil
}

// asyncDelete 异步执行优雅关闭
// 流程: SIGTERM → 等待 10 秒 → SIGKILL → 标记 terminated → 清理
func (m *SandboxManager) asyncDelete(sandboxID string) {
	const gracefulTimeout = 10 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), gracefulTimeout+5*time.Second)
	defer cancel()

	// 1. 尝试优雅关闭
	if runtime, ok := m.runtime.(*ContainerdRuntime); ok {
		runtime.GracefulDeleteSandbox(ctx, sandboxID, gracefulTimeout)
	} else {
		// 其他运行时直接删除
		m.runtime.DeleteSandbox(ctx, sandboxID)
	}

	// 2. 更新状态为 terminated（保留在 map 中供 controller 读取）
	m.mu.Lock()
	m.sandboxPhases[sandboxID] = "terminated"
	m.mu.Unlock()

	log.Printf("Sandbox %s deletion completed", sandboxID)

	// 3. 延迟清理：给 controller 时间读取 terminated 状态
	// 30 秒后从 map 中完全删除
	go func() {
		time.Sleep(30 * time.Second)
		m.mu.Lock()
		delete(m.sandboxes, sandboxID)
		delete(m.sandboxPhases, sandboxID)
		m.mu.Unlock()
		log.Printf("Sandbox %s fully cleaned up from manager cache", sandboxID)
	}()
}

// GetLogs 获取沙箱日志
func (m *SandboxManager) GetLogs(ctx context.Context, sandboxID string, follow bool, w io.Writer) error {
	// 不加锁，因为日志读取是长耗时操作
	return m.runtime.GetSandboxLogs(ctx, sandboxID, follow, w)
}

// GetSandbox 获取指定 sandbox 的元数据
func (m *SandboxManager) GetSandbox(ctx context.Context, sandboxID string) (*SandboxMetadata, error) {
	return m.runtime.GetSandbox(ctx, sandboxID)
}

// ListSandboxes 列出所有当前运行的 sandbox
func (m *SandboxManager) ListSandboxes(ctx context.Context) ([]*SandboxMetadata, error) {
	return m.runtime.ListSandboxes(ctx)
}

// ListImages 列出节点上可用的镜像
func (m *SandboxManager) ListImages(ctx context.Context) ([]string, error) {
	return m.runtime.ListImages(ctx)
}

// GetCapacity 获取当前 Agent 的容量信息
func (m *SandboxManager) GetCapacity() int {
	return m.capacity
}

// GetRunningSandboxCount 获取当前运行的 sandbox 数量
func (m *SandboxManager) GetRunningSandboxCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sandboxes)
}

// GetAllSandboxStatuses 获取所有 sandbox 的状态（用于心跳上报）
// 优化: 先复制列表再查询 runtime，避免嵌套锁和长时间持锁
func (m *SandboxManager) GetAllSandboxStatuses(ctx context.Context) []api.SandboxStatus {
	// 1. 快速复制 sandbox 列表 (短暂读锁)
	m.mu.RLock()
	sandboxIDs := make([]string, 0, len(m.sandboxes))
	snapshots := make(map[string]*SandboxMetadata)
	phases := make(map[string]string)
	for id, meta := range m.sandboxes {
		sandboxIDs = append(sandboxIDs, id)
		snapshots[id] = meta
		phases[id] = m.sandboxPhases[id]
	}
	m.mu.RUnlock()

	// 2. 无锁查询 runtime 状态
	result := make([]api.SandboxStatus, 0, len(sandboxIDs))
	for _, sandboxID := range sandboxIDs {
		meta := snapshots[sandboxID]
		phase := phases[sandboxID]
		if phase == "" {
			phase = "running"
		}

		// 不持 Manager 锁调用 runtime
		runtimeStatus, _ := m.runtime.GetSandboxStatus(ctx, sandboxID)

		result = append(result, api.SandboxStatus{
			SandboxID: sandboxID,
			ClaimUID:  meta.ClaimUID,
			Phase:     phase,
			Message:   runtimeStatus,
			CreatedAt: meta.CreatedAt,
		})
	}

	return result
}

// Close 关闭 SandboxManager
func (m *SandboxManager) Close() error {
	return m.runtime.Close()
}
