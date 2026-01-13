package api

// ConsistencyMode defines the consistency mode for sandbox creation.
type ConsistencyMode string

const (
	// ConsistencyModeFast creates sandbox on agent first, then writes CRD asynchronously.
	// Lowest latency, but CRD write failure may cause running sandbox to be cleaned up.
	ConsistencyModeFast ConsistencyMode = "fast"

	// ConsistencyModeStrong writes CRD first, then creates sandbox on agent.
	// Higher latency, but guarantees strong consistency.
	ConsistencyModeStrong ConsistencyMode = "strong"
)

// SandboxSpec describes the desired state of a sandbox on an agent.
type SandboxSpec struct {
	SandboxID string `json:"sandboxId"`
	// sandbox cr uid and name
	ClaimUID  string            `json:"claimUid"`
	ClaimName string            `json:"claimName"`
	Image     string            `json:"image"`
	CPU       string            `json:"cpu,omitempty"`
	Memory    string            `json:"memory,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// SandboxStatus represents the observed state of a sandbox on an agent.
type SandboxStatus struct {
	SandboxID string `json:"sandboxId"`
	ClaimUID  string `json:"claimUid"`
	Phase     string `json:"phase"`
	Message   string `json:"message,omitempty"`
	CreatedAt int64  `json:"createdAt"` // Unix timestamp for orphan cleanup
}

// CreateSandboxRequest is sent to create a single sandbox on an agent.
type CreateSandboxRequest struct {
	Sandbox SandboxSpec `json:"sandbox"`
}

// CreateSandboxResponse is returned after creating a sandbox.
type CreateSandboxResponse struct {
	Success   bool   `json:"success"`
	Message   string `json:"message,omitempty"`
	SandboxID string `json:"sandboxId"`
	CreatedAt int64  `json:"createdAt"` // Unix timestamp when sandbox was created
}

// DeleteSandboxRequest is sent to delete a single sandbox from an agent.
type DeleteSandboxRequest struct {
	SandboxID string `json:"sandboxId"`
}

// DeleteSandboxResponse is returned after deleting a sandbox.
type DeleteSandboxResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// AgentStatus represents the current status of an agent (internal use).
type AgentStatus struct {
	AgentID string `json:"agentId"`
	// contained的实现是k8s 的node，其他实现如果不能共享image，那么应当将pod name 作为 nodeName ，以便让Controller 进行调度
	NodeName        string          `json:"nodeName"`
	Capacity        int             `json:"capacity"`
	Allocated       int             `json:"allocated"`
	Images          []string        `json:"images"`
	SandboxStatuses []SandboxStatus `json:"sandboxStatuses"`
}
