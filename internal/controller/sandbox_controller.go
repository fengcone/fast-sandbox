package controller

import (
	"context"
	"fmt"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentpool"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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

	// --- 0. 删除逻辑 (Finalizer) ---
	finalizerName := "sandbox.fast.io/cleanup"
	if sandbox.ObjectMeta.DeletionTimestamp != nil {
		if !controllerutil.ContainsFinalizer(&sandbox, finalizerName) {
			// Finalizer 已被移除，无需处理
			return ctrl.Result{}, nil
		}

		// 执行资源清理
		cleanupErr := error(nil)
		if sandbox.Status.AssignedPod != "" {
			logger.Info("Sandbox deleting, releasing resources", "agent", sandbox.Status.AssignedPod)
			// 注意：Registry.Release 当前没有返回错误，但为了健壮性保留错误处理模式
			r.Registry.Release(agentpool.AgentID(sandbox.Status.AssignedPod), &sandbox)
			// 清理状态字段，防止重复释放
			sandbox.Status.AssignedPod = ""
			sandbox.Status.NodeName = ""
		}

		// 只在清理成功后移除 finalizer
		if cleanupErr == nil {
			controllerutil.RemoveFinalizer(&sandbox, finalizerName)
			if err := r.Update(ctx, &sandbox); err != nil {
				logger.Error(err, "Failed to remove finalizer after cleanup")
				return ctrl.Result{Requeue: true}, err
			}
			logger.Info("Sandbox cleanup completed, finalizer removed")
			return ctrl.Result{}, nil
		}

		// 清理失败，记录错误但不移除 finalizer，让 K8s 重新触发
		logger.Error(cleanupErr, "Sandbox cleanup failed, will retry")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, cleanupErr
	}

	if !controllerutil.ContainsFinalizer(&sandbox, finalizerName) {
		controllerutil.AddFinalizer(&sandbox, finalizerName)
		if err := r.Update(ctx, &sandbox); err != nil {
			return ctrl.Result{}, err
		}
	}

	// --- 0.1 重置逻辑 (ResetRevision) ---
	if sandbox.Spec.ResetRevision != nil && !sandbox.Spec.ResetRevision.IsZero() {
		shouldReset := false
		if sandbox.Status.AcceptedResetRevision == nil || sandbox.Status.AcceptedResetRevision.IsZero() {
			shouldReset = true
		} else if sandbox.Spec.ResetRevision.Time.Truncate(time.Second).After(sandbox.Status.AcceptedResetRevision.Time.Truncate(time.Second)) {
			shouldReset = true
		}

		if shouldReset {
			logger.Info("Manual reset requested, evicting sandbox", "revision", sandbox.Spec.ResetRevision)
			
			if sandbox.Status.AssignedPod != "" {
				r.Registry.Release(agentpool.AgentID(sandbox.Status.AssignedPod), &sandbox)
			}

			sandbox.Status.AssignedPod = ""
			sandbox.Status.NodeName = ""
			sandbox.Status.Phase = "Pending"
			sandbox.Status.SandboxID = ""
			sandbox.Status.AcceptedResetRevision = sandbox.Spec.ResetRevision
			if err := r.Status().Update(ctx, &sandbox); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// --- 0.2 过期清理 (ExpireTime GC) ---
	if sandbox.Spec.ExpireTime != nil && !sandbox.Spec.ExpireTime.IsZero() {
		if time.Now().UTC().After(sandbox.Spec.ExpireTime.UTC()) {
			logger.Info("Sandbox expired, deleting", "name", sandbox.Name)
			
			if sandbox.Status.AssignedPod != "" {
				r.Registry.Release(agentpool.AgentID(sandbox.Status.AssignedPod), &sandbox)
			}

			if err := r.Delete(ctx, &sandbox); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
			return ctrl.Result{}, nil
		}
	}

	// --- 1. 调度逻辑 ---
	if sandbox.Status.AssignedPod == "" {
		if sandbox.Spec.PoolRef == "" {
			logger.Error(nil, "poolRef is required")
			sandbox.Status.Phase = "Failed"
			r.Status().Update(ctx, &sandbox)
			return ctrl.Result{}, nil
		}
		res, err := r.handleScheduling(ctx, &sandbox)
		if err != nil {
			logger.Info("Scheduling pending...", "reason", err.Error())
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		if res.Requeue {
			return res, nil
		}
	}

	// --- 1.1 健康检查与自愈逻辑 ---
	if sandbox.Status.AssignedPod != "" {
		agent, ok := r.Registry.GetAgentByID(agentpool.AgentID(sandbox.Status.AssignedPod))
		if !ok {
			logger.Info("Assigned agent disappeared, resetting", "agent", sandbox.Status.AssignedPod)
			sandbox.Status.AssignedPod = ""
			sandbox.Status.Phase = "Pending"
			r.Status().Update(ctx, &sandbox)
			return ctrl.Result{Requeue: true}, nil
		}

		heartbeatAge := time.Since(agent.LastHeartbeat)
		isConnected := heartbeatAge < 10*time.Second
		
		newCond := metav1.Condition{
			Type:               "AgentReady",
			Status:             metav1.ConditionTrue,
			Reason:             "HeartbeatHealthy",
			Message:            fmt.Sprintf("Agent last seen %s ago", heartbeatAge.Truncate(time.Second)),
			LastTransitionTime: metav1.Now(),
		}
		if !isConnected {
			newCond.Status = metav1.ConditionFalse
			newCond.Reason = "HeartbeatTimeout"
			newCond.Message = fmt.Sprintf("Agent heartbeat is stale (age: %s)", heartbeatAge.Truncate(time.Second))
		}

		if r.updateCondition(&sandbox, newCond) {
			if err := r.Status().Update(ctx, &sandbox); err != nil {
				return ctrl.Result{}, err
			}
		}

		if !isConnected && sandbox.Spec.FailurePolicy == apiv1alpha1.FailurePolicyAutoRecreate {
			timeout := time.Duration(60) * time.Second
			if sandbox.Spec.RecoveryTimeoutSeconds > 0 {
				timeout = time.Duration(sandbox.Spec.RecoveryTimeoutSeconds) * time.Second
			}
			if heartbeatAge > timeout {
				logger.Info("Agent lost beyond timeout, auto-recreating", "agent", agent.PodName)
				sandbox.Status.AssignedPod = ""
				sandbox.Status.Phase = "Pending"
				r.Status().Update(ctx, &sandbox)
				return ctrl.Result{Requeue: true}, nil
			}
		}

		if isConnected {
			r.syncAgent(ctx, sandbox.Status.AssignedPod)
			r.updateStatusFromRegistry(ctx, &sandbox)
		}
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *SandboxReconciler) handleScheduling(ctx context.Context, sandbox *apiv1alpha1.Sandbox) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 原子预留内存插槽和端口 (内部包含镜像亲和性打分)
	agent, err := r.Registry.Allocate(sandbox)
	if err != nil {
		return ctrl.Result{}, err
	}

	// 尝试更新 K8s 状态
	// 使用自定义错误类型来区分"已被占用"和真正的"成功"
	errAlreadyAssigned := fmt.Errorf("sandbox already assigned to another agent")
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &apiv1alpha1.Sandbox{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(sandbox), latest); err != nil {
			return err
		}
		if latest.Status.AssignedPod != "" {
			// 检查是否是被当前 agent 占用（可能是之前重试时已成功）
			if latest.Status.AssignedPod == agent.PodName {
				// 已被当前 agent 占用，视为成功
				return nil
			}
			// 被其他 agent 占用，需要回滚
			logger.Info("Sandbox already assigned to another agent during retry", "assignedTo", latest.Status.AssignedPod, "attempted", agent.PodName)
			return errAlreadyAssigned
		}
		latest.Status.AssignedPod = agent.PodName
		latest.Status.NodeName = agent.NodeName
		latest.Status.Phase = "Bound"
		return r.Status().Update(ctx, latest)
	})

	if updateErr != nil {
		if updateErr == errAlreadyAssigned {
			logger.Info("Scheduling lost race, rolling back slot allocation", "agent", agent.PodName)
			r.Registry.Release(agent.ID, sandbox)
			// 返回重试，让调度逻辑重新选择
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}
		logger.Error(updateErr, "Failed to commit scheduling, rolling back slot", "agent", agent.PodName)
		r.Registry.Release(agent.ID, sandbox)
		return ctrl.Result{}, updateErr
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *SandboxReconciler) updateStatusFromRegistry(ctx context.Context, sandbox *apiv1alpha1.Sandbox) error {
	agentInfo, ok := r.Registry.GetAgentByID(agentpool.AgentID(sandbox.Status.AssignedPod))
	if !ok { return nil }

	updated := false
	if status, ok := agentInfo.SandboxStatuses[sandbox.Name]; ok {
		if sandbox.Status.Phase != status.Phase || sandbox.Status.SandboxID != status.SandboxID {
			sandbox.Status.Phase = status.Phase
			sandbox.Status.SandboxID = status.SandboxID
			updated = true
		}
	}

	if len(sandbox.Spec.ExposedPorts) > 0 && agentInfo.PodIP != "" {
		var endpoints []string
		for _, p := range sandbox.Spec.ExposedPorts {
			endpoints = append(endpoints, fmt.Sprintf("%s:%d", agentInfo.PodIP, p))
		}
		if len(sandbox.Status.Endpoints) != len(endpoints) {
			sandbox.Status.Endpoints = endpoints
			updated = true
		}
	}

	if updated {
		return r.Status().Update(ctx, sandbox)
	}
	return nil
}

func (r *SandboxReconciler) syncAgent(ctx context.Context, agentPodName string) error {
	agentInfo, ok := r.Registry.GetAgentByID(agentpool.AgentID(agentPodName))
	if !ok { return nil }

	var sandboxList apiv1alpha1.SandboxList
	if err := r.List(ctx, &sandboxList, client.MatchingFields{"status.assignedPod": agentPodName}); err != nil {
		return err
	}

	var specs []api.SandboxSpec
	for _, sb := range sandboxList.Items {
		specs = append(specs, api.SandboxSpec{
			SandboxID: sb.Name,
			ClaimUID:  string(sb.UID),
			ClaimName: sb.Name,
			Image:     sb.Spec.Image,
			Command:   sb.Spec.Command,
			Args:      sb.Spec.Args,
		})
	}

	endpoint := fmt.Sprintf("%s:8081", agentInfo.PodIP)
	return r.AgentClient.SyncSandboxes(endpoint, &api.SandboxesRequest{AgentID: agentPodName, SandboxSpecs: specs})
}

func (r *SandboxReconciler) updateCondition(sandbox *apiv1alpha1.Sandbox, newCond metav1.Condition) bool {
	for i, existing := range sandbox.Status.Conditions {
		if existing.Type == newCond.Type {
			if existing.Status == newCond.Status && existing.Reason == newCond.Reason { return false }
			sandbox.Status.Conditions[i] = newCond
			return true
		}
	}
	sandbox.Status.Conditions = append(sandbox.Status.Conditions, newCond)
	return true
}

func (r *SandboxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1alpha1.Sandbox{}, "status.assignedPod", func(rawObj client.Object) []string {
		sb := rawObj.(*apiv1alpha1.Sandbox)
		return []string{sb.Status.AssignedPod}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.Sandbox{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []ctrl.Request {
			pod, ok := obj.(*corev1.Pod)
			if !ok || pod.Labels["app"] != "sandbox-agent" { return nil }
			if pod.Status.Phase != corev1.PodRunning { return nil }

			var sbList apiv1alpha1.SandboxList
			if err := r.List(ctx, &sbList, client.InNamespace(pod.Namespace), client.MatchingFields{"status.assignedPod": ""}); err != nil {
				return nil
			}

			var reqs []ctrl.Request
			for _, sb := range sbList.Items {
				reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&sb)})
			}
			return reqs
		})).
		Complete(r)
}