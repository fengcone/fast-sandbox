package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	"fast-sandbox/internal/api"

	"k8s.io/klog/v2"
)

type SandboxManager struct {
	mu       sync.RWMutex
	runtime  Runtime
	capacity int
	// sandboxes  sandboxID -> metadata
	sandboxes map[string]*SandboxMetadata
	// terminatedSandboxes  sandboxID -> deletion time (for Controller confirmation)
	terminatedSandboxes map[string]int64
	// creating  sandboxID -> channel for tracking ongoing creations
	creating map[string]chan struct{}
}

func NewSandboxManager(runtime Runtime) *SandboxManager {
	capVal := 5
	if capStr := os.Getenv("AGENT_CAPACITY"); capStr != "" {
		if v, err := strconv.Atoi(capStr); err == nil {
			capVal = v
		}
	}
	return &SandboxManager{
		runtime:             runtime,
		capacity:            capVal,
		sandboxes:           make(map[string]*SandboxMetadata),
		terminatedSandboxes: make(map[string]int64),
		creating:            make(map[string]chan struct{}),
	}
}

func (m *SandboxManager) CreateSandbox(ctx context.Context, spec *api.SandboxSpec) (*api.CreateSandboxResponse, error) {
	m.mu.RLock()
	_, exists := m.sandboxes[spec.SandboxID]
	m.mu.RUnlock()
	if exists {
		klog.InfoS("Sandbox already exists in cache, returning success (idempotent)", "sandbox", spec.SandboxID)
		return &api.CreateSandboxResponse{
			Success:   true,
			SandboxID: spec.SandboxID,
		}, nil
	}

	// Check if sandbox is currently being created
	m.mu.Lock()
	createCh, creating := m.creating[spec.SandboxID]
	if creating {
		// Another request is creating this sandbox, wait for it to complete
		m.mu.Unlock()
		klog.InfoS("Sandbox creation already in progress, waiting for completion", "sandbox", spec.SandboxID)
		select {
		case <-createCh:
			// Creation completed, check if it succeeded
			m.mu.RLock()
			_, exists := m.sandboxes[spec.SandboxID]
			m.mu.RUnlock()
			if exists {
				return &api.CreateSandboxResponse{
					Success:   true,
					SandboxID: spec.SandboxID,
				}, nil
			}
			return &api.CreateSandboxResponse{
				Success: false,
				Message: "sandbox creation failed",
			}, fmt.Errorf("sandbox creation failed")
		case <-ctx.Done():
			return &api.CreateSandboxResponse{
				Success: false,
				Message: "context cancelled while waiting for creation",
			}, ctx.Err()
		case <-time.After(30 * time.Second):
			return &api.CreateSandboxResponse{
				Success: false,
				Message: "timeout waiting for sandbox creation",
			}, fmt.Errorf("timeout waiting for sandbox creation")
		}
	}

	// Mark this sandbox as being created
	createCh = make(chan struct{})
	m.creating[spec.SandboxID] = createCh
	m.mu.Unlock()

	// Ensure we clean up the creating map when done
	defer func() {
		m.mu.Lock()
		close(createCh)
		delete(m.creating, spec.SandboxID)
		m.mu.Unlock()
	}()

	createdAt := time.Now().Unix()
	metadata, err := m.runtime.CreateSandbox(ctx, spec)
	if err != nil {
		klog.ErrorS(err, "Failed to create sandbox", "sandbox", spec.SandboxID)
		// Don't call asyncDelete here - the runtime cleans up on failure
		// and calling asyncDelete here causes race conditions with duplicate requests
		return &api.CreateSandboxResponse{
			Success: false,
			Message: fmt.Sprintf("create failed: %v", err),
		}, err
	}
	m.mu.Lock()
	metadata.Phase = "running"
	m.sandboxes[spec.SandboxID] = metadata
	m.mu.Unlock()
	klog.InfoS("Created sandbox", "sandbox", spec.SandboxID, "image", spec.Image)
	return &api.CreateSandboxResponse{
		Success:   true,
		SandboxID: spec.SandboxID,
		CreatedAt: createdAt,
	}, nil
}

func (m *SandboxManager) DeleteSandbox(sandboxID string) (*api.DeleteSandboxResponse, error) {
	m.mu.Lock()
	sandbox, ok := m.sandboxes[sandboxID]
	if ok && sandbox.Phase == "terminating" {
		m.mu.Unlock()
		klog.InfoS("Sandbox is already terminating, returning success (idempotent)", "sandbox", sandboxID)
		return &api.DeleteSandboxResponse{
			Success: true,
		}, nil
	}
	sandbox.Phase = "terminating"
	m.mu.Unlock()
	go m.asyncDelete(sandboxID)
	klog.InfoS("Sandbox marked for deletion (async graceful shutdown)", "sandbox", sandboxID)
	return &api.DeleteSandboxResponse{
		Success: true,
	}, nil
}

func (m *SandboxManager) asyncDelete(sandboxID string) {
	const gracefulTimeout = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), gracefulTimeout+5*time.Second)
	defer cancel()
	err := m.runtime.DeleteSandbox(ctx, sandboxID)
	klog.InfoS("Sandbox deletion completed", "sandbox", sandboxID, "err", err)
	m.mu.Lock()
	defer m.mu.Unlock()
	// Move to terminated sandboxes for Controller confirmation
	delete(m.sandboxes, sandboxID)
	m.terminatedSandboxes[sandboxID] = time.Now().Unix()
}

func (m *SandboxManager) GetLogs(ctx context.Context, sandboxID string, follow bool, w io.Writer) error {
	return m.runtime.GetSandboxLogs(ctx, sandboxID, follow, w)
}
func (m *SandboxManager) ListImages(ctx context.Context) ([]string, error) {
	return m.runtime.ListImages(ctx)
}

func (m *SandboxManager) GetCapacity() int {
	return m.capacity
}

func (m *SandboxManager) GetSandboxStatuses(ctx context.Context) []api.SandboxStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]api.SandboxStatus, 0)

	// Add active sandboxes
	for sandboxID, meta := range m.sandboxes {
		runtimeStatus, _ := m.runtime.GetSandboxStatus(ctx, sandboxID)
		result = append(result, api.SandboxStatus{
			SandboxID: sandboxID,
			ClaimUID:  meta.ClaimUID,
			Phase:     meta.Phase,
			Message:   runtimeStatus,
			CreatedAt: meta.CreatedAt,
		})
	}

	// Add terminated sandboxes (for Controller confirmation)
	for sandboxID, deletedAt := range m.terminatedSandboxes {
		result = append(result, api.SandboxStatus{
			SandboxID: sandboxID,
			Phase:     "terminated",
			Message:   "",
			CreatedAt: deletedAt,
		})
	}

	return result
}

func (m *SandboxManager) Close() error {
	return m.runtime.Close()
}
