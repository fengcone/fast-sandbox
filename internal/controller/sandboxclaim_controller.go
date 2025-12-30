package controller

import (
	"context"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SandboxClaimReconciler reconciles SandboxClaim resources.
type SandboxClaimReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Ctx    context.Context
}

// Reconcile is the main reconciliation loop.
func (r *SandboxClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// TODO: 实现 SandboxClaim 的调度与状态迁移逻辑
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SandboxClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.SandboxClaim{}).
		Complete(r)
}
