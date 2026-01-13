package controller

import (
	"context"
	"fmt"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentpool"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

type SandboxReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Ctx         context.Context
	Registry    agentpool.AgentRegistry
	AgentClient *api.AgentClient
}

func (r *SandboxReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var sandbox apiv1alpha1.Sandbox
	if err := r.Get(ctx, req.NamespacedName, &sandbox); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	finalizerName := "sandbox.fast.io/cleanup"
	if sandbox.ObjectMeta.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(&sandbox, finalizerName) {
			if sandbox.Status.AssignedPod != "" {
				r.deleteFromAgent(ctx, &sandbox)
				r.Registry.Release(agentpool.AgentID(sandbox.Status.AssignedPod), &sandbox)
			}
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				latest := &apiv1alpha1.Sandbox{}
				if err := r.Get(ctx, req.NamespacedName, latest); err != nil {
					return err
				}
				controllerutil.RemoveFinalizer(latest, finalizerName)
				return r.Update(ctx, latest)
			})
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&sandbox, finalizerName) {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			latest := &apiv1alpha1.Sandbox{}
			if err := r.Get(ctx, req.NamespacedName, latest); err != nil {
				return err
			}
			controllerutil.AddFinalizer(latest, finalizerName)
			return r.Update(ctx, latest)
		})
		return ctrl.Result{Requeue: true}, err
	}

	if sandbox.Spec.ResetRevision != nil && !sandbox.Spec.ResetRevision.IsZero() {
		if sandbox.Status.AcceptedResetRevision == nil || sandbox.Spec.ResetRevision.After(sandbox.Status.AcceptedResetRevision.Time) {
			if sandbox.Status.AssignedPod != "" {
				r.Registry.Release(agentpool.AgentID(sandbox.Status.AssignedPod), &sandbox)
			}
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				latest := &apiv1alpha1.Sandbox{}
				if err := r.Get(ctx, req.NamespacedName, latest); err != nil {
					return err
				}
				latest.Status.AssignedPod = ""
				latest.Status.Phase = "Pending"
				latest.Status.AcceptedResetRevision = sandbox.Spec.ResetRevision
				return r.Status().Update(ctx, latest)
			})
			return ctrl.Result{Requeue: true}, err
		}
	}

	if sandbox.Status.AssignedPod == "" {
		return r.handleScheduling(ctx, &sandbox)
	}

	agent, ok := r.Registry.GetAgentByID(agentpool.AgentID(sandbox.Status.AssignedPod))
	if !ok {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			latest := &apiv1alpha1.Sandbox{}
			if err := r.Get(ctx, req.NamespacedName, latest); err != nil {
				return err
			}
			latest.Status.AssignedPod = ""
			latest.Status.Phase = "Pending"
			return r.Status().Update(ctx, latest)
		})
		return ctrl.Result{Requeue: true}, err
	}

	if time.Since(agent.LastHeartbeat) < 10*time.Second {
		if sandbox.Status.Phase == "Pending" || sandbox.Status.Phase == "" {
			if err := r.handleCreateOnAgent(ctx, &sandbox); err != nil {
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				latest := &apiv1alpha1.Sandbox{}
				if err := r.Get(ctx, req.NamespacedName, latest); err != nil {
					return err
				}
				latest.Status.Phase = "Bound"
				return r.Status().Update(ctx, latest)
			})
			return ctrl.Result{Requeue: true}, err
		}
		if err := r.updateStatusFromRegistry(ctx, &sandbox); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *SandboxReconciler) handleScheduling(ctx context.Context, sandbox *apiv1alpha1.Sandbox) (ctrl.Result, error) {
	agent, err := r.Registry.Allocate(sandbox)
	if err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &apiv1alpha1.Sandbox{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(sandbox), latest); err != nil {
			return err
		}
		if latest.Status.AssignedPod != "" {
			return fmt.Errorf("race")
		}
		latest.Status.AssignedPod = agent.PodName
		latest.Status.NodeName = agent.NodeName
		latest.Status.Phase = "Pending"
		return r.Status().Update(ctx, latest)
	})
	if err != nil {
		r.Registry.Release(agent.ID, sandbox)
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{Requeue: true}, nil
}

func (r *SandboxReconciler) updateStatusFromRegistry(ctx context.Context, sandbox *apiv1alpha1.Sandbox) error {
	agentInfo, ok := r.Registry.GetAgentByID(agentpool.AgentID(sandbox.Status.AssignedPod))
	if !ok {
		return nil
	}
	if status, ok := agentInfo.SandboxStatuses[sandbox.Name]; ok {
		if sandbox.Status.Phase != status.Phase || sandbox.Status.SandboxID != status.SandboxID {
			return retry.RetryOnConflict(retry.DefaultRetry, func() error {
				latest := &apiv1alpha1.Sandbox{}
				if err := r.Get(ctx, client.ObjectKeyFromObject(sandbox), latest); err != nil {
					return err
				}
				latest.Status.Phase = status.Phase
				latest.Status.SandboxID = status.SandboxID
				if len(latest.Spec.ExposedPorts) > 0 && agentInfo.PodIP != "" {
					var ep []string
					for _, p := range latest.Spec.ExposedPorts {
						ep = append(ep, fmt.Sprintf("%s:%d", agentInfo.PodIP, p))
					}
					latest.Status.Endpoints = ep
				}
				return r.Status().Update(ctx, latest)
			})
		}
	}
	return nil
}

func (r *SandboxReconciler) handleCreateOnAgent(ctx context.Context, sandbox *apiv1alpha1.Sandbox) error {
	agentInfo, ok := r.Registry.GetAgentByID(agentpool.AgentID(sandbox.Status.AssignedPod))
	if !ok {
		return fmt.Errorf("no agent")
	}
	_, err := r.AgentClient.CreateSandbox(fmt.Sprintf("%s:8081", agentInfo.PodIP), &api.CreateSandboxRequest{
		Sandbox: api.SandboxSpec{SandboxID: sandbox.Name, ClaimName: sandbox.Name, Image: sandbox.Spec.Image, Command: sandbox.Spec.Command, Args: sandbox.Spec.Args},
	})
	return err
}

func (r *SandboxReconciler) deleteFromAgent(ctx context.Context, sandbox *apiv1alpha1.Sandbox) error {
	agentInfo, ok := r.Registry.GetAgentByID(agentpool.AgentID(sandbox.Status.AssignedPod))
	if !ok {
		return nil
	}
	_, err := r.AgentClient.DeleteSandbox(fmt.Sprintf("%s:8081", agentInfo.PodIP), &api.DeleteSandboxRequest{SandboxID: sandbox.Name})
	return err
}

func (r *SandboxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1alpha1.Sandbox{}, "status.assignedPod", func(o client.Object) []string {
		return []string{o.(*apiv1alpha1.Sandbox).Status.AssignedPod}
	})
	return ctrl.NewControllerManagedBy(mgr).For(&apiv1alpha1.Sandbox{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
			pod := o.(*corev1.Pod)
			if pod.Labels["app"] != "sandbox-agent" || pod.Status.Phase != corev1.PodRunning {
				return nil
			}
			var sbList apiv1alpha1.SandboxList
			mgr.GetClient().List(ctx, &sbList, client.MatchingFields{"status.assignedPod": ""})
			var reqs []ctrl.Request
			for _, sb := range sbList.Items {
				reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&sb)})
			}
			return reqs
		})).Complete(r)
}
