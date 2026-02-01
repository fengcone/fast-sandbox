package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"k8s.io/klog/v2"
)

// AgentAPIClient defines the interface for communicating with sandbox agents.
// This allows both the real HTTP client and mocks to be used interchangeably.
type AgentAPIClient interface {
	CreateSandbox(agentIP string, req *CreateSandboxRequest) (*CreateSandboxResponse, error)
	DeleteSandbox(agentIP string, req *DeleteSandboxRequest) (*DeleteSandboxResponse, error)
	GetAgentStatus(ctx context.Context, agentIP string) (*AgentStatus, error)
}

const (
	// defaultAgentTimeout is the default timeout for agent API calls
	defaultAgentTimeout = 5 * time.Second
)

// AgentClient handles HTTP communication with agents.
type AgentClient struct {
	httpClient *http.Client
	timeout    time.Duration
	agentPort  int
}

// NewAgentClient creates a new agent client.
func NewAgentClient(agentPort int) *AgentClient {
	return &AgentClient{
		httpClient: &http.Client{
			Timeout: defaultAgentTimeout,
		},
		timeout:   defaultAgentTimeout,
		agentPort: agentPort,
	}
}

// SetTimeout sets the timeout for agent API calls.
func (c *AgentClient) SetTimeout(timeout time.Duration) {
	c.timeout = timeout
	c.httpClient.Timeout = timeout
}

// CreateSandbox sends a create sandbox request to the agent.
func (c *AgentClient) CreateSandbox(agentIP string, req *CreateSandboxRequest) (*CreateSandboxResponse, error) {
	start := time.Now()
	defer func() {
		duration := time.Since(start)
		klog.InfoS("Agent CreateSandbox RPC",
			"endpoint", agentIP,
			"sandboxID", req.Sandbox.SandboxID,
			"duration_ms", duration.Milliseconds())
	}()

	if req.Sandbox.SandboxID == "" {
		return nil, errors.New("sandboxID is required")
	}

	url := fmt.Sprintf("http://%s:%d/api/v1/agent/create", agentIP, c.agentPort)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var createResp CreateSandboxResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return &createResp, fmt.Errorf("create failed with status: %d, message: %s", resp.StatusCode, createResp.Message)
	}

	return &createResp, nil
}

// DeleteSandbox sends a delete sandbox request to the agent.
func (c *AgentClient) DeleteSandbox(agentIP string, req *DeleteSandboxRequest) (*DeleteSandboxResponse, error) {
	start := time.Now()
	defer func() {
		duration := time.Since(start)
		klog.InfoS("Agent DeleteSandbox RPC",
			"endpoint", agentIP,
			"sandboxID", req.SandboxID,
			"duration_ms", duration.Milliseconds())
	}()

	url := fmt.Sprintf("http://%s:%d/api/v1/agent/delete", agentIP, c.agentPort)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var deleteResp DeleteSandboxResponse
	if err := json.NewDecoder(resp.Body).Decode(&deleteResp); err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return &deleteResp, fmt.Errorf("delete failed with status: %d, message: %s", resp.StatusCode, deleteResp.Message)
	}

	return &deleteResp, nil
}

// GetAgentStatus fetches the current status of an agent with context support.
func (c *AgentClient) GetAgentStatus(ctx context.Context, agentIP string) (*AgentStatus, error) {
	// Apply timeout if not already set in context
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	url := fmt.Sprintf("http://%s:%d/api/v1/agent/status", agentIP, c.agentPort)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get status failed with status: %d", resp.StatusCode)
	}

	var status AgentStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}

	return &status, nil
}
