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
}

// InMemoryRegistry is a simple in-memory implementation of AgentRegistry.
type InMemoryRegistry struct {
	mu     sync.RWMutex
	agents map[AgentID]AgentInfo
}

// NewInMemoryRegistry creates a new in-memory registry.
func NewInMemoryRegistry() *InMemoryRegistry {
	return &InMemoryRegistry{
		agents: make(map[AgentID]AgentInfo),
	}
}

func (r *InMemoryRegistry) RegisterOrUpdate(info AgentInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	if old, ok := r.agents[info.ID]; ok {
		info.Allocated = old.Allocated
		info.UsedPorts = old.UsedPorts
	}
	if info.UsedPorts == nil {
		info.UsedPorts = make(map[int32]bool)
	}
	r.agents[info.ID] = info
}

func (r *InMemoryRegistry) GetAllAgents() []AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]AgentInfo, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, a)
	}
	return out
}

func (r *InMemoryRegistry) GetAgentByID(id AgentID) (AgentInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[id]
	return a, ok
}

func (r *InMemoryRegistry) Allocate(sb *apiv1alpha1.Sandbox) (*AgentInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var bestID AgentID
	var minScore = 1000000

	for id, a := range r.agents {
		if a.PoolName != sb.Spec.PoolRef {
			continue
		}
		if a.Capacity > 0 && a.Allocated >= a.Capacity {
			continue
		}

		// 1. 端口冲突检查 (硬性约束)
		portConflict := false
		for _, p := range sb.Spec.ExposedPorts {
			if a.UsedPorts[p] {
				portConflict = true
				break
			}
		}
		if portConflict {
			continue
		}

		// 2. 镜像亲和性计算 (软性权重)
		hasImage := false
		for _, img := range a.Images {
			if img == sb.Spec.Image {
				hasImage = true
				break
			}
		}

		// 打分逻辑：
		// - 没镜像的节点基础分 +1000
		// - 负载 (Allocated) 基础分 +N
		// 目标：优先选已有镜像的，其次选负载低的
		score := a.Allocated
		if !hasImage {
			score += 1000
		}

		if score < minScore {
			minScore = score
			bestID = id
		}
	}

	if bestID == "" {
		return nil, fmt.Errorf("insufficient capacity or port conflict in pool %s", sb.Spec.PoolRef)
	}

	agent := r.agents[bestID]
	agent.Allocated++
	if agent.UsedPorts == nil {
		agent.UsedPorts = make(map[int32]bool)
	}
	for _, p := range sb.Spec.ExposedPorts {
		agent.UsedPorts[p] = true
	}
	r.agents[bestID] = agent
	
	res := agent
	return &res, nil
}

func (r *InMemoryRegistry) Release(id AgentID, sb *apiv1alpha1.Sandbox) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if a, ok := r.agents[id]; ok {
		if a.Allocated > 0 {
			a.Allocated--
		}
		for _, p := range sb.Spec.ExposedPorts {
			delete(a.UsedPorts, p)
		}
		r.agents[id] = a
	}
}

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
			a, ok := r.agents[id]
			if !ok {
				a = AgentInfo{
					ID:        id,
					PodName:   string(id),
					UsedPorts: make(map[int32]bool),
				}
			}
			if a.UsedPorts == nil {
				a.UsedPorts = make(map[int32]bool)
			}
			
			a.Allocated++
			for _, p := range sb.Spec.ExposedPorts {
				a.UsedPorts[p] = true
			}
			r.agents[id] = a
		}
	}
	return nil
}

func (r *InMemoryRegistry) Remove(id AgentID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, id)
}