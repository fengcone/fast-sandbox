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
	AgentClient api.AgentAPIClient
}

func (r *SandboxReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var sandbox apiv1alpha1.Sandbox
	if err := r.Get(ctx, req.NamespacedName, &sandbox); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if sandbox has expired (only if not being deleted)
	// 如果有 deletionTimestamp，优先处理 finalizer，跳过过期检查
	if sandbox.ObjectMeta.DeletionTimestamp == nil && sandbox.Spec.ExpireTime != nil && !sandbox.Spec.ExpireTime.IsZero() {
		if time.Now().After(sandbox.Spec.ExpireTime.Time) {
			// Sandbox has expired - soft delete: remove runtime but keep CRD for history
			if sandbox.Status.Phase != "Expired" {
				// 1. Delete the underlying sandbox from Agent
				if sandbox.Status.AssignedPod != "" {
					if err := r.deleteFromAgent(ctx, &sandbox); err != nil {
						return ctrl.Result{}, fmt.Errorf("failed to delete sandbox from agent: %w", err)
					}
					r.Registry.Release(agentpool.AgentID(sandbox.Status.AssignedPod), &sandbox)
				}

				// 2. Update CRD status to "Expired" (keep CRD for history)
				err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					latest := &apiv1alpha1.Sandbox{}
					if err := r.Get(ctx, req.NamespacedName, latest); err != nil {
						return err
					}
					latest.Status.Phase = "Expired"
					latest.Status.AssignedPod = ""
					latest.Status.SandboxID = ""
					return r.Status().Update(ctx, latest)
				})
				return ctrl.Result{}, err
			}
			// Already expired, no need to requeue
			return ctrl.Result{}, nil
		}
		// Not expired yet, requeue after the remaining time
		remainingTime := time.Until(sandbox.Spec.ExpireTime.Time)
		if remainingTime > 0 && remainingTime < 30*time.Second {
			// Requeue before expiration to check again
			return ctrl.Result{RequeueAfter: remainingTime}, nil
		}
	}

	finalizerName := "sandbox.fast.io/cleanup"
	if sandbox.ObjectMeta.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(&sandbox, finalizerName) {
			// Expired 状态：底层 sandbox 已删除，直接移除 finalizer
			if sandbox.Status.Phase == "Expired" {
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

			// 异步删除流程：Terminating → terminated → remove finalizer
			if sandbox.Status.Phase == "Terminating" || sandbox.Status.Phase == "Bound" {
				agent, ok := r.Registry.GetAgentByID(agentpool.AgentID(sandbox.Status.AssignedPod))
				if !ok {
					// Agent 不存在，直接清理 finalizer
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

				// 检查 agent 上报的状态
				agentStatus, hasStatus := agent.SandboxStatuses[sandbox.Name]

				// 第一次进入删除流程，调用 agent 删除（异步）
				if sandbox.Status.Phase == "Bound" {
					if err := r.deleteFromAgent(ctx, &sandbox); err != nil {
						// Agent 删除调用失败，重试
						return ctrl.Result{}, fmt.Errorf("failed to delete from agent: %w", err)
					}
					// 标记为 Terminating
					err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
						latest := &apiv1alpha1.Sandbox{}
						if err := r.Get(ctx, req.NamespacedName, latest); err != nil {
							return err
						}
						latest.Status.Phase = "Terminating"
						return r.Status().Update(ctx, latest)
					})
					if err != nil {
						return ctrl.Result{}, err
					}
					// Requeue 等待下次检查
					return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
				}

				// 已经是 Terminating 状态，检查 agent 是否完成删除
				if hasStatus && agentStatus.Phase == "terminated" {
					// Agent 已完成删除，释放资源并移除 finalizer
					r.Registry.Release(agentpool.AgentID(sandbox.Status.AssignedPod), &sandbox)
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

				// 仍在 terminating 中，继续等待
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}

			// Phase 不是 Bound/Terminating，直接清理
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
		// Agent 不存在（被删除或心跳超时清理）
		// 根据 failurePolicy 决定是否重新调度
		if sandbox.Spec.FailurePolicy == apiv1alpha1.FailurePolicyAutoRecreate {
			// AutoRecreate 模式：立即清空 assignedPod 触发重新调度
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				latest := &apiv1alpha1.Sandbox{}
				if err := r.Get(ctx, req.NamespacedName, latest); err != nil {
					return err
				}
				// 只有在 assignedPod 没有变化时才执行（避免竞争）
				if latest.Status.AssignedPod == sandbox.Status.AssignedPod {
					latest.Status.AssignedPod = ""
					latest.Status.Phase = "Pending"
					latest.Status.SandboxID = ""
					return r.Status().Update(ctx, latest)
				}
				return nil
			})
			return ctrl.Result{Requeue: true}, err
		}
		// Manual 模式：保持当前状态，不自动重新调度
		// 只是等待用户手动干预（更新 resetRevision）
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// 检查心跳超时
	heartbeatAge := time.Since(agent.LastHeartbeat)
	if heartbeatAge < 10*time.Second {
		// 心跳正常
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
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// 心跳超时但 Agent 仍在 Registry 中（Agent 进程可能挂了但 Pod 还在）
	// 等待 AgentControlLoop 的 CleanupStaleAgents（5分钟）清理后触发 AutoRecreate
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
		Sandbox: api.SandboxSpec{
			SandboxID:  sandbox.Name,
			ClaimName:  sandbox.Name,
			Image:      sandbox.Spec.Image,
			Command:    sandbox.Spec.Command,
			Args:       sandbox.Spec.Args,
			Env:        envVarToMap(sandbox.Spec.Envs),
			WorkingDir: sandbox.Spec.WorkingDir,
		},
	})
	return err
}

// envVarToMap converts K8s EnvVar slice to map[string]string
func envVarToMap(envs []corev1.EnvVar) map[string]string {
	result := make(map[string]string)
	for _, e := range envs {
		result[e.Name] = e.Value
	}
	return result
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
