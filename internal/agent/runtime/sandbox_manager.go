package runtime

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"fast-sandbox/internal/api"
)

type SandboxManager struct {
	mu       sync.RWMutex
	runtime  Runtime
	capacity int
	// sandboxes  sandboxID -> metadata
	sandboxes map[string]*SandboxMetadata
}

func NewSandboxManager(runtime Runtime) *SandboxManager {
	capVal := 5
	if capStr := os.Getenv("AGENT_CAPACITY"); capStr != "" {
		if v, err := strconv.Atoi(capStr); err == nil {
			capVal = v
		}
	}
	return &SandboxManager{
		runtime:   runtime,
		capacity:  capVal,
		sandboxes: make(map[string]*SandboxMetadata),
	}
}

func (m *SandboxManager) CreateSandbox(ctx context.Context, spec *api.SandboxSpec) (*api.CreateSandboxResponse, error) {
	m.mu.RLock()
	_, exists := m.sandboxes[spec.SandboxID]
	m.mu.RUnlock()
	if exists {
		log.Printf("Sandbox %s already exists in cache, returning success (idempotent)", spec.SandboxID)
		return &api.CreateSandboxResponse{
			Success:   true,
			SandboxID: spec.SandboxID,
		}, nil
	}
	createdAt := time.Now().Unix()
	metadata, err := m.runtime.CreateSandbox(ctx, spec)
	if err != nil {
		log.Printf("Failed to create sandbox %s: %v", spec.SandboxID, err)
		go m.asyncDelete(spec.SandboxID)
		return &api.CreateSandboxResponse{
			Success: false,
			Message: fmt.Sprintf("create failed: %v", err),
		}, err
	}
	m.mu.Lock()
	metadata.Phase = "running"
	m.sandboxes[spec.SandboxID] = metadata
	m.mu.Unlock()
	log.Printf("Created sandbox %s (image: %s)", spec.SandboxID, spec.Image)
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
		log.Printf("Sandbox %s is already terminating, returning success (idempotent)", sandboxID)
		return &api.DeleteSandboxResponse{
			Success: true,
		}, nil
	}
	sandbox.Phase = "terminating"
	m.mu.Unlock()
	go m.asyncDelete(sandboxID)
	log.Printf("Sandbox %s marked for deletion (async graceful shutdown)", sandboxID)
	return &api.DeleteSandboxResponse{
		Success: true,
	}, nil
}

func (m *SandboxManager) asyncDelete(sandboxID string) {
	const gracefulTimeout = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), gracefulTimeout+5*time.Second)
	defer cancel()
	err := m.runtime.DeleteSandbox(ctx, sandboxID)
	log.Printf("Sandbox %s deletion completed, err: %v", sandboxID, err)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sandboxes, sandboxID)
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
	snapshots := make(map[string]*SandboxMetadata)
	for id, meta := range m.sandboxes {
		snapshots[id] = meta
	}
	m.mu.RUnlock()
	result := make([]api.SandboxStatus, 0, len(snapshots))
	for sandboxID, meta := range snapshots {
		runtimeStatus, _ := m.runtime.GetSandboxStatus(ctx, sandboxID)
		result = append(result, api.SandboxStatus{
			SandboxID: sandboxID,
			ClaimUID:  meta.ClaimUID,
			Phase:     meta.Phase,
			Message:   runtimeStatus,
			CreatedAt: meta.CreatedAt,
		})
	}
	return result
}

func (m *SandboxManager) Close() error {
	return m.runtime.Close()
}
