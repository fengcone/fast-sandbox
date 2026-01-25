package agentcontrol

import (
	"context"
	"sync"
	"time"

	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentpool"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Loop periodically syncs desired sandboxes with agents and updates claim status.
type Loop struct {
	Client      client.Client
	Registry    agentpool.AgentRegistry
	AgentClient *api.AgentClient
	Interval    time.Duration
}

// NewLoop creates a new AgentControlLoop with a default interval.
func NewLoop(c client.Client, reg agentpool.AgentRegistry, agentClient *api.AgentClient) *Loop {
	return &Loop{
		Client:      c,
		Registry:    reg,
		AgentClient: agentClient,
		Interval:    2 * time.Second,
	}
}

// Start runs the loop until the context is cancelled.
func (l *Loop) Start(ctx context.Context) {
	logger := klog.Background().WithName("agent-control-loop")
	ticker := time.NewTicker(l.Interval)
	defer ticker.Stop()

	syncInProgress := false
	var syncMu sync.Mutex

	for {
		select {
		case <-ctx.Done():
			logger.Info("agent control loop stopped")
			return
		case <-ticker.C:
			syncMu.Lock()
			if syncInProgress {
				syncMu.Unlock()
				logger.Info("Previous sync still in progress, skipping this tick")
				continue
			}
			syncInProgress = true
			syncMu.Unlock()

			go func() {
				defer func() {
					syncMu.Lock()
					syncInProgress = false
					syncMu.Unlock()
				}()

				start := time.Now()
				if err := l.syncOnce(ctx); err != nil {
					logger.Error(err, "agent control loop sync failed")
				}
				duration := time.Since(start)
				if duration > l.Interval {
					logger.Info("Sync took longer than interval", "duration", duration, "interval", l.Interval)
				}
			}()
		}
	}
}

const (
	perAgentTimeout   = 5 * time.Second
	staleAgentTimeout = 15 * time.Second
)

func (l *Loop) syncOnce(ctx context.Context) error {
	logger := klog.Background().WithName("agent-control-loop")

	syncCtx, cancel := context.WithTimeout(ctx, l.Interval*2)
	defer cancel()

	var podList corev1.PodList
	if err := l.Client.List(syncCtx, &podList, client.MatchingLabels{"app": "sandbox-agent"}); err != nil {
		return err
	}

	seenAgents := make(map[agentpool.AgentID]bool)

	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
			continue
		}

		agentID := agentpool.AgentID(pod.Name)
		seenAgents[agentID] = true

		agentCtx, agentCancel := context.WithTimeout(syncCtx, perAgentTimeout)
		status, err := l.AgentClient.GetAgentStatus(agentCtx, pod.Status.PodIP)
		agentCancel()

		if err != nil {
			logger.Error(err, "Failed to probe agent", "pod", pod.Name, "ip", pod.Status.PodIP)
			continue
		}

		sbStatuses := make(map[string]api.SandboxStatus)
		for _, s := range status.SandboxStatuses {
			sbStatuses[s.SandboxID] = s
		}

		info := agentpool.AgentInfo{
			ID:              agentID,
			Namespace:       pod.Namespace,
			PodName:         pod.Name,
			PodIP:           pod.Status.PodIP,
			NodeName:        pod.Spec.NodeName,
			PoolName:        pod.Labels["fast-sandbox.io/pool"],
			Capacity:        status.Capacity,
			Images:          status.Images,
			SandboxStatuses: sbStatuses,
			LastHeartbeat:   time.Now(),
		}
		l.Registry.RegisterOrUpdate(info)
	}

	allAgents := l.Registry.GetAllAgents()
	for _, a := range allAgents {
		if !seenAgents[a.ID] {
			logger.Info("Removing stale agent from registry (Pod not found)", "agent", a.ID, "pool", a.PoolName)
			l.Registry.Remove(a.ID)
		}
	}

	cleaned := l.Registry.CleanupStaleAgents(staleAgentTimeout)
	if cleaned > 0 {
		logger.Info("Cleaned up stale agents by heartbeat timeout", "count", cleaned)
	}
	return nil
}
