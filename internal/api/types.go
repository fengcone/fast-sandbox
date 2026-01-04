package api

// SandboxDesired describes the desired state of a sandbox on an agent.
type SandboxDesired struct {
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
	AgentID   string           `json:"agentId"`
	Sandboxes []SandboxDesired `json:"sandboxes"`
}

// AgentStatus represents the current status of an agent (internal use).
type AgentStatus struct {
	AgentID   string          `json:"agentId"`
	Capacity  int             `json:"capacity"`
	Allocated int             `json:"allocated"`
	Images    []string        `json:"images"`
	Sandboxes []SandboxStatus `json:"sandboxes"`
}
