package controller

import (
	"context"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/controller/agentpool"
	"fast-sandbox/internal/controller/scheduler"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// SandboxClaimReconciler reconciles SandboxClaim resources.
type SandboxClaimReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Ctx       context.Context
	Registry  agentpool.AgentRegistry
	Scheduler scheduler.Scheduler
}

// Reconcile is the main reconciliation loop.
func (r *SandboxClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var claim apiv1alpha1.SandboxClaim
	if err := r.Get(ctx, req.NamespacedName, &claim); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("Reconciling SandboxClaim", "name", claim.Name, "phase", claim.Status.Phase, "assignedPod", claim.Status.AssignedAgentPod)

	// 仅在 Pending/空 Phase 时做调度，其他状态交给 AgentControlLoop 处理
	if claim.Status.Phase == "" || claim.Status.Phase == "Pending" {
		agents := r.Registry.GetAllAgents()
		logger.Info("Scheduling SandboxClaim", "name", claim.Name, "totalAgents", len(agents))
		
		agent, err := r.Scheduler.Schedule(ctx, &claim, agents)
		if err != nil {
			logger.Info("No available agent, requeuing", "claim", claim.Name, "error", err.Error())
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		// 记录调度结果
		logger.Info("Agent selected", "claim", claim.Name, "agentPod", agent.PodName, "agentIP", agent.PodIP)
		claim.Status.AssignedAgentPod = agent.PodName
		claim.Status.NodeName = agent.NodeName
		claim.Status.Phase = "Scheduling"

		if err := r.Status().Update(ctx, &claim); err != nil {
			logger.Error(err, "Failed to update SandboxClaim status")
			return ctrl.Result{}, err
		}

		logger.Info("SandboxClaim scheduled successfully", "claim", claim.Name, "assignedPod", agent.PodName)
		return ctrl.Result{}, nil
	}

	// 其他 Phase 交给 AgentControlLoop 处理
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SandboxClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.SandboxClaim{}).
		Complete(r)
}
