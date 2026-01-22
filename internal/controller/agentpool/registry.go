package agentpool

import (
	"context"
	"fmt"
	"sync"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/api"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AgentID is a logical identifier for an agent instance.
type AgentID string

// AgentInfo describes a sandbox agent pod registered in controller memory.
type AgentInfo struct {
	ID              AgentID
	Namespace       string
	PodName         string
	PodIP           string
	NodeName        string
	PoolName        string
	Capacity        int
	Allocated       int
	UsedPorts       map[int32]bool // 记录当前节点已占用的端口
	Images          []string       // 该节点已缓存的镜像列表
	SandboxStatuses map[string]api.SandboxStatus
	LastHeartbeat   time.Time
}

// AgentRegistry defines operations to manage agents in controller memory.
type AgentRegistry interface {
	RegisterOrUpdate(info AgentInfo)
	GetAllAgents() []AgentInfo
	GetAgentByID(id AgentID) (AgentInfo, bool)

	// Allocate 尝试根据 Sandbox 的需求（PoolRef, ExposedPorts 等）原子分配一个插槽
	// 优先选择已有镜像的节点
	Allocate(sb *apiv1alpha1.Sandbox) (*AgentInfo, error)

	// Release 释放指定 Agent 上的 Sandbox 占用的插槽和端口
	Release(id AgentID, sb *apiv1alpha1.Sandbox)

	// Restore 从 K8s 现状恢复内存状态
	Restore(ctx context.Context, c client.Reader) error

	Remove(id AgentID)

	// CleanupStaleAgents 移除超过指定时间未更新的 Agent
	CleanupStaleAgents(timeout time.Duration) int
}

// agentSlot 是单个 Agent 的锁保护容器
// 用于细粒度锁优化，每个 Agent 独立锁
type agentSlot struct {
	mu   sync.RWMutex
	info AgentInfo
}

// InMemoryRegistry is a fine-grained locking implementation of AgentRegistry.
// 优化: 使用细粒度锁，每个 Agent 独立锁，减少全局锁竞争
type InMemoryRegistry struct {
	mu     sync.RWMutex           // 仅保护 agents map 结构
	agents map[AgentID]*agentSlot // 每个 Agent 有独立的 slot 锁
}

// NewInMemoryRegistry creates a new in-memory registry.
func NewInMemoryRegistry() *InMemoryRegistry {
	return &InMemoryRegistry{
		agents: make(map[AgentID]*agentSlot),
	}
}

// RegisterOrUpdate 注册或更新 Agent 信息
// 优化: 只在创建新 slot 时持全局锁，更新时只持单个 slot 锁
func (r *InMemoryRegistry) RegisterOrUpdate(info AgentInfo) {
	// 1. 快速检查是否存在 (读锁)
	r.mu.RLock()
	slot, exists := r.agents[info.ID]
	r.mu.RUnlock()

	// 2. 不存在则创建 (短暂写锁)
	if !exists {
		r.mu.Lock()
		// 双重检查
		slot, exists = r.agents[info.ID]
		if !exists {
			slot = &agentSlot{
				info: AgentInfo{
					ID:              info.ID,
					UsedPorts:       make(map[int32]bool),
					SandboxStatuses: make(map[string]api.SandboxStatus),
				},
			}
			r.agents[info.ID] = slot
		}
		r.mu.Unlock()
	}

	// 3. 更新 slot (只锁单个 Agent)
	slot.mu.Lock()
	defer slot.mu.Unlock()

	// 保留本地状态 (Allocated, UsedPorts)
	allocated := slot.info.Allocated
	usedPorts := slot.info.UsedPorts
	sandboxStatuses := slot.info.SandboxStatuses

	// 更新来自心跳的信息
	slot.info = info
	slot.info.Allocated = allocated

	if usedPorts != nil {
		slot.info.UsedPorts = usedPorts
	} else {
		slot.info.UsedPorts = make(map[int32]bool)
	}

	if sandboxStatuses != nil && info.SandboxStatuses == nil {
		slot.info.SandboxStatuses = sandboxStatuses
	} else if info.SandboxStatuses == nil {
		slot.info.SandboxStatuses = make(map[string]api.SandboxStatus)
	}
}

// CleanupStaleAgents 移除超过指定时间未更新的 Agent
// 用于防止已删除/宕机 Agent 的记录永久占用内存
func (r *InMemoryRegistry) CleanupStaleAgents(timeout time.Duration) int {
	now := time.Now()

	// 1. 收集所有 slot (读锁)
	r.mu.RLock()
	slots := make([]*agentSlot, 0, len(r.agents))
	ids := make([]AgentID, 0, len(r.agents))
	for id, slot := range r.agents {
		slots = append(slots, slot)
		ids = append(ids, id)
	}
	r.mu.RUnlock()

	// 2. 检查每个 slot (单 slot 读锁)
	var staleAgents []AgentID
	for i, slot := range slots {
		slot.mu.RLock()
		if now.Sub(slot.info.LastHeartbeat) > timeout {
			staleAgents = append(staleAgents, ids[i])
		}
		slot.mu.RUnlock()
	}

	// 3. 删除过期 Agent (全局写锁)
	if len(staleAgents) > 0 {
		r.mu.Lock()
		for _, id := range staleAgents {
			delete(r.agents, id)
		}
		r.mu.Unlock()
	}

	return len(staleAgents)
}

// GetAllAgents 返回所有 Agent 的快照
func (r *InMemoryRegistry) GetAllAgents() []AgentInfo {
	r.mu.RLock()
	slots := make([]*agentSlot, 0, len(r.agents))
	for _, slot := range r.agents {
		slots = append(slots, slot)
	}
	r.mu.RUnlock()

	// 复制每个 Agent 信息 (单 slot 读锁)
	out := make([]AgentInfo, 0, len(slots))
	for _, slot := range slots {
		slot.mu.RLock()
		out = append(out, slot.info)
		slot.mu.RUnlock()
	}
	return out
}

// GetAgentByID 获取指定 Agent 的信息
func (r *InMemoryRegistry) GetAgentByID(id AgentID) (AgentInfo, bool) {
	r.mu.RLock()
	slot, ok := r.agents[id]
	r.mu.RUnlock()

	if !ok {
		return AgentInfo{}, false
	}

	slot.mu.RLock()
	info := slot.info
	slot.mu.RUnlock()

	return info, true
}

// Allocate 原子分配一个 Agent 插槽
// 优化: 两阶段分配 - 评分阶段只读，分配阶段只锁选中的 Agent
func (r *InMemoryRegistry) Allocate(sb *apiv1alpha1.Sandbox) (*AgentInfo, error) {
	// 端口范围验证 (有效端口范围: 1-65535)
	for _, p := range sb.Spec.ExposedPorts {
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("invalid port %d: must be between 1 and 65535", p)
		}
	}

	// 1. 收集候选 Agent (全局读锁)
	r.mu.RLock()
	candidates := make([]*agentSlot, 0, len(r.agents))
	for _, slot := range r.agents {
		candidates = append(candidates, slot)
	}
	r.mu.RUnlock()

	// 2. 评分阶段 (每个 slot 读锁)
	var bestSlot *agentSlot
	var minScore = 1000000

	for _, slot := range candidates {
		slot.mu.RLock()
		info := slot.info

		// 过滤条件
		if info.PoolName != sb.Spec.PoolRef {
			slot.mu.RUnlock()
			continue
		}
		// Namespace 强制校验
		if info.Namespace != sb.Namespace {
			slot.mu.RUnlock()
			continue
		}
		// 容量检查
		if info.Capacity > 0 && info.Allocated >= info.Capacity {
			slot.mu.RUnlock()
			continue
		}

		// 端口冲突检查
		portConflict := false
		for _, p := range sb.Spec.ExposedPorts {
			if info.UsedPorts[p] {
				portConflict = true
				break
			}
		}
		if portConflict {
			slot.mu.RUnlock()
			continue
		}

		// 镜像亲和性计算
		hasImage := false
		for _, img := range info.Images {
			if img == sb.Spec.Image {
				hasImage = true
				break
			}
		}

		// 打分
		score := info.Allocated
		if !hasImage {
			score += 1000
		}

		slot.mu.RUnlock()

		if score < minScore {
			minScore = score
			bestSlot = slot
		}
	}

	if bestSlot == nil {
		return nil, fmt.Errorf("insufficient capacity or port conflict in pool %s", sb.Spec.PoolRef)
	}

	// 3. 分配阶段 (只锁选中的 Agent)
	bestSlot.mu.Lock()
	defer bestSlot.mu.Unlock()

	// 双重检查：分配期间状态可能已变
	info := bestSlot.info
	if info.Capacity > 0 && info.Allocated >= info.Capacity {
		return nil, fmt.Errorf("agent %s capacity full during allocation", info.ID)
	}
	for _, p := range sb.Spec.ExposedPorts {
		if info.UsedPorts[p] {
			return nil, fmt.Errorf("port %d conflicted during allocation", p)
		}
	}

	// 执行分配
	bestSlot.info.Allocated++
	if bestSlot.info.UsedPorts == nil {
		bestSlot.info.UsedPorts = make(map[int32]bool)
	}
	for _, p := range sb.Spec.ExposedPorts {
		bestSlot.info.UsedPorts[p] = true
	}

	res := bestSlot.info
	return &res, nil
}

// Release 释放指定 Agent 上的 Sandbox 占用的插槽和端口
func (r *InMemoryRegistry) Release(id AgentID, sb *apiv1alpha1.Sandbox) {
	r.mu.RLock()
	slot, ok := r.agents[id]
	r.mu.RUnlock()

	if !ok {
		return
	}

	slot.mu.Lock()
	defer slot.mu.Unlock()

	// 验证 sandbox 是否真的分配给这个 agent
	if _, exists := slot.info.SandboxStatuses[sb.Name]; !exists && len(slot.info.SandboxStatuses) > 0 {
		// 安全起见仍然清理端口
		for _, p := range sb.Spec.ExposedPorts {
			delete(slot.info.UsedPorts, p)
		}
		return
	}

	// 执行释放
	if slot.info.Allocated > 0 {
		slot.info.Allocated--
	}
	for _, p := range sb.Spec.ExposedPorts {
		delete(slot.info.UsedPorts, p)
	}
	delete(slot.info.SandboxStatuses, sb.Name)
}

// Restore 从 K8s 现状恢复内存状态
func (r *InMemoryRegistry) Restore(ctx context.Context, c client.Reader) error {
	var sbList apiv1alpha1.SandboxList
	if err := c.List(ctx, &sbList); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, sb := range sbList.Items {
		if sb.Status.AssignedPod != "" {
			id := AgentID(sb.Status.AssignedPod)
			slot, ok := r.agents[id]
			if !ok {
				slot = &agentSlot{
					info: AgentInfo{
						ID:              id,
						PodName:         string(id),
						UsedPorts:       make(map[int32]bool),
						SandboxStatuses: make(map[string]api.SandboxStatus),
						LastHeartbeat:   time.Now(),
					},
				}
				r.agents[id] = slot
			}

			slot.mu.Lock()
			if slot.info.UsedPorts == nil {
				slot.info.UsedPorts = make(map[int32]bool)
			}
			if slot.info.SandboxStatuses == nil {
				slot.info.SandboxStatuses = make(map[string]api.SandboxStatus)
			}
			slot.info.Allocated++
			for _, p := range sb.Spec.ExposedPorts {
				slot.info.UsedPorts[p] = true
			}
			slot.mu.Unlock()
		}
	}
	return nil
}

// Remove 移除指定 Agent
func (r *InMemoryRegistry) Remove(id AgentID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, id)
}
