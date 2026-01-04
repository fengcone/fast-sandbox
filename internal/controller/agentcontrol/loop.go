package agentcontrol

import (
	"context"
	"fmt"
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
		Interval:    5 * time.Second,
	}
}

// Start runs the loop until the context is cancelled.
func (l *Loop) Start(ctx context.Context) {
	logger := ctrl.Log.WithName("agent-control-loop")
	ticker := time.NewTicker(l.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("agent control loop stopped")
			return
		case <-ticker.C:
			if err := l.syncOnce(ctx); err != nil {
				logger.Error(err, "agent control loop sync failed")
			}
		}
	}
}

func (l *Loop) syncOnce(ctx context.Context) error {
	logger := ctrl.Log.WithName("agent-control-loop")

	// 1. List all Agent Pods
	var podList corev1.PodList
	// 暂时只查找 default namespace 下带有 app=sandbox-agent 标签的 Pod
	// 在生产环境中，这应该通过 SandboxPool 的 OwnerReference 或更精确的 Selector 来查找
	if err := l.Client.List(ctx, &podList, client.MatchingLabels{"app": "sandbox-agent"}); err != nil {
		return err
	}

	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
			continue
		}

		// 2. Probe Agent
		// 假设 Agent 端口固定为 8081，可以通过 Pod Annotation 或 Env 配置
		endpoint := fmt.Sprintf("%s:8081", pod.Status.PodIP)
		status, err := l.AgentClient.GetAgentStatus(endpoint)
		if err != nil {
			logger.Error(err, "Failed to probe agent", "pod", pod.Name, "ip", pod.Status.PodIP)
			// TODO: 如果连续失败，考虑从 Registry 中移除
			continue
		}

		// 3. Update Registry
		sbStatuses := make(map[string]api.SandboxStatus)
		for _, s := range status.SandboxStatuses {
			sbStatuses[s.SandboxID] = s
		}

		info := agentpool.AgentInfo{
			ID:              agentpool.AgentID(pod.Name),
			Namespace:       pod.Namespace,
			PodName:         pod.Name,
			PodIP:           pod.Status.PodIP,
			NodeName:        pod.Spec.NodeName,
			PoolName:        pod.Labels["fast-sandbox.io/pool"],
			Capacity:        status.Capacity,
			Allocated:       status.Allocated,
			Images:          status.Images,
			SandboxStatuses: sbStatuses,
			LastHeartbeat:   time.Now(),
		}
		l.Registry.RegisterOrUpdate(info)
	}
	return nil
}
