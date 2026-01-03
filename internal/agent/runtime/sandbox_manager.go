package runtime

import (
	"context"
	"fmt"
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
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. 构建期望的 sandbox 集合
	desiredMap := make(map[string]*api.SandboxDesired)
	for i := range desired {
		d := &desired[i]
		desiredMap[d.SandboxID] = d
	}

	// 2. 获取当前实际运行的 sandbox
	actual, err := m.runtime.ListSandboxes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list sandboxes: %w", err)
	}

	actualMap := make(map[string]*SandboxMetadata)
	for _, meta := range actual {
		actualMap[meta.SandboxID] = meta
	}

	// 3. 找出需要删除的 sandbox（在 actual 中但不在 desired 中）
	for sandboxID := range actualMap {
		if _, exists := desiredMap[sandboxID]; !exists {
			if err := m.runtime.DeleteSandbox(ctx, sandboxID); err != nil {
				// 记录错误但继续处理其他 sandbox
				fmt.Printf("Warning: failed to delete sandbox %s: %v\n", sandboxID, err)
			} else {
				delete(actualMap, sandboxID)
			}
		}
	}

	// 4. 找出需要创建的 sandbox（在 desired 中但不在 actual 中）
	for sandboxID, d := range desiredMap {
		if _, exists := actualMap[sandboxID]; !exists {
			config := &SandboxConfig{
				SandboxID: d.SandboxID,
				ClaimUID:  d.ClaimUID,
				ClaimName: d.ClaimName,
				Image:     d.Image,
				Command:   d.Command,
				Args:      d.Args,
				Env:       d.Env,
				CPU:       d.CPU,
				Memory:    d.Memory,
				Port:      d.Port,
			}

			meta, err := m.runtime.CreateSandbox(ctx, config)
			if err != nil {
				// 记录错误但继续处理其他 sandbox
				fmt.Printf("Warning: failed to create sandbox %s: %v\n", sandboxID, err)
				// 创建失败的 sandbox 也要返回状态
				actualMap[sandboxID] = &SandboxMetadata{
					SandboxID: sandboxID,
					ClaimUID:  d.ClaimUID,
					Status:    "failed",
				}
			} else {
				actualMap[sandboxID] = meta
			}
		}
	}

	// 5. 构建返回的状态列表
	statuses := make([]api.SandboxStatus, 0, len(actualMap))
	for _, meta := range actualMap {
		status := api.SandboxStatus{
			SandboxID: meta.SandboxID,
			ClaimUID:  meta.ClaimUID,
			Phase:     meta.Status,
			Port:      meta.Port,
		}
		statuses = append(statuses, status)
	}

	// 6. 更新内部缓存
	m.sandboxes = actualMap

	return statuses, nil
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
