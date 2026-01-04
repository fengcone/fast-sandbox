package runtime

import (
	"context"
	"sync"

	"fast-sandbox/internal/api"
)

// SandboxManager 管理 sandbox 的生命周期
// 使用 Runtime 接口与底层容器运行时交互
type SandboxManager struct {
	mu      sync.RWMutex
	runtime Runtime
	// sandboxes 维护 sandboxID -> metadata 的映射（从 runtime 同步）
	sandboxes map[string]*SandboxMetadata
}

// NewSandboxManager 创建一个新的 SandboxManager
func NewSandboxManager(runtime Runtime) *SandboxManager {
	return &SandboxManager{
		runtime:   runtime,
		sandboxes: make(map[string]*SandboxMetadata),
	}
}

// SyncSandboxes 同步期望的 sandbox 列表
// 这是 Controller 调用的主要接口，实现声明式状态同步
func (m *SandboxManager) SyncSandboxes(ctx context.Context, desired []api.SandboxDesired) ([]api.SandboxStatus, error) {
	return nil, nil
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
// TODO: 根据实际资源使用情况计算
func (m *SandboxManager) GetCapacity() int {
	// 临时返回固定值
	return 10
}

// GetRunningSandboxCount 获取当前运行的 sandbox 数量
func (m *SandboxManager) GetRunningSandboxCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sandboxes)
}

// Close 关闭 SandboxManager
func (m *SandboxManager) Close() error {
	return m.runtime.Close()
}
