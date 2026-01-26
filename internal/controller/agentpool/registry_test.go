package agentpool

import (
	"sync"
	"testing"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/api"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ============================================================================
// Test Helpers
// ============================================================================

func newTestAgentInfo(id AgentID, opts ...func(*AgentInfo)) AgentInfo {
	info := AgentInfo{
		ID:              id,
		Namespace:       "default",
		PodName:         string(id),
		PodIP:           "10.0.0.1",
		NodeName:        "test-node",
		PoolName:        "test-pool",
		Capacity:        10,
		Allocated:       0,
		UsedPorts:       make(map[int32]bool),
		Images:          []string{},
		SandboxStatuses: make(map[string]api.SandboxStatus),
		LastHeartbeat:   time.Now(),
	}
	for _, opt := range opts {
		opt(&info)
	}
	return info
}

func withNamespace(ns string) func(*AgentInfo) {
	return func(a *AgentInfo) { a.Namespace = ns }
}

func withPoolName(pool string) func(*AgentInfo) {
	return func(a *AgentInfo) { a.PoolName = pool }
}

func withCapacity(cap int) func(*AgentInfo) {
	return func(a *AgentInfo) { a.Capacity = cap }
}

func withAllocated(alloc int) func(*AgentInfo) {
	return func(a *AgentInfo) { a.Allocated = alloc }
}

func withUsedPorts(ports ...int32) func(*AgentInfo) {
	return func(a *AgentInfo) {
		a.UsedPorts = make(map[int32]bool)
		for _, p := range ports {
			a.UsedPorts[p] = true
		}
	}
}

func withImages(images ...string) func(*AgentInfo) {
	return func(a *AgentInfo) { a.Images = images }
}

func withSandboxStatus(name string, status api.SandboxStatus) func(*AgentInfo) {
	return func(a *AgentInfo) {
		if a.SandboxStatuses == nil {
			a.SandboxStatuses = make(map[string]api.SandboxStatus)
		}
		a.SandboxStatuses[name] = status
	}
}

func withLastHeartbeat(t time.Time) func(*AgentInfo) {
	return func(a *AgentInfo) { a.LastHeartbeat = t }
}

func newTestSandbox(name string, opts ...func(*apiv1alpha1.Sandbox)) *apiv1alpha1.Sandbox {
	sb := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: apiv1alpha1.SandboxSpec{
			Image:   "alpine:latest",
			PoolRef: "test-pool",
		},
	}
	for _, opt := range opts {
		opt(sb)
	}
	return sb
}

func withSandboxNamespace(ns string) func(*apiv1alpha1.Sandbox) {
	return func(sb *apiv1alpha1.Sandbox) { sb.Namespace = ns }
}

func withSandboxPoolRef(pool string) func(*apiv1alpha1.Sandbox) {
	return func(sb *apiv1alpha1.Sandbox) { sb.Spec.PoolRef = pool }
}

func withSandboxImage(image string) func(*apiv1alpha1.Sandbox) {
	return func(sb *apiv1alpha1.Sandbox) { sb.Spec.Image = image }
}

func withSandboxPorts(ports ...int32) func(*apiv1alpha1.Sandbox) {
	return func(sb *apiv1alpha1.Sandbox) { sb.Spec.ExposedPorts = ports }
}

// ============================================================================
// 1. RegisterOrUpdate Tests
// ============================================================================

func TestInMemoryRegistry_RegisterOrUpdate_NewAgent(t *testing.T) {
	// R-01: Registering a new agent initializes it correctly
	registry := NewInMemoryRegistry()

	agentInfo := newTestAgentInfo("agent-1",
		withPoolName("pool-a"),
		withCapacity(5),
		withImages("alpine:latest", "nginx:latest"),
	)

	registry.RegisterOrUpdate(agentInfo)

	// Verify agent was registered
	agent, ok := registry.GetAgentByID("agent-1")
	require.True(t, ok, "Agent should be registered")
	assert.Equal(t, AgentID("agent-1"), agent.ID)
	assert.Equal(t, "pool-a", agent.PoolName)
	assert.Equal(t, 5, agent.Capacity)
	assert.Equal(t, 0, agent.Allocated, "New agent should start with 0 allocations")
	assert.NotNil(t, agent.UsedPorts, "UsedPorts should be initialized")
	assert.NotNil(t, agent.SandboxStatuses, "SandboxStatuses should be initialized")
	assert.Equal(t, []string{"alpine:latest", "nginx:latest"}, agent.Images)
}

func TestInMemoryRegistry_RegisterOrUpdate_UpdateExisting(t *testing.T) {
	// R-02: Updating an existing agent preserves allocated/ports
	registry := NewInMemoryRegistry()

	// Initial registration
	agentInfo := newTestAgentInfo("agent-1",
		withCapacity(5),
	)
	registry.RegisterOrUpdate(agentInfo)

	// Perform an allocation to set allocated count and ports
	sandbox := newTestSandbox("test-sb", withSandboxPorts(8080, 9090))
	_, err := registry.Allocate(sandbox)
	require.NoError(t, err)

	// Verify state after allocation
	agent, _ := registry.GetAgentByID("agent-1")
	assert.Equal(t, 1, agent.Allocated)
	assert.True(t, agent.UsedPorts[8080])
	assert.True(t, agent.UsedPorts[9090])

	// Update with new heartbeat info (simulating heartbeat from agent)
	updatedInfo := newTestAgentInfo("agent-1",
		withCapacity(10), // Capacity changed
		withImages("ubuntu:latest"),
	)
	registry.RegisterOrUpdate(updatedInfo)

	// Verify allocated count and ports are preserved
	agent, ok := registry.GetAgentByID("agent-1")
	require.True(t, ok)
	assert.Equal(t, 1, agent.Allocated, "Allocated should be preserved")
	assert.Equal(t, 10, agent.Capacity, "Capacity should be updated")
	assert.True(t, agent.UsedPorts[8080], "UsedPorts should be preserved")
	assert.True(t, agent.UsedPorts[9090], "UsedPorts should be preserved")
	assert.Equal(t, []string{"ubuntu:latest"}, agent.Images, "Images should be updated")
}

func TestInMemoryRegistry_RegisterOrUpdate_PreservesSandboxStatuses(t *testing.T) {
	// R-03: Updating preserves existing sandbox statuses when input has nil
	registry := NewInMemoryRegistry()

	// Initial registration with sandbox status
	agentInfo := newTestAgentInfo("agent-1")
	registry.RegisterOrUpdate(agentInfo)

	// Manually add a sandbox status (simulating status sync from agent)
	agent, _ := registry.GetAgentByID("agent-1")
	agent.SandboxStatuses["sb-1"] = api.SandboxStatus{Phase: "running"}

	// Update with info that has nil SandboxStatuses (default from newTestAgentInfo)
	updatedInfo := AgentInfo{
		ID:        "agent-1",
		PoolName:  "test-pool",
		Capacity:  20,
		Namespace: "default",
		PodName:   "agent-1",
		// SandboxStatuses is nil (not set)
	}
	registry.RegisterOrUpdate(updatedInfo)

	// Verify sandbox status is preserved
	agent, ok := registry.GetAgentByID("agent-1")
	require.True(t, ok)
	assert.Contains(t, agent.SandboxStatuses, "sb-1")
	assert.Equal(t, "running", agent.SandboxStatuses["sb-1"].Phase)
}

// ============================================================================
// 2. Allocate Tests
// ============================================================================

func TestInMemoryRegistry_Allocate_ImageAffinity(t *testing.T) {
	// A-01: Allocation prefers agents with cached image
	registry := NewInMemoryRegistry()

	// Register two agents - one with the image, one without
	registry.RegisterOrUpdate(newTestAgentInfo("agent-with-image",
		withPoolName("test-pool"),
		withCapacity(10),
		withImages("alpine:latest", "nginx:latest"),
	))
	registry.RegisterOrUpdate(newTestAgentInfo("agent-without-image",
		withPoolName("test-pool"),
		withCapacity(10),
		withImages("ubuntu:latest"),
	))

	// Simulate existing allocations by allocating dummy sandboxes
	for i := 0; i < 3; i++ {
		dummySB := newTestSandbox("dummy-"+string(rune('0'+i)))
		registry.Allocate(dummySB)
	}
	// agent-with-image now has 3 allocations
	for i := 0; i < 1; i++ {
		dummySB := newTestSandbox("dummy2-"+string(rune('0'+i)))
		registry.Allocate(dummySB)
	}
	// agent-without-image now has 1 allocation (both agents share capacity since they're in same pool)

	sandbox := newTestSandbox("test-sb",
		withSandboxImage("alpine:latest"),
	)

	agent, err := registry.Allocate(sandbox)
	require.NoError(t, err)
	// The agent with the cached image should be preferred
	// Both agents share the pool, so the allocation goes to the one with cached image
	assert.Equal(t, "test-pool", agent.PoolName)

	// Verify an allocation happened
	agents := registry.GetAllAgents()
	totalAllocated := 0
	for _, a := range agents {
		totalAllocated += a.Allocated
	}
	assert.Equal(t, 5, totalAllocated, "Should have 5 total allocations")
}

func TestInMemoryRegistry_Allocate_CapacityCheck(t *testing.T) {
	// A-02: Allocation fails when no capacity
	registry := NewInMemoryRegistry()

	// Register a limited capacity agent
	registry.RegisterOrUpdate(newTestAgentInfo("full-agent",
		withPoolName("test-pool"),
		withCapacity(2),
	))

	// Fill it to capacity
	for i := 0; i < 2; i++ {
		dummySB := newTestSandbox("fill-"+string(rune('0'+i)))
		_, err := registry.Allocate(dummySB)
		require.NoError(t, err)
	}

	// Verify it's full
	agent, _ := registry.GetAgentByID("full-agent")
	require.Equal(t, 2, agent.Allocated)

	sandbox := newTestSandbox("test-sb",
		withSandboxPoolRef("test-pool"),
	)

	_, err := registry.Allocate(sandbox)
	assert.Error(t, err, "Should fail when no capacity in matching pool")
	assert.Contains(t, err.Error(), "insufficient capacity")
}

func TestInMemoryRegistry_Allocate_PortConflict(t *testing.T) {
	// A-03: Allocation handles port conflicts correctly
	registry := NewInMemoryRegistry()

	// Register a single agent
	registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
		withPoolName("test-pool"),
		withCapacity(10),
	))

	// Allocate one sandbox with port 8080
	sb1 := newTestSandbox("sb-1", withSandboxPorts(8080))
	_, _ = registry.Allocate(sb1)

	// Try to allocate another sandbox with BOTH 8080 and 9090
	// This should fail because 8080 is already in use
	sandbox := newTestSandbox("test-sb",
		withSandboxPorts(8080, 9090),
	)

	_, err := registry.Allocate(sandbox)
	assert.Error(t, err, "Should fail when port 8080 is already in use")
	assert.Contains(t, err.Error(), "insufficient capacity or port conflict")
}

func TestInMemoryRegistry_Allocate_SelectsAgentWithAvailablePorts(t *testing.T) {
	// A-04: Allocation selects agent with available ports
	registry := NewInMemoryRegistry()

	// Register two agents
	registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
		withPoolName("test-pool"),
		withCapacity(10),
	))
	registry.RegisterOrUpdate(newTestAgentInfo("agent-2",
		withPoolName("test-pool"),
		withCapacity(10),
	))

	// Allocate a sandbox with port 8080 to agent-1
	sb1 := newTestSandbox("sb-1", withSandboxPorts(8080))
	allocated1, _ := registry.Allocate(sb1)
	agentWithPort8080 := allocated1.ID

	// Now try to allocate a sandbox that needs BOTH 8080 and 9090
	// It should go to the agent that doesn't have 8080 in use
	sandbox := newTestSandbox("test-sb",
		withSandboxPorts(8080, 9090),
	)

	agent, err := registry.Allocate(sandbox)
	require.NoError(t, err)
	assert.NotEqual(t, agentWithPort8080, agent.ID, "Should select agent without port 8080 conflict")
	assert.True(t, agent.UsedPorts[8080], "Port 8080 should be marked as used in returned info")
	assert.True(t, agent.UsedPorts[9090], "Port 9090 should be marked as used in returned info")

	// Verify ports are marked in registry
	storedAgent, _ := registry.GetAgentByID(agent.ID)
	assert.True(t, storedAgent.UsedPorts[8080])
	assert.True(t, storedAgent.UsedPorts[9090])
}

func TestInMemoryRegistry_Allocate_NoMatch(t *testing.T) {
	// A-05: Allocation returns error when no suitable agents
	registry := NewInMemoryRegistry()

	// Register agents in different namespace
	registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
		withNamespace("kube-system"),
		withPoolName("test-pool"),
	))

	// Register agents in different pool
	registry.RegisterOrUpdate(newTestAgentInfo("agent-2",
		withNamespace("default"),
		withPoolName("other-pool"),
	))

	sandbox := newTestSandbox("test-sb",
		withSandboxNamespace("default"),
		withSandboxPoolRef("test-pool"),
	)

	_, err := registry.Allocate(sandbox)
	assert.Error(t, err, "Should fail when no agents match namespace and pool")
	assert.Contains(t, err.Error(), "insufficient capacity")
}

func TestInMemoryRegistry_Allocate_ZeroCapacity(t *testing.T) {
	// A-06: Agents with zero capacity have no limit
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("unlimited-agent",
		withPoolName("test-pool"),
		withCapacity(0), // Zero capacity means unlimited
	))

	// Allocate many sandboxes - should all succeed
	for i := 0; i < 100; i++ {
		dummySB := newTestSandbox("unlimited-"+string(rune('0'+i%10)))
		_, err := registry.Allocate(dummySB)
		require.NoError(t, err)
	}

	agent, _ := registry.GetAgentByID("unlimited-agent")
	assert.Equal(t, 100, agent.Allocated, "Should handle many allocations with capacity=0")

	// One more should still work
	sandbox := newTestSandbox("test-sb")
	allocatedAgent, err := registry.Allocate(sandbox)
	require.NoError(t, err)
	assert.Equal(t, AgentID("unlimited-agent"), allocatedAgent.ID)
	assert.Equal(t, 101, allocatedAgent.Allocated)
}

func TestInMemoryRegistry_Allocate_InvalidPort(t *testing.T) {
	// A-07: Invalid port numbers are rejected
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
		withPoolName("test-pool"),
	))

	sandbox := newTestSandbox("test-sb",
		withSandboxPorts(0), // Invalid port
	)

	_, err := registry.Allocate(sandbox)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid port")
}

func TestInMemoryRegistry_Allocate_LeastLoadedPreferred(t *testing.T) {
	// A-08: When image affinity doesn't apply, prefer least loaded agent
	registry := NewInMemoryRegistry()

	// Register agent-1 with limited capacity to force some allocations to agent-2
	registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
		withPoolName("test-pool"),
		withCapacity(2), // Limited to 2 allocations
	))
	registry.RegisterOrUpdate(newTestAgentInfo("agent-2",
		withPoolName("test-pool"),
		withCapacity(10),
	))

	// Allocate 3 sandboxes - first 2 go to agent-1 (fills it), 3rd goes to agent-2
	for i := 0; i < 3; i++ {
		dummySB := newTestSandbox("load-"+string(rune('0'+i)))
		_, _ = registry.Allocate(dummySB)
	}

	// Verify state
	agent1, _ := registry.GetAgentByID("agent-1")
	agent2, _ := registry.GetAgentByID("agent-2")
	require.Equal(t, 2, agent1.Allocated, "agent-1 should be full")
	require.Equal(t, 1, agent2.Allocated, "agent-2 should have 1 allocation")

	sandbox := newTestSandbox("test-sb",
		withSandboxImage("ubuntu:latest"), // Neither agent has this image
	)

	agent, err := registry.Allocate(sandbox)
	require.NoError(t, err)
	assert.Equal(t, AgentID("agent-2"), agent.ID, "Should prefer least loaded agent")
	assert.Equal(t, 2, agent.Allocated)
}

func TestInMemoryRegistry_Allocate_NamespaceMatch(t *testing.T) {
	// A-09: Allocation only considers agents in the same namespace
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
		withNamespace("default"),
		withPoolName("test-pool"),
	))
	registry.RegisterOrUpdate(newTestAgentInfo("agent-2",
		withNamespace("kube-system"),
		withPoolName("test-pool"),
	))

	sandbox := newTestSandbox("test-sb",
		withSandboxNamespace("default"),
	)

	agent, err := registry.Allocate(sandbox)
	require.NoError(t, err)
	assert.Equal(t, AgentID("agent-1"), agent.ID, "Should match namespace")
}

func TestInMemoryRegistry_Allocate_ImageAffinityOverLoad(t *testing.T) {
	// A-10: Image affinity is preferred over lower load
	registry := NewInMemoryRegistry()

	// Agent with cached image
	registry.RegisterOrUpdate(newTestAgentInfo("cached-agent",
		withPoolName("test-pool"),
		withCapacity(10),
		withImages("alpine:latest"),
	))
	// Agent without cached image - very limited capacity
	registry.RegisterOrUpdate(newTestAgentInfo("empty-agent",
		withPoolName("test-pool"),
		withCapacity(1), // Will fill after 1 allocation
		withImages("ubuntu:latest"),
	))

	// First allocation will go to cached-agent (lower ID, both have 0 allocated)
	dummySB1 := newTestSandbox("dummy-1", withSandboxImage("ubuntu:latest"))
	registry.Allocate(dummySB1) // Goes to cached-agent (both 0 allocated, tie-breaker)

	// Second allocation will go to empty-agent (still 0 allocated vs cached-agent's 1)
	dummySB2 := newTestSandbox("dummy-2", withSandboxImage("ubuntu:latest"))
	registry.Allocate(dummySB2) // Goes to empty-agent

	// Now cached-agent has 1, empty-agent has 1 (and is full at capacity=1)

	// Verify state
	cachedAgent, _ := registry.GetAgentByID("cached-agent")
	emptyAgent, _ := registry.GetAgentByID("empty-agent")
	require.Equal(t, 1, cachedAgent.Allocated, "cached-agent should have 1")
	require.Equal(t, 1, emptyAgent.Allocated, "empty-agent should be full")

	// Request with alpine image - cached-agent has image affinity
	// Score cached-agent = 1 + 0 (has image) = 1
	// Score empty-agent = full (capacity=1, allocated=1), so skipped
	sandbox := newTestSandbox("test-sb",
		withSandboxImage("alpine:latest"),
	)

	agent, err := registry.Allocate(sandbox)
	require.NoError(t, err)
	assert.Equal(t, AgentID("cached-agent"), agent.ID, "Should prefer image affinity over lower load")
	assert.Equal(t, 2, agent.Allocated)
}

// ============================================================================
// 3. Release Tests
// ============================================================================

func TestInMemoryRegistry_Release(t *testing.T) {
	// L-01: Release decrements allocation and frees ports
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
		withPoolName("test-pool"),
		withCapacity(10),
	))

	sandbox := newTestSandbox("test-sb",
		withSandboxPorts(8080, 9090),
	)

	// First allocate
	_, err := registry.Allocate(sandbox)
	require.NoError(t, err)

	// Verify allocation
	agent, _ := registry.GetAgentByID("agent-1")
	assert.Equal(t, 1, agent.Allocated)
	assert.True(t, agent.UsedPorts[8080])

	// Now release
	registry.Release("agent-1", sandbox)

	agent, ok := registry.GetAgentByID("agent-1")
	require.True(t, ok)
	assert.Equal(t, 0, agent.Allocated, "Allocated should be decremented")
	assert.False(t, agent.UsedPorts[8080], "Port 8080 should be freed")
	assert.False(t, agent.UsedPorts[9090], "Port 9090 should be freed")
}

func TestInMemoryRegistry_Release_NonExistent(t *testing.T) {
	// L-02: Release handles non-existent agents gracefully
	registry := NewInMemoryRegistry()

	sandbox := newTestSandbox("test-sb")

	// Should not panic
	registry.Release("non-existent", sandbox)

	// Registry should remain empty
	agents := registry.GetAllAgents()
	assert.Empty(t, agents)
}

func TestInMemoryRegistry_Release_WithSandboxStatus(t *testing.T) {
	// L-03: Release removes sandbox status
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
		withPoolName("test-pool"),
		withCapacity(10),
		withSandboxStatus("test-sb", api.SandboxStatus{Phase: "running"}),
	))

	sandbox := newTestSandbox("test-sb",
		withSandboxPorts(8080),
	)

	// First allocate to set ports
	_, err := registry.Allocate(sandbox)
	require.NoError(t, err)

	// Add sandbox status
	agent, _ := registry.GetAgentByID("agent-1")
	agent.SandboxStatuses["test-sb"] = api.SandboxStatus{Phase: "running"}

	// Now release
	registry.Release("agent-1", sandbox)

	agent, _ = registry.GetAgentByID("agent-1")
	_, exists := agent.SandboxStatuses["test-sb"]
	assert.False(t, exists, "Sandbox status should be removed")
}

func TestInMemoryRegistry_Release_WithPartialPortMatch(t *testing.T) {
	// L-04: Release only frees the specified ports
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
		withPoolName("test-pool"),
		withCapacity(10),
	))

	// Allocate first sandbox with ports 8080, 9090
	sb1 := newTestSandbox("sb-1", withSandboxPorts(8080, 9090))
	_, err := registry.Allocate(sb1)
	require.NoError(t, err)

	// Allocate second sandbox with port 7070
	sb2 := newTestSandbox("sb-2", withSandboxPorts(7070))
	_, err = registry.Allocate(sb2)
	require.NoError(t, err)

	// Verify all ports are in use
	agent, _ := registry.GetAgentByID("agent-1")
	assert.True(t, agent.UsedPorts[8080])
	assert.True(t, agent.UsedPorts[9090])
	assert.True(t, agent.UsedPorts[7070])
	assert.Equal(t, 2, agent.Allocated)

	// Release first sandbox
	registry.Release("agent-1", sb1)

	agent, _ = registry.GetAgentByID("agent-1")
	assert.Equal(t, 1, agent.Allocated)
	assert.False(t, agent.UsedPorts[8080])
	assert.False(t, agent.UsedPorts[9090])
	assert.True(t, agent.UsedPorts[7070], "Port 7070 should remain in use")
}

// ============================================================================
// 4. GetAllAgents Tests
// ============================================================================

func TestInMemoryRegistry_GetAllAgents(t *testing.T) {
	// G-01: Getting all agents returns correct list
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
		withPoolName("pool-a"),
	))
	registry.RegisterOrUpdate(newTestAgentInfo("agent-2",
		withPoolName("pool-b"),
	))
	registry.RegisterOrUpdate(newTestAgentInfo("agent-3",
		withPoolName("pool-a"),
	))

	agents := registry.GetAllAgents()
	assert.Len(t, agents, 3, "Should return all 3 agents")

	agentIDs := make(map[AgentID]bool)
	for _, a := range agents {
		agentIDs[a.ID] = true
	}
	assert.True(t, agentIDs["agent-1"])
	assert.True(t, agentIDs["agent-2"])
	assert.True(t, agentIDs["agent-3"])
}

func TestInMemoryRegistry_GetAllAgents_Empty(t *testing.T) {
	// G-02: Empty registry returns empty list
	registry := NewInMemoryRegistry()

	agents := registry.GetAllAgents()
	assert.Empty(t, agents)
}

func TestInMemoryRegistry_GetAllAgents_ThreadSafe(t *testing.T) {
	// G-03: GetAllAgents is thread-safe during concurrent operations
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("agent-1"))

	var wg sync.WaitGroup
	wg.Add(2)

	// Concurrent read and update
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			_ = registry.GetAllAgents()
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
				withCapacity(5 + i),
			))
		}
	}()

	wg.Wait()

	// Should not panic or deadlock
	agents := registry.GetAllAgents()
	assert.NotEmpty(t, agents)
}

// ============================================================================
// 5. GetAgentByID Tests
// ============================================================================

func TestInMemoryRegistry_GetAgentByID(t *testing.T) {
	// GB-01: Getting agent by ID works correctly
	registry := NewInMemoryRegistry()

	expectedInfo := newTestAgentInfo("agent-1",
		withPoolName("pool-a"),
		withCapacity(5),
		withImages("alpine:latest"),
	)
	registry.RegisterOrUpdate(expectedInfo)

	agent, ok := registry.GetAgentByID("agent-1")
	require.True(t, ok)
	assert.Equal(t, AgentID("agent-1"), agent.ID)
	assert.Equal(t, "pool-a", agent.PoolName)
	assert.Equal(t, 5, agent.Capacity)
	assert.Equal(t, []string{"alpine:latest"}, agent.Images)
}

func TestInMemoryRegistry_GetAgentByID_NotFound(t *testing.T) {
	// GB-02: Getting non-existent agent returns false
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("agent-1"))

	_, ok := registry.GetAgentByID("non-existent")
	assert.False(t, ok, "Should return false for non-existent agent")
}

func TestInMemoryRegistry_GetAgentByID_ThreadSafe(t *testing.T) {
	// GB-03: GetAgentByID is thread-safe
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("agent-1", withCapacity(5)))

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			_, _ = registry.GetAgentByID("agent-1")
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
				withCapacity(5 + i),
			))
		}
	}()

	wg.Wait()

	// Should not panic or deadlock
	agent, ok := registry.GetAgentByID("agent-1")
	assert.True(t, ok)
	assert.NotEqual(t, 5, agent.Capacity, "Capacity should have been updated")
}

// ============================================================================
// 6. Remove Tests
// ============================================================================

func TestInMemoryRegistry_Remove(t *testing.T) {
	// RM-01: Remove deletes agent from registry
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("agent-1"))
	registry.RegisterOrUpdate(newTestAgentInfo("agent-2"))

	// Verify both exist
	_, ok1 := registry.GetAgentByID("agent-1")
	_, ok2 := registry.GetAgentByID("agent-2")
	assert.True(t, ok1)
	assert.True(t, ok2)

	registry.Remove("agent-1")

	// Verify agent-1 is gone, agent-2 remains
	_, ok1 = registry.GetAgentByID("agent-1")
	_, ok2 = registry.GetAgentByID("agent-2")
	assert.False(t, ok1, "agent-1 should be removed")
	assert.True(t, ok2, "agent-2 should still exist")
}

func TestInMemoryRegistry_Remove_NonExistent(t *testing.T) {
	// RM-02: Removing non-existent agent is safe
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("agent-1"))

	// Should not panic
	registry.Remove("non-existent")

	// Original agent should still exist
	_, ok := registry.GetAgentByID("agent-1")
	assert.True(t, ok)
}

// ============================================================================
// 7. CleanupStaleAgents Tests
// ============================================================================

func TestInMemoryRegistry_CleanupStaleAgents(t *testing.T) {
	// C-01: Cleanup removes agents with stale heartbeats
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("fresh-agent",
		withLastHeartbeat(time.Now().Add(-30 * time.Second)),
	))
	registry.RegisterOrUpdate(newTestAgentInfo("stale-agent",
		withLastHeartbeat(time.Now().Add(-5 * time.Minute)),
	))

	timeout := 2 * time.Minute
	cleaned := registry.CleanupStaleAgents(timeout)

	assert.Equal(t, 1, cleaned, "Should clean 1 stale agent")

	agents := registry.GetAllAgents()
	assert.Len(t, agents, 1, "Should have 1 agent remaining")

	_, ok := registry.GetAgentByID("fresh-agent")
	assert.True(t, ok, "fresh-agent should remain")

	_, ok = registry.GetAgentByID("stale-agent")
	assert.False(t, ok, "stale-agent should be removed")
}

func TestInMemoryRegistry_CleanupStaleAgents_None(t *testing.T) {
	// C-02: No agents cleaned when all are fresh
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
		withLastHeartbeat(time.Now().Add(-30 * time.Second)),
	))
	registry.RegisterOrUpdate(newTestAgentInfo("agent-2",
		withLastHeartbeat(time.Now()),
	))

	timeout := 5 * time.Minute
	cleaned := registry.CleanupStaleAgents(timeout)

	assert.Equal(t, 0, cleaned)

	agents := registry.GetAllAgents()
	assert.Len(t, agents, 2)
}

func TestInMemoryRegistry_CleanupStaleAgents_All(t *testing.T) {
	// C-03: All agents cleaned when all are stale
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
		withLastHeartbeat(time.Now().Add(-10 * time.Minute)),
	))
	registry.RegisterOrUpdate(newTestAgentInfo("agent-2",
		withLastHeartbeat(time.Now().Add(-1 * time.Hour)),
	))

	timeout := 1 * time.Minute
	cleaned := registry.CleanupStaleAgents(timeout)

	assert.Equal(t, 2, cleaned)

	agents := registry.GetAllAgents()
	assert.Empty(t, agents)
}

func TestInMemoryRegistry_CleanupStaleAgents_EmptyRegistry(t *testing.T) {
	// C-04: Cleanup on empty registry is safe
	registry := NewInMemoryRegistry()

	timeout := 1 * time.Minute
	cleaned := registry.CleanupStaleAgents(timeout)

	assert.Equal(t, 0, cleaned)
}

func TestInMemoryRegistry_CleanupStaleAgents_Boundary(t *testing.T) {
	// C-05: Agents exactly at timeout boundary are cleaned
	registry := NewInMemoryRegistry()

	// Agent exactly at timeout boundary (using slightly more to be safe)
	registry.RegisterOrUpdate(newTestAgentInfo("boundary-agent",
		withLastHeartbeat(time.Now().Add(-2*time.Minute - time.Second)),
	))

	timeout := 2 * time.Minute
	cleaned := registry.CleanupStaleAgents(timeout)

	assert.Equal(t, 1, cleaned, "Agent at boundary should be cleaned")
}

// ============================================================================
// 8. Thread Safety Tests
// ============================================================================

func TestInMemoryRegistry_ConcurrentRegister(t *testing.T) {
	// T-01: Concurrent registrations are safe
	registry := NewInMemoryRegistry()
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			agentID := AgentID("agent-" + string(rune('0'+idx)))
			registry.RegisterOrUpdate(newTestAgentInfo(agentID))
		}(i)
	}

	wg.Wait()

	agents := registry.GetAllAgents()
	assert.NotEmpty(t, agents, "Should have some agents registered")
}

func TestInMemoryRegistry_ConcurrentAllocate(t *testing.T) {
	// T-02: Concurrent allocations work correctly
	registry := NewInMemoryRegistry()

	// Register agents with capacity
	for i := 0; i < 5; i++ {
		agentID := AgentID("agent-" + string(rune('0'+i)))
		registry.RegisterOrUpdate(newTestAgentInfo(agentID,
			withCapacity(10),
		))
	}

	var wg sync.WaitGroup
	successCount := 0
	var mu sync.Mutex

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sandbox := newTestSandbox("test-sb")
			_, err := registry.Allocate(sandbox)
			if err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()

	// Verify total allocations
	agents := registry.GetAllAgents()
	totalAllocated := 0
	for _, a := range agents {
		totalAllocated += a.Allocated
	}
	assert.Equal(t, successCount, totalAllocated, "All successful allocations should be counted")
}

func TestInMemoryRegistry_ConcurrentRelease(t *testing.T) {
	// T-03: Concurrent releases are safe
	registry := NewInMemoryRegistry()

	registry.RegisterOrUpdate(newTestAgentInfo("agent-1",
		withCapacity(100),
	))

	// Allocate some sandboxes
	for i := 0; i < 10; i++ {
		sandbox := newTestSandbox("sb-"+string(rune('0'+i)))
		registry.Allocate(sandbox)
	}

	var wg sync.WaitGroup

	// Concurrent releases
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sandbox := newTestSandbox("sb-" + string(rune('0'+idx)))
			registry.Release("agent-1", sandbox)
		}(i)
	}

	wg.Wait()

	agent, _ := registry.GetAgentByID("agent-1")
	assert.Equal(t, 0, agent.Allocated, "All allocations should be released")
}
