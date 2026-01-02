package controller

import (
	"context"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/controller/agentpool"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// SandboxPoolReconciler reconciles SandboxPool resources.
type SandboxPoolReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Registry agentpool.AgentRegistry
}

// Reconcile currently manages SandboxPool pods and updates status.
func (r *SandboxPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pool apiv1alpha1.SandboxPool
	if err := r.Get(ctx, req.NamespacedName, &pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// List Pods belonging to this pool
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(pool.Namespace), client.MatchingLabels{"sandbox.fast.io/pool": pool.Name}); err != nil {
		logger.Error(err, "failed to list pods for SandboxPool")
		return ctrl.Result{}, err
	}

	currentPods := int32(len(podList.Items))

	// Derive agent stats from registry, only counting agents belonging to this pool
	agents := r.Registry.GetAllAgents()
	var totalAgents, idleAgents, busyAgents int32
	for _, a := range agents {
		if a.PoolName != pool.Name {
			continue
		}
		// 可选：按 namespace 进一步过滤
		if a.Namespace != pool.Namespace {
			continue
		}

		totalAgents++
		if a.Allocated == 0 {
			idleAgents++
		} else {
			busyAgents++
		}
	}

	// Decide scaling based on capacity; buffer-based logic only when we有Agent统计
	desiredPods := currentPods
	cap := pool.Spec.Capacity

	// Enforce poolMin/poolMax first
	if desiredPods < cap.PoolMin {
		desiredPods = cap.PoolMin
	}
	if desiredPods > cap.PoolMax {
		desiredPods = cap.PoolMax
	}

	// If我们已经有Agent在线，可以使用 bufferMin/bufferMax 做细粒度扩缩
	if totalAgents > 0 {
		if idleAgents < cap.BufferMin && desiredPods < cap.PoolMax {
			// 预期需要更多空闲 Agent，向上调整 desiredPods
			needed := cap.BufferMin - idleAgents
			if needed > 0 {
				if desiredPods+needed > cap.PoolMax {
					needed = cap.PoolMax - desiredPods
				}
				desiredPods += needed
			}
		}
		if idleAgents > cap.BufferMax && desiredPods > cap.PoolMin {
			// 空闲过多，尝试缩小池子
			toDrop := idleAgents - cap.BufferMax
			if desiredPods-toDrop < cap.PoolMin {
				toDrop = desiredPods - cap.PoolMin
			}
			if toDrop > 0 {
				desiredPods -= toDrop
			}
		}
	}

	// Scale up: create new Pods based on AgentTemplate
	if desiredPods > currentPods {
		toCreate := desiredPods - currentPods
		for i := int32(0); i < toCreate; i++ {
			pod := corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:    pool.Namespace,
					GenerateName: pool.Name + "-agent-",
					Labels:       map[string]string{},
				},
				Spec: pool.Spec.AgentTemplate.Spec,
			}

			// Merge template labels
			for k, v := range pool.Spec.AgentTemplate.Labels {
				pod.Labels[k] = v
			}
			// Add pool label
			pod.Labels["sandbox.fast.io/pool"] = pool.Name

			if err := controllerutil.SetControllerReference(&pool, &pod, r.Scheme); err != nil {
				logger.Error(err, "failed to set controller reference for pod")
				return ctrl.Result{}, err
			}

			if err := r.Create(ctx, &pod); err != nil {
				logger.Error(err, "failed to create agent pod")
				return ctrl.Result{}, err
			}
		}
	}

	// Scale down: delete extra Pods (当前阶段简单按列表顺序删除，后续可基于 idle 状态优化)
	if desiredPods < currentPods {
		toDelete := currentPods - desiredPods
		deleted := int32(0)
		for i := range podList.Items {
			if deleted >= toDelete {
				break
			}
			pod := &podList.Items[i]
			if err := r.Delete(ctx, pod); err != nil {
				logger.Error(err, "failed to delete agent pod", "pod", pod.Name)
				return ctrl.Result{}, err
			}
			deleted++
		}
	}

	// Update status snapshot
	pool.Status.ObservedGeneration = pool.Generation
	pool.Status.CurrentPods = desiredPods
	pool.Status.ReadyPods = desiredPods // 简化处理，后续可按 Pod Ready 实际计算
	pool.Status.TotalAgents = totalAgents
	pool.Status.IdleAgents = idleAgents
	pool.Status.BusyAgents = busyAgents

	if err := r.Status().Update(ctx, &pool); err != nil {
		logger.Error(err, "failed to update SandboxPool status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SandboxPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.SandboxPool{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
