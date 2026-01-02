package agentcontrol

import (
	"context"
	"fmt"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentclient"
	"fast-sandbox/internal/controller/agentpool"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Loop periodically syncs desired sandboxes with agents and updates claim status.
type Loop struct {
	Client      client.Client
	Registry    agentpool.AgentRegistry
	AgentClient *agentclient.AgentClient
	Interval    time.Duration
}

// NewLoop creates a new AgentControlLoop with a default interval.
func NewLoop(c client.Client, reg agentpool.AgentRegistry, agentClient *agentclient.AgentClient) *Loop {
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
	logger := log.FromContext(ctx)

	// List all SandboxClaims
	var claimList apiv1alpha1.SandboxClaimList
	if err := l.Client.List(ctx, &claimList); err != nil {
		return fmt.Errorf("list SandboxClaims: %w", err)
	}

	// Build index by ClaimUID
	claimsByUID := make(map[string]*apiv1alpha1.SandboxClaim)
	for i := range claimList.Items {
		c := &claimList.Items[i]
		claimsByUID[string(c.UID)] = c
	}

	// Build desired sandboxes per agent PodName
	desiredByAgent := make(map[string][]api.SandboxDesired)
	for i := range claimList.Items {
		c := &claimList.Items[i]

		if c.Status.AssignedAgentPod == "" {
			continue
		}
		// 只对 Scheduling/Running 的 Claim 下发期望
		if c.Status.Phase != "Scheduling" && c.Status.Phase != "Running" {
			continue
		}

		agentPodName := c.Status.AssignedAgentPod
		d := api.SandboxDesired{
			SandboxID: string(c.UID),
			ClaimUID:  string(c.UID),
			ClaimName: c.Name,
			Image:     c.Spec.Image,
			CPU:       c.Spec.CPU,
			Memory:    c.Spec.Memory,
			Port:      c.Spec.Port,
			Command:   c.Spec.Command,
			Args:      c.Spec.Args,
			Env:       c.Spec.Env,
		}
		desiredByAgent[agentPodName] = append(desiredByAgent[agentPodName], d)
	}

	// For each agent in registry, send SandboxesRequest
	agents := l.Registry.GetAllAgents()
	for _, a := range agents {
		agentPodName := a.PodName
		desired := desiredByAgent[agentPodName]

		req := &api.SandboxesRequest{
			AgentID:   string(a.ID),
			Sandboxes: desired,
		}

		resp, err := l.AgentClient.SyncSandboxes(a.PodIP, 8081, req)
		if err != nil {
			logger.Error(err, "sync sandboxes with agent failed", "agentPod", agentPodName)
			continue
		}

		// Update claims based on returned sandbox statuses
		for _, st := range resp.Sandboxes {
			claim, ok := claimsByUID[st.ClaimUID]
			if !ok {
				continue
			}

			// Fetch latest version before status update
			var fresh apiv1alpha1.SandboxClaim
			if err := l.Client.Get(ctx, types.NamespacedName{Namespace: claim.Namespace, Name: claim.Name}, &fresh); err != nil {
				logger.Error(err, "failed to get fresh SandboxClaim", "claim", claim.Name)
				continue
			}

			// 目前先简单处理：一旦 Agent 报告 Running，则认为创建成功
			if st.Phase == "Running" || st.Phase == "" {
				fresh.Status.SandboxID = st.SandboxID
				if st.Port == 0 {
					fresh.Status.Address = fmt.Sprintf("%s:%d", a.PodIP, fresh.Spec.Port)
				} else {
					fresh.Status.Address = fmt.Sprintf("%s:%d", a.PodIP, st.Port)
				}
				fresh.Status.Phase = "Running"
			} else if st.Phase == "Failed" {
				fresh.Status.Phase = "Failed"
			}

			if err := l.Client.Status().Update(ctx, &fresh); err != nil {
				logger.Error(err, "failed to update SandboxClaim status", "claim", fresh.Name)
			}
		}
	}

	return nil
}
