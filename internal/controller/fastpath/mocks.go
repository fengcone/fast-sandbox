package fastpath

import (
	"context"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentpool"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MockRegistryForTest is a mock implementation of AgentRegistry for testing.
type MockRegistryForTest struct {
	AllocateFunc  func(sb *apiv1alpha1.Sandbox) (*agentpool.AgentInfo, error)
	ReleaseFunc   func(id agentpool.AgentID, sb *apiv1alpha1.Sandbox)
	AllocatedSb   *apiv1alpha1.Sandbox
	ReleasedID    agentpool.AgentID
	ReleasedSb    *apiv1alpha1.Sandbox
	DefaultAgent  *agentpool.AgentInfo
	AllocateError error
	Agents        map[agentpool.AgentID]agentpool.AgentInfo
}

func (m *MockRegistryForTest) RegisterOrUpdate(info agentpool.AgentInfo) {
	if m.Agents == nil {
		m.Agents = make(map[agentpool.AgentID]agentpool.AgentInfo)
	}
	m.Agents[info.ID] = info
}

func (m *MockRegistryForTest) GetAllAgents() []agentpool.AgentInfo {
	result := make([]agentpool.AgentInfo, 0, len(m.Agents))
	for _, a := range m.Agents {
		result = append(result, a)
	}
	return result
}

func (m *MockRegistryForTest) GetAgentByID(id agentpool.AgentID) (agentpool.AgentInfo, bool) {
	if a, ok := m.Agents[id]; ok {
		return a, true
	}
	return agentpool.AgentInfo{}, false
}

func (m *MockRegistryForTest) Allocate(sb *apiv1alpha1.Sandbox) (*agentpool.AgentInfo, error) {
	m.AllocatedSb = sb
	if m.AllocateFunc != nil {
		return m.AllocateFunc(sb)
	}
	if m.AllocateError != nil {
		return nil, m.AllocateError
	}
	if m.DefaultAgent != nil {
		return m.DefaultAgent, nil
	}
	return &agentpool.AgentInfo{
		ID:        "test-agent",
		PodName:   "test-agent",
		PodIP:     "10.0.0.1",
		NodeName:  "test-node",
		PoolName:  "test-pool",
		Capacity:  10,
		Allocated: 0,
		LastHeartbeat: time.Now(),
	}, nil
}

func (m *MockRegistryForTest) Release(id agentpool.AgentID, sb *apiv1alpha1.Sandbox) {
	m.ReleasedID = id
	m.ReleasedSb = sb
	if m.ReleaseFunc != nil {
		m.ReleaseFunc(id, sb)
	}
}

func (m *MockRegistryForTest) Restore(ctx context.Context, c client.Reader) error {
	return nil
}

func (m *MockRegistryForTest) Remove(id agentpool.AgentID) {
	if m.Agents != nil {
		delete(m.Agents, id)
	}
}

func (m *MockRegistryForTest) CleanupStaleAgents(timeout time.Duration) int {
	return 0
}

// MockAgentClientForTest is a mock implementation of AgentAPIClient for testing.
type MockAgentClientForTest struct {
	CreateSandboxFunc  func(endpoint string, req *api.CreateSandboxRequest) (*api.CreateSandboxResponse, error)
	DeleteSandboxFunc  func(endpoint string, req *api.DeleteSandboxRequest) (*api.DeleteSandboxResponse, error)
	GetAgentStatusFunc func(ctx context.Context, endpoint string) (*api.AgentStatus, error)
	CreateCalled       bool
	DeleteCalled       bool
	LastCreateEndpoint string
	LastDeleteEndpoint string
	LastCreateReq      *api.CreateSandboxRequest
	LastDeleteReq      *api.DeleteSandboxRequest
	CreateError        error
	DeleteError        error
}

func (m *MockAgentClientForTest) CreateSandbox(endpoint string, req *api.CreateSandboxRequest) (*api.CreateSandboxResponse, error) {
	m.CreateCalled = true
	m.LastCreateEndpoint = endpoint
	m.LastCreateReq = req
	if m.CreateSandboxFunc != nil {
		return m.CreateSandboxFunc(endpoint, req)
	}
	if m.CreateError != nil {
		return nil, m.CreateError
	}
	return &api.CreateSandboxResponse{
		Success:   true,
		SandboxID: req.Sandbox.SandboxID,
		CreatedAt: time.Now().Unix(),
	}, nil
}

func (m *MockAgentClientForTest) DeleteSandbox(endpoint string, req *api.DeleteSandboxRequest) (*api.DeleteSandboxResponse, error) {
	m.DeleteCalled = true
	m.LastDeleteEndpoint = endpoint
	m.LastDeleteReq = req
	if m.DeleteSandboxFunc != nil {
		return m.DeleteSandboxFunc(endpoint, req)
	}
	if m.DeleteError != nil {
		return nil, m.DeleteError
	}
	return &api.DeleteSandboxResponse{
		Success: true,
	}, nil
}

func (m *MockAgentClientForTest) GetAgentStatus(ctx context.Context, endpoint string) (*api.AgentStatus, error) {
	if m.GetAgentStatusFunc != nil {
		return m.GetAgentStatusFunc(ctx, endpoint)
	}
	return &api.AgentStatus{
		AgentID:   "test-agent",
		NodeName:  "test-node",
		Capacity:  10,
		Allocated: 0,
	}, nil
}
