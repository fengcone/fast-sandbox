package agentpool

import (
	"context"
	"fmt"
	"sync"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/api"

	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Lock ordering convention:
// 1. Always acquire registry-level locks (r.mu) before slot-level locks (slot.mu)
// 2. Never hold r.mu while performing expensive operations or I/O
// 3. Release r.mu before acquiring slot.mu whenever possible to minimize contention
// 4. This prevents deadlocks and improves concurrency

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

	// First pass: collect potential stale agents under read lock
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

	// Second pass: verify and delete under write lock
	// We need to re-check that the agent still exists and is still stale
	if len(staleAgents) > 0 {
		r.mu.Lock()
		for _, id := range staleAgents {
			if slot, exists := r.agents[id]; exists {
				// Re-verify the agent is still stale before deleting
				// Note: We don't hold slot.mu here to avoid lock ordering issues.
				// This is a best-effort cleanup; if the agent just updated its heartbeat,
				// it will be cleaned up in the next cycle.
				slot.mu.RLock()
				stale := now.Sub(slot.info.LastHeartbeat) > timeout
				slot.mu.RUnlock()
				if stale {
					delete(r.agents, id)
				}
			}
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
	totalStart := time.Now()

	for _, p := range sb.Spec.ExposedPorts {
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("invalid port %d: must be between 1 and 65535", p)
		}
	}

	// 1. Find candidates
	candidateStart := time.Now()
	r.mu.RLock()
	candidates := make([]*agentSlot, 0, len(r.agents))
	for _, slot := range r.agents {
		candidates = append(candidates, slot)
	}
	r.mu.RUnlock()
	candidateDuration := time.Since(candidateStart)

	var bestSlot *agentSlot
	var minScore = 1000000
	var imageHit bool

	// 2. Score agents and select best
	scoreStart := time.Now()
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

		klog.V(4).Info("Checking image affinity", "sandbox", sb.Name, "agent", info.ID, "hasImage", hasImage, "image", sb.Spec.Image)

		score := info.Allocated
		if !hasImage {
			score += 1000
		}

		slot.mu.RUnlock()

		if score < minScore {
			minScore = score
			bestSlot = slot
			imageHit = hasImage
		}
	}
	scoreDuration := time.Since(scoreStart)

	if bestSlot == nil {
		return nil, fmt.Errorf("insufficient capacity or port conflict in pool %s", sb.Spec.PoolRef)
	}

	// 3. Final allocation
	selectStart := time.Now()
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
	selectDuration := time.Since(selectStart)
	totalDuration := time.Since(totalStart)

	klog.V(2).InfoS("Registry Allocate timing",
		"sandbox", sb.Name,
		"total_ms", totalDuration.Milliseconds(),
		"candidate_ms", candidateDuration.Milliseconds(),
		"score_ms", scoreDuration.Milliseconds(),
		"select_ms", selectDuration.Milliseconds(),
		"selectedAgent", info.ID,
		"imageHit", imageHit,
		"agentCount", len(candidates))

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

	// Always release allocated slot - sandbox may have already been removed from
	// SandboxStatuses due to async deletion or heartbeat sync delay.
	// The presence or absence of the sandbox in statuses doesn't matter for
	// allocated count, only whether this specific sandbox was counting against capacity.
	if _, exists := slot.info.SandboxStatuses[sb.Name]; exists {
		delete(slot.info.SandboxStatuses, sb.Name)
	}

	if slot.info.Allocated > 0 {
		slot.info.Allocated--
	}
	for _, p := range sb.Spec.ExposedPorts {
		delete(slot.info.UsedPorts, p)
	}
}

func (r *InMemoryRegistry) Restore(ctx context.Context, c client.Reader) error {
	var sbList apiv1alpha1.SandboxList
	if err := c.List(ctx, &sbList); err != nil {
		return err
	}

	// Lock ordering: Always acquire r.mu before slot.mu to maintain consistency
	// with other operations in this file. We hold r.mu while creating slots,
	// then release it before modifying individual slot contents to minimize
	// lock contention.
	r.mu.Lock()
	var slotsToRestore []struct {
		id     AgentID
		sb     *apiv1alpha1.Sandbox
		create bool
		slot   *agentSlot
	}

	for _, sb := range sbList.Items {
		if sb.Status.AssignedPod != "" {
			id := AgentID(sb.Status.AssignedPod)
			slot, ok := r.agents[id]
			if !ok {
				// Create new slot but don't modify contents yet
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
				slotsToRestore = append(slotsToRestore, struct {
					id     AgentID
					sb     *apiv1alpha1.Sandbox
					create bool
					slot   *agentSlot
				}{id, &sb, true, slot})
			} else {
				slotsToRestore = append(slotsToRestore, struct {
					id     AgentID
					sb     *apiv1alpha1.Sandbox
					create bool
					slot   *agentSlot
				}{id, &sb, false, slot})
			}
		}
	}
	r.mu.Unlock()

	// Now modify each slot's contents without holding r.mu
	// This prevents lock ordering issues and minimizes critical section time
	for _, item := range slotsToRestore {
		item.slot.mu.Lock()
		if item.slot.info.UsedPorts == nil {
			item.slot.info.UsedPorts = make(map[int32]bool)
		}
		if item.slot.info.SandboxStatuses == nil {
			item.slot.info.SandboxStatuses = make(map[string]api.SandboxStatus)
		}
		item.slot.info.Allocated++
		for _, p := range item.sb.Spec.ExposedPorts {
			item.slot.info.UsedPorts[p] = true
		}
		item.slot.mu.Unlock()
	}

	return nil
}

func (r *InMemoryRegistry) Remove(id AgentID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, id)
}
