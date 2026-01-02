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

// CreateSandbox sends a request to agent to create a sandbox.
func (c *AgentClient) CreateSandbox(agentIP string, agentPort int32, req *api.CreateSandboxRequest) (*api.CreateSandboxResponse, error) {
	if agentPort == 0 {
		agentPort = 8081
	}
	url := fmt.Sprintf("http://%s:%d/api/v1/sandbox/create", agentIP, agentPort)

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
		return nil, fmt.Errorf("create sandbox failed with status: %d", resp.StatusCode)
	}

	var result api.CreateSandboxResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// DestroySandbox sends a request to agent to destroy a sandbox.
func (c *AgentClient) DestroySandbox(agentIP string, agentPort int32, req *api.DestroySandboxRequest) (*api.DestroySandboxResponse, error) {
	if agentPort == 0 {
		agentPort = 8081
	}
	url := fmt.Sprintf("http://%s:%d/api/v1/sandbox/destroy", agentIP, agentPort)

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
		return nil, fmt.Errorf("destroy sandbox failed with status: %d", resp.StatusCode)
	}

	var result api.DestroySandboxResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}
