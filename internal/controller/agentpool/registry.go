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
	UsedPorts       map[int32]bool
	Images          []string
	SandboxStatuses map[string]api.SandboxStatus
	LastHeartbeat   time.Time
}

// AgentRegistry defines operations to manage agents in controller memory.
type AgentRegistry interface {
	RegisterOrUpdate(info AgentInfo)
	GetAllAgents() []AgentInfo
	GetAgentByID(id AgentID) (AgentInfo, bool)
	Allocate(sb *apiv1alpha1.Sandbox) (*AgentInfo, error)
	Release(id AgentID, sb *apiv1alpha1.Sandbox)
	Restore(ctx context.Context, c client.Reader) error
	Remove(id AgentID)
	CleanupStaleAgents(timeout time.Duration) int
}

type agentSlot struct {
	mu   sync.RWMutex
	info AgentInfo
}

type InMemoryRegistry struct {
	mu     sync.RWMutex
	agents map[AgentID]*agentSlot
}

// NewInMemoryRegistry creates a new in-memory registry.
func NewInMemoryRegistry() *InMemoryRegistry {
	return &InMemoryRegistry{
		agents: make(map[AgentID]*agentSlot),
	}
}

func (r *InMemoryRegistry) RegisterOrUpdate(info AgentInfo) {
	r.mu.RLock()
	slot, exists := r.agents[info.ID]
	r.mu.RUnlock()
	if !exists {
		r.mu.Lock()
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

	slot.mu.Lock()
	defer slot.mu.Unlock()

	allocated := slot.info.Allocated
	usedPorts := slot.info.UsedPorts
	sandboxStatuses := slot.info.SandboxStatuses

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

func (r *InMemoryRegistry) CleanupStaleAgents(timeout time.Duration) int {
	now := time.Now()

	r.mu.RLock()
	slots := make([]*agentSlot, 0, len(r.agents))
	ids := make([]AgentID, 0, len(r.agents))
	for id, slot := range r.agents {
		slots = append(slots, slot)
		ids = append(ids, id)
	}
	r.mu.RUnlock()

	var staleAgents []AgentID
	for i, slot := range slots {
		slot.mu.RLock()
		if now.Sub(slot.info.LastHeartbeat) > timeout {
			staleAgents = append(staleAgents, ids[i])
		}
		slot.mu.RUnlock()
	}

	if len(staleAgents) > 0 {
		r.mu.Lock()
		for _, id := range staleAgents {
			delete(r.agents, id)
		}
		r.mu.Unlock()
	}

	return len(staleAgents)
}

func (r *InMemoryRegistry) GetAllAgents() []AgentInfo {
	r.mu.RLock()
	slots := make([]*agentSlot, 0, len(r.agents))
	for _, slot := range r.agents {
		slots = append(slots, slot)
	}
	r.mu.RUnlock()

	out := make([]AgentInfo, 0, len(slots))
	for _, slot := range slots {
		slot.mu.RLock()
		out = append(out, slot.info)
		slot.mu.RUnlock()
	}
	return out
}

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

func (r *InMemoryRegistry) Allocate(sb *apiv1alpha1.Sandbox) (*AgentInfo, error) {
	for _, p := range sb.Spec.ExposedPorts {
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("invalid port %d: must be between 1 and 65535", p)
		}
	}

	r.mu.RLock()
	candidates := make([]*agentSlot, 0, len(r.agents))
	for _, slot := range r.agents {
		candidates = append(candidates, slot)
	}
	r.mu.RUnlock()

	var bestSlot *agentSlot
	var minScore = 1000000

	for _, slot := range candidates {
		slot.mu.RLock()
		info := slot.info

		if info.PoolName != sb.Spec.PoolRef {
			slot.mu.RUnlock()
			continue
		}
		if info.Namespace != sb.Namespace {
			slot.mu.RUnlock()
			continue
		}
		if info.Capacity > 0 && info.Allocated >= info.Capacity {
			slot.mu.RUnlock()
			continue
		}

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

		hasImage := false
		for _, img := range info.Images {
			if img == sb.Spec.Image {
				hasImage = true
				break
			}
		}

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

	bestSlot.mu.Lock()
	defer bestSlot.mu.Unlock()

	info := bestSlot.info
	if info.Capacity > 0 && info.Allocated >= info.Capacity {
		return nil, fmt.Errorf("agent %s capacity full during allocation", info.ID)
	}
	for _, p := range sb.Spec.ExposedPorts {
		if info.UsedPorts[p] {
			return nil, fmt.Errorf("port %d conflicted during allocation", p)
		}
	}

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

func (r *InMemoryRegistry) Release(id AgentID, sb *apiv1alpha1.Sandbox) {
	r.mu.RLock()
	slot, ok := r.agents[id]
	r.mu.RUnlock()

	if !ok {
		return
	}

	slot.mu.Lock()
	defer slot.mu.Unlock()

	if _, exists := slot.info.SandboxStatuses[sb.Name]; !exists && len(slot.info.SandboxStatuses) > 0 {
		for _, p := range sb.Spec.ExposedPorts {
			delete(slot.info.UsedPorts, p)
		}
		return
	}

	if slot.info.Allocated > 0 {
		slot.info.Allocated--
	}
	for _, p := range sb.Spec.ExposedPorts {
		delete(slot.info.UsedPorts, p)
	}
	delete(slot.info.SandboxStatuses, sb.Name)
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

func (r *InMemoryRegistry) Remove(id AgentID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, id)
}
