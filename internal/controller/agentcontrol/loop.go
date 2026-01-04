package agentcontrol

import (
	"context"
	"time"

	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentpool"

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

	return nil
}
