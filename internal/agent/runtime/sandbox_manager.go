package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"

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
		runtime:   runtime,
		capacity:  capVal,
		sandboxes: make(map[string]*SandboxMetadata),
	}
}

// SyncSandboxes 同步期望的 sandbox 列表
// 这是 Controller 调用的主要接口，实现声明式状态同步
// 返回错误时表示部分或全部操作失败，调用方应根据错误信息决定是否重试
func (m *SandboxManager) SyncSandboxes(ctx context.Context, desired []api.SandboxSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. 获取当前所有 Sandboxes
	currentSandboxes, err := m.runtime.ListSandboxes(ctx)
	if err != nil {
		return fmt.Errorf("failed to list current sandboxes: %w", err)
	}

	currentMap := make(map[string]*SandboxMetadata)
	for _, sb := range currentSandboxes {
		currentMap[sb.SandboxID] = sb
	}

	desiredMap := make(map[string]api.SandboxSpec)
	for _, spec := range desired {
		desiredMap[spec.SandboxID] = spec
	}

	// 收集所有操作错误
	var createErrors []string
	var deleteErrors []string

	// 2. 找出需要创建的 (在 Desired 中，不在 Current 中)
	for id, spec := range desiredMap {
		if _, exists := currentMap[id]; !exists {
			// 创建新的 Sandbox
			config := &SandboxConfig{
				SandboxID: spec.SandboxID,
				ClaimUID:  spec.ClaimUID,
				ClaimName: spec.ClaimName,
				Image:     spec.Image,
				Command:   spec.Command,
				Args:      spec.Args,
				Env:       spec.Env,
				CPU:       spec.CPU,
				Memory:    spec.Memory,
			}
			log.Printf("Creating sandbox: %s (Image: %s)", id, spec.Image)
			if _, err := m.runtime.CreateSandbox(ctx, config); err != nil {
				errMsg := fmt.Sprintf("create %s: %v", id, err)
				log.Printf("Failed to create sandbox %s: %v", id, err)
				createErrors = append(createErrors, errMsg)
			}
		}
	}

	// 3. 找出需要删除的 (在 Current 中，不在 Desired 中)
	for id := range currentMap {
		if _, exists := desiredMap[id]; !exists {
			log.Printf("Deleting sandbox: %s", id)
			if err := m.runtime.DeleteSandbox(ctx, id); err != nil {
				errMsg := fmt.Sprintf("delete %s: %v", id, err)
				log.Printf("Failed to delete sandbox %s: %v", id, err)
				deleteErrors = append(deleteErrors, errMsg)
			}
		}
	}

	// 如果有任何错误，返回组合错误
	if len(createErrors) > 0 || len(deleteErrors) > 0 {
		var allErrors []string
		if len(createErrors) > 0 {
			allErrors = append(allErrors, fmt.Sprintf("create errors: [%s]", strings.Join(createErrors, "; ")))
		}
		if len(deleteErrors) > 0 {
			allErrors = append(allErrors, fmt.Sprintf("delete errors: [%s]", strings.Join(deleteErrors, "; ")))
		}
		return errors.New(strings.Join(allErrors, ", "))
	}

	return nil
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

// Close 关闭 SandboxManager
func (m *SandboxManager) Close() error {
	return m.runtime.Close()
}
