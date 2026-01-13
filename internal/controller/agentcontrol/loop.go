package agentcontrol

import (
	"context"
	"fmt"
	"sync"
	"time"

	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentpool"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
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
	logger := ctrl.Log.WithName("agent-control-loop")
	ticker := time.NewTicker(l.Interval)
	defer ticker.Stop()

	// 用于检测同步是否正在进行
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

			// 在 goroutine 中执行 sync，防止阻塞主循环
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
	// perAgentTimeout 是单个 Agent 探测的超时时间
	perAgentTimeout = 2 * time.Second
	// staleAgentTimeout 是 Agent 心跳超时时间，超过此时间未更新的 Agent 会被清理
	staleAgentTimeout = 5 * time.Minute
)

func (l *Loop) syncOnce(ctx context.Context) error {
	logger := ctrl.Log.WithName("agent-control-loop")

	// 设置整体同步超时，防止单个同步周期过长
	syncCtx, cancel := context.WithTimeout(ctx, l.Interval*2)
	defer cancel()

	// 1. List all Agent Pods
	var podList corev1.PodList
	if err := l.Client.List(syncCtx, &podList, client.MatchingLabels{"app": "sandbox-agent"}); err != nil {
		return err
	}

	seenAgents := make(map[agentpool.AgentID]bool)

	// 使用 errgroup 或 WaitGroup 可以并发探测，但为了保持原有行为，我们顺序探测
	// 但每个 agent 探测都有独立的超时
	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
			continue
		}

		agentID := agentpool.AgentID(pod.Name)
		seenAgents[agentID] = true

		// 2. Probe Agent with per-agent timeout
		endpoint := fmt.Sprintf("%s:8081", pod.Status.PodIP)

		agentCtx, agentCancel := context.WithTimeout(syncCtx, perAgentTimeout)
		status, err := l.AgentClient.GetAgentStatusWithContext(agentCtx, endpoint)
		agentCancel()

		if err != nil {
			logger.Error(err, "Failed to probe agent", "pod", pod.Name, "ip", pod.Status.PodIP)
			continue
		}

		// 3. Update Registry (Keep existing Allocated count)
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

	// 4. Cleanup stale agents
	allAgents := l.Registry.GetAllAgents()
	for _, a := range allAgents {
		if !seenAgents[a.ID] {
			logger.Info("Removing stale agent from registry", "agent", a.ID)
			l.Registry.Remove(a.ID)
		}
	}

	// 5. 基于时间清理长期未更新的 Agent（防止内存泄漏）
	// 这是额外的安全网，捕获那些 Pod 仍存在但 Agent 宕机的情况
	cleaned := l.Registry.CleanupStaleAgents(staleAgentTimeout)
	if cleaned > 0 {
		logger.Info("Cleaned up stale agents by heartbeat timeout", "count", cleaned)
	}

	return nil
}
