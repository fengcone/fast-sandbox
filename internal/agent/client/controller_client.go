package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"fast-sandbox/internal/api"
)

// ControllerClient handles communication with the controller.
type ControllerClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewControllerClient creates a new controller client.
func NewControllerClient(baseURL string) *ControllerClient {
	return &ControllerClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Register registers the agent with the controller.
func (c *ControllerClient) Register(req *api.RegisterRequest) (*api.RegisterResponse, error) {
	url := fmt.Sprintf("%s/api/v1/agent/register", c.baseURL)

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
		return nil, fmt.Errorf("register failed with status: %d", resp.StatusCode)
	}

	var result api.RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// Heartbeat sends a heartbeat to the controller.
func (c *ControllerClient) Heartbeat(req *api.HeartbeatRequest) (*api.HeartbeatResponse, error) {
	url := fmt.Sprintf("%s/api/v1/agent/heartbeat", c.baseURL)

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
		return nil, fmt.Errorf("heartbeat failed with status: %d", resp.StatusCode)
	}

	var result api.HeartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}
