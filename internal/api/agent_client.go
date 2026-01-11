package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	// defaultAgentTimeout is the default timeout for agent API calls
	defaultAgentTimeout = 5 * time.Second
)

// AgentClient handles HTTP communication with agents.
type AgentClient struct {
	httpClient *http.Client
	timeout    time.Duration
}

// NewAgentClient creates a new agent client.
func NewAgentClient() *AgentClient {
	return &AgentClient{
		httpClient: &http.Client{
			Timeout: defaultAgentTimeout,
		},
		timeout: defaultAgentTimeout,
	}
}

// SetTimeout sets the timeout for agent API calls.
func (c *AgentClient) SetTimeout(timeout time.Duration) {
	c.timeout = timeout
	c.httpClient.Timeout = timeout
}

// SyncSandboxes sends the desired sandboxes to the agent.
func (c *AgentClient) SyncSandboxes(agentEndpoint string, req *SandboxesRequest) error {
	url := fmt.Sprintf("http://%s/api/v1/agent/sync", agentEndpoint)

	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sync failed with status: %d", resp.StatusCode)
	}

	return nil
}

// GetAgentStatus fetches the current status of an agent.
// Deprecated: Use GetAgentStatusWithContext for better timeout control.
func (c *AgentClient) GetAgentStatus(agentEndpoint string) (*AgentStatus, error) {
	return c.GetAgentStatusWithContext(context.Background(), agentEndpoint)
}

// GetAgentStatusWithContext fetches the current status of an agent with context support.
func (c *AgentClient) GetAgentStatusWithContext(ctx context.Context, agentEndpoint string) (*AgentStatus, error) {
	// Apply timeout if not already set in context
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	url := fmt.Sprintf("http://%s/api/v1/agent/status", agentEndpoint)

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