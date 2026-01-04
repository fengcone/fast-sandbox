package api

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
}

// SandboxesRequest is sent by Controller to Agent with desired sandboxes.
type SandboxesRequest struct {
	AgentID      string        `json:"agentId"`
	SandboxSpecs []SandboxSpec `json:"sandboxSpecs"`
}

// AgentStatus represents the current status of an agent (internal use).
type AgentStatus struct {
	AgentID      string        `json:"agentId"`
	SandboxSpecs []SandboxSpec `json:"sandboxSpecs"`
	// contained的实现是k8s 的node，其他实现如果不能共享image，那么应当将pod name 作为 nodeName ，以便让Controller 进行调度
	NodeName       string          `json:"nodeName"`
	Capacity       int             `json:"capacity"`
	Allocated      int             `json:"allocated"`
	Images         []string        `json:"images"`
	SandboxStatuses []SandboxStatus `json:"sandboxStatuses"`
}
