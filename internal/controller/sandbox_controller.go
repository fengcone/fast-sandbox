package controller

import (
	"context"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/controller/agentpool"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SandboxReconciler reconciles Sandbox resources.
type SandboxReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Ctx      context.Context
	Registry agentpool.AgentRegistry
}

// Reconcile is the main reconciliation loop.
func (r *SandboxReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SandboxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.Sandbox{}).
		Complete(r)
}
