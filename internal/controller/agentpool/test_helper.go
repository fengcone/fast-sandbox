package agentpool

import "time"

// InjectTestAgent injects a test agent for debugging purposes.
func InjectTestAgent(registry AgentRegistry) {
	registry.RegisterOrUpdate(AgentInfo{
		ID:            "test-agent-1",
		Namespace:     "default",
		PodName:       "test-agent-pod-1",
		PodIP:         "10.244.0.100",
		NodeName:      "kind-fast-sandbox-control-plane",
		Capacity:      10,
		Allocated:     0,
		Images:        []string{"nginx:latest", "redis:latest", "ubuntu:22.04"},
		LastHeartbeat: time.Now(),
	})
}
