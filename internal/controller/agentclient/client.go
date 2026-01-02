package agentclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"fast-sandbox/internal/api"
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
func (c *AgentClient) SyncSandboxes(agentIP string, agentPort int32, req *api.SandboxesRequest) (*api.SandboxesResponse, error) {
	if agentPort == 0 {
		agentPort = 8081
	}
	// for current test
	agentIP = "localhost"
	url := fmt.Sprintf("http://%s:%d/api/v1/agent/sandboxes", agentIP, agentPort)

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sync sandboxes failed with status: %d", resp.StatusCode)
	}

	var result api.SandboxesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}
