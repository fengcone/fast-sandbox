package api

import "time"

// RegisterRequest is sent by Agent to register itself with Controller.
type RegisterRequest struct {
	AgentID   string   `json:"agentId"`
	Namespace string   `json:"namespace"`
	PodName   string   `json:"podName"`
	PodIP     string   `json:"podIp"`
	NodeName  string   `json:"nodeName"`
	Capacity  int      `json:"capacity"`
	Images    []string `json:"images"`
}

// RegisterResponse is returned by Controller after registration.
type RegisterResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// HeartbeatRequest is sent periodically by Agent to update status.
type HeartbeatRequest struct {
	AgentID             string   `json:"agentId"`
	RunningSandboxCount int      `json:"runningSandboxCount"`
	Images              []string `json:"images,omitempty"`
	Timestamp           int64    `json:"timestamp"`
}

// HeartbeatResponse is returned by Controller.
type HeartbeatResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// CreateSandboxRequest is sent by Controller to Agent to create a sandbox.
type CreateSandboxRequest struct {
	ClaimUID  string            `json:"claimUid"`
	ClaimName string            `json:"claimName"`
	Image     string            `json:"image"`
	CPU       string            `json:"cpu,omitempty"`
	Memory    string            `json:"memory,omitempty"`
	Port      int32             `json:"port,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// CreateSandboxResponse is returned by Agent after sandbox creation.
type CreateSandboxResponse struct {
	Success   bool   `json:"success"`
	SandboxID string `json:"sandboxId,omitempty"`
	Port      int32  `json:"port,omitempty"`
	Message   string `json:"message,omitempty"`
}

// DestroySandboxRequest is sent by Controller to Agent to destroy a sandbox.
type DestroySandboxRequest struct {
	SandboxID string `json:"sandboxId"`
}

// DestroySandboxResponse is returned by Agent after sandbox destruction.
type DestroySandboxResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// AgentStatus represents the current status of an agent (internal use).
type AgentStatus struct {
	AgentID       string    `json:"agentId"`
	Capacity      int       `json:"capacity"`
	Allocated     int       `json:"allocated"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
}
