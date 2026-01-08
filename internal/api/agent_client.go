package api

import (
	"bytes"
	"encoding/json"
	"fmt"
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
			Timeout: 60 * time.Second,
		},
	}
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

func (c *AgentClient) GetAgentStatus(agentEndpoint string) (*AgentStatus, error) {
	url := fmt.Sprintf("http://%s/api/v1/agent/status", agentEndpoint)

	resp, err := c.httpClient.Get(url)
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