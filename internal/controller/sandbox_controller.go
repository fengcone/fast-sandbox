package controller

import (
	"context"
	"fmt"
	"sort"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentpool"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// SandboxReconciler reconciles Sandbox resources.
type SandboxReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Ctx         context.Context
	Registry    agentpool.AgentRegistry
	AgentClient *api.AgentClient
}

// Reconcile is the main reconciliation loop.
func (r *SandboxReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var sandbox apiv1alpha1.Sandbox
	if err := r.Get(ctx, req.NamespacedName, &sandbox); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 1. Check if assigned
	if sandbox.Status.AssignedPod == "" {
		// Schedule it
		agent, err := r.schedule(sandbox)
		if err != nil {
			logger.Error(err, "Failed to schedule sandbox")
			// TODO: Update condition to specific why
			return ctrl.Result{Requeue: true}, nil
		}

		sandbox.Status.AssignedPod = agent.PodName
		sandbox.Status.NodeName = agent.NodeName
		sandbox.Status.Phase = "Bound"

		if err := r.Status().Update(ctx, &sandbox); err != nil {
			return ctrl.Result{}, err
		}
		// Requeue to sync immediately
		return ctrl.Result{Requeue: true}, nil
	}

	// 2. Sync with Agent
	if err := r.syncAgent(ctx, sandbox.Status.AssignedPod); err != nil {
		logger.Error(err, "Failed to sync with agent", "agent", sandbox.Status.AssignedPod)
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, nil
}

func (r *SandboxReconciler) schedule(sandbox apiv1alpha1.Sandbox) (*agentpool.AgentInfo, error) {
	agents := r.Registry.GetAllAgents()
	if len(agents) == 0 {
		return nil, fmt.Errorf("no agents available")
	}

	// Filter & Score
	var candidates []agentpool.AgentInfo
	for _, a := range agents {
		// Capacity check
		if a.Allocated >= a.Capacity {
			continue
		}
		candidates = append(candidates, a)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("insufficient capacity")
	}

	// Simple scoring: Prefer image locality, then least allocated
	sort.Slice(candidates, func(i, j int) bool {
		scoreI := scoreAgent(candidates[i], sandbox)
		scoreJ := scoreAgent(candidates[j], sandbox)
		if scoreI != scoreJ {
			return scoreI > scoreJ
		}
		return candidates[i].Allocated < candidates[j].Allocated
	})

	// Pick best
	selected := candidates[0]

	// Update Registry allocation immediately (optimistic locking)
	if !r.Registry.AllocateSlot(selected.ID) {
		return nil, fmt.Errorf("failed to allocate slot (race condition)")
	}

	return &selected, nil
}

func scoreAgent(agent agentpool.AgentInfo, sandbox apiv1alpha1.Sandbox) int {
	score := 0
	// Image affinity
	for _, img := range agent.Images {
		if img == sandbox.Spec.Image {
			score += 100
			break
		}
	}
	return score
}

func (r *SandboxReconciler) syncAgent(ctx context.Context, agentPodName string) error {
	// 1. Get Agent Info
	agentID := agentpool.AgentID(agentPodName)
	agentInfo, ok := r.Registry.GetAgentByID(agentID)
	if !ok {
		return fmt.Errorf("agent %s not found in registry", agentPodName)
	}

	// 2. List all sandboxes assigned to this agent
	var sandboxList apiv1alpha1.SandboxList
	if err := r.List(ctx, &sandboxList, client.MatchingFields{"status.assignedPod": agentPodName}); err != nil {
		return err
	}

	var specs []api.SandboxSpec
	for _, sb := range sandboxList.Items {
		// Convert to Spec
		envs := make(map[string]string)
		for _, e := range sb.Spec.Envs {
			envs[e.Name] = e.Value
		}
		// Memory/CPU handling can be added here

		specs = append(specs, api.SandboxSpec{
			SandboxID: sb.Name, // Use CR Name as ID
			ClaimUID:  string(sb.UID),
			ClaimName: sb.Name,
			Image:     sb.Spec.Image,
			Command:   sb.Spec.Command,
			Args:      sb.Spec.Args,
			Env:       envs,
		})
	}

	// 3. Call Agent
	req := &api.SandboxesRequest{
		AgentID:      agentPodName,
		SandboxSpecs: specs,
	}

	endpoint := fmt.Sprintf("%s:8081", agentInfo.PodIP)
	return r.AgentClient.SyncSandboxes(endpoint, req)
}

// SetupWithManager sets up the controller with the Manager.
func (r *SandboxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Index status.assignedPod for efficient lookup
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1alpha1.Sandbox{}, "status.assignedPod", func(rawObj client.Object) []string {
		sandbox := rawObj.(*apiv1alpha1.Sandbox)
		if sandbox.Status.AssignedPod == "" {
			return nil
		}
		return []string{sandbox.Status.AssignedPod}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.Sandbox{}).
		Complete(r)
}