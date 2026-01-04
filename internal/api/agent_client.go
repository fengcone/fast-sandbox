package api

import (
	"net/http"
	"time"
)

// AgentClient handles HTTP communication with agents.
type AgentClient struct {
	httpClient *http.Client
}

// NewAgentClient creates a new agent client.
func NewAgentClient() *AgentClient {
	return &AgentClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SyncSandboxes sends the desired sandboxes to the agent and receives their status.
func (c *AgentClient) SyncSandboxes(agentEndpoint string, req *SandboxesRequest) error {
	return nil
}

func (c *AgentClient) GetAgentStatus(agentEndpoint string) (*AgentStatus, error) {
	return nil, nil
}
