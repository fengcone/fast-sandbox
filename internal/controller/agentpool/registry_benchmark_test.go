package agentpool

import (
	"fmt"
	"testing"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func BenchmarkRegistryAllocate(b *testing.B) {
	registry := NewInMemoryRegistry()

	// Populate with 100 agents
	for i := 0; i < 100; i++ {
		registry.RegisterOrUpdate(AgentInfo{
			ID:       AgentID(fmt.Sprintf("agent-%d", i)),
			PodIP:    fmt.Sprintf("10.0.0.%d", i),
			Capacity: 10,
			Images:   []string{"alpine", "nginx", "redis"},
		})
	}

	sb := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "test-sb", Namespace: "default"},
		Spec: apiv1alpha1.SandboxSpec{
			Image:   "alpine",
			PoolRef: "default-pool",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.Allocate(sb)
	}
}

func BenchmarkRegistryAllocateWithPorts(b *testing.B) {
	registry := NewInMemoryRegistry()

	// Populate with 100 agents
	for i := 0; i < 100; i++ {
		registry.RegisterOrUpdate(AgentInfo{
			ID:       AgentID(fmt.Sprintf("agent-%d", i)),
			PodIP:    fmt.Sprintf("10.0.0.%d", i),
			Capacity: 10,
			Images:   []string{"alpine", "nginx", "redis"},
		})
	}

	sb := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "test-sb", Namespace: "default"},
		Spec: apiv1alpha1.SandboxSpec{
			Image:        "alpine",
			PoolRef:      "default-pool",
			ExposedPorts: []int32{8080, 9090},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.Allocate(sb)
	}
}

func BenchmarkRegistryAllocateNoImageMatch(b *testing.B) {
	registry := NewInMemoryRegistry()

	// Populate with 100 agents
	for i := 0; i < 100; i++ {
		registry.RegisterOrUpdate(AgentInfo{
			ID:       AgentID(fmt.Sprintf("agent-%d", i)),
			PodIP:    fmt.Sprintf("10.0.0.%d", i),
			Capacity: 10,
			Images:   []string{"alpine", "nginx", "redis"},
		})
	}

	sb := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "test-sb", Namespace: "default"},
		Spec: apiv1alpha1.SandboxSpec{
			Image:   "ubuntu", // Not in agent image list
			PoolRef: "default-pool",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.Allocate(sb)
	}
}

func BenchmarkRegistryAllocateLargePool(b *testing.B) {
	registry := NewInMemoryRegistry()

	// Populate with 1000 agents (stress test)
	for i := 0; i < 1000; i++ {
		registry.RegisterOrUpdate(AgentInfo{
			ID:       AgentID(fmt.Sprintf("agent-%d", i)),
			PodIP:    fmt.Sprintf("10.0.0.%d", i),
			Capacity: 10,
			Images:   []string{"alpine", "nginx", "redis"},
		})
	}

	sb := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "test-sb", Namespace: "default"},
		Spec: apiv1alpha1.SandboxSpec{
			Image:   "alpine",
			PoolRef: "default-pool",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.Allocate(sb)
	}
}

func BenchmarkRegistryRegisterOrUpdate(b *testing.B) {
	registry := NewInMemoryRegistry()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.RegisterOrUpdate(AgentInfo{
			ID:       AgentID(fmt.Sprintf("agent-%d", i%1000)),
			PodIP:    fmt.Sprintf("10.0.0.%d", i%1000),
			Capacity: 10,
			Images:   []string{"alpine", "nginx", "redis"},
		})
	}
}

func BenchmarkRegistryGetAllAgents(b *testing.B) {
	registry := NewInMemoryRegistry()

	// Populate with 100 agents
	for i := 0; i < 100; i++ {
		registry.RegisterOrUpdate(AgentInfo{
			ID:       AgentID(fmt.Sprintf("agent-%d", i)),
			PodIP:    fmt.Sprintf("10.0.0.%d", i),
			Capacity: 10,
			Images:   []string{"alpine", "nginx", "redis"},
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.GetAllAgents()
	}
}

func BenchmarkRegistryGetAllAgentsLargePool(b *testing.B) {
	registry := NewInMemoryRegistry()

	// Populate with 1000 agents
	for i := 0; i < 1000; i++ {
		registry.RegisterOrUpdate(AgentInfo{
			ID:       AgentID(fmt.Sprintf("agent-%d", i)),
			PodIP:    fmt.Sprintf("10.0.0.%d", i),
			Capacity: 10,
			Images:   []string{"alpine", "nginx", "redis"},
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.GetAllAgents()
	}
}

func BenchmarkRegistryGetAgentByID(b *testing.B) {
	registry := NewInMemoryRegistry()

	// Populate with 100 agents
	for i := 0; i < 100; i++ {
		registry.RegisterOrUpdate(AgentInfo{
			ID:       AgentID(fmt.Sprintf("agent-%d", i)),
			PodIP:    fmt.Sprintf("10.0.0.%d", i),
			Capacity: 10,
			Images:   []string{"alpine", "nginx", "redis"},
		})
	}

	targetID := AgentID("agent-50")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.GetAgentByID(targetID)
	}
}

func BenchmarkRegistryRelease(b *testing.B) {
	registry := NewInMemoryRegistry()

	// Populate with 100 agents
	for i := 0; i < 100; i++ {
		registry.RegisterOrUpdate(AgentInfo{
			ID:       AgentID(fmt.Sprintf("agent-%d", i)),
			PodIP:    fmt.Sprintf("10.0.0.%d", i),
			Capacity: 10,
			Images:   []string{"alpine", "nginx", "redis"},
		})
	}

	sb := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "test-sb", Namespace: "default"},
		Spec: apiv1alpha1.SandboxSpec{
			Image:        "alpine",
			PoolRef:      "default-pool",
			ExposedPorts: []int32{8080},
		},
	}
	agentID := AgentID("agent-0")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.Release(agentID, sb)
	}
}

func BenchmarkRegistryCleanupStaleAgents(b *testing.B) {
	registry := NewInMemoryRegistry()

	// Populate with 100 agents
	for i := 0; i < 100; i++ {
		registry.RegisterOrUpdate(AgentInfo{
			ID:            AgentID(fmt.Sprintf("agent-%d", i)),
			PodIP:         fmt.Sprintf("10.0.0.%d", i),
			Capacity:      10,
			Images:        []string{"alpine", "nginx", "redis"},
			LastHeartbeat: time.Now().Add(-10 * time.Minute), // All stale
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.CleanupStaleAgents(5 * time.Minute)
	}
}

// BenchmarkParallelAllocate tests concurrent allocation performance
func BenchmarkParallelAllocate(b *testing.B) {
	registry := NewInMemoryRegistry()

	// Populate with 100 agents
	for i := 0; i < 100; i++ {
		registry.RegisterOrUpdate(AgentInfo{
			ID:       AgentID(fmt.Sprintf("agent-%d", i)),
			PodIP:    fmt.Sprintf("10.0.0.%d", i),
			Capacity: 100, // Large capacity for parallel allocations
			Images:   []string{"alpine", "nginx", "redis"},
		})
	}

	sb := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "test-sb", Namespace: "default"},
		Spec: apiv1alpha1.SandboxSpec{
			Image:   "alpine",
			PoolRef: "default-pool",
		},
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			sb.Name = fmt.Sprintf("test-sb-%d", i)
			registry.Allocate(sb)
			i++
		}
	})
}
