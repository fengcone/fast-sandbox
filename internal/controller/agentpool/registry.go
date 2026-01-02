package agentpool

import (
	"sync"
	"time"
)

// AgentID is a logical identifier for an agent instance.
type AgentID string

// AgentInfo describes a sandbox agent pod registered in controller memory.
type AgentInfo struct {
	ID            AgentID
	Namespace     string
	PodName       string
	PodIP         string
	NodeName      string
	PoolName      string
	Capacity      int
	Allocated     int
	Images        []string
	LastHeartbeat time.Time
}

// AgentRegistry defines operations to manage agents in controller memory.
type AgentRegistry interface {
	RegisterOrUpdate(info AgentInfo)
	GetAllAgents() []AgentInfo
	GetAgentByID(id AgentID) (AgentInfo, bool)
	AllocateSlot(id AgentID) bool
	ReleaseSlot(id AgentID)
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

func (r *InMemoryRegistry) AllocateSlot(id AgentID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.agents[id]
	if !ok {
		return false
	}
	if a.Capacity > 0 && a.Allocated >= a.Capacity {
		return false
	}
	a.Allocated++
	r.agents[id] = a
	return true
}

func (r *InMemoryRegistry) ReleaseSlot(id AgentID) {
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.agents[id]
	if !ok {
		return
	}
	if a.Allocated > 0 {
		a.Allocated--
		r.agents[id] = a
	}
}
