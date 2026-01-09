package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentpool"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
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

	// 0. 手动重置逻辑 (ResetRevision)
	if sandbox.Spec.ResetRevision != nil {
		if sandbox.Status.AcceptedResetRevision == nil || sandbox.Spec.ResetRevision.After(sandbox.Status.AcceptedResetRevision.Time) {
			logger.Info("Manual reset requested, evicting sandbox", "revision", sandbox.Spec.ResetRevision)
			sandbox.Status.AssignedPod = ""
			sandbox.Status.NodeName = ""
			sandbox.Status.Phase = "Pending"
			sandbox.Status.AcceptedResetRevision = sandbox.Spec.ResetRevision
			if err := r.Status().Update(ctx, &sandbox); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// 0.1 自动过期清理逻辑 (ExpireTime GC)
	if sandbox.Spec.ExpireTime != nil && !sandbox.Spec.ExpireTime.IsZero() {
		now := time.Now().UTC()
		expireAt := sandbox.Spec.ExpireTime.UTC()
		if now.After(expireAt) || now.Equal(expireAt) {
			logger.Info("Sandbox expired, deleting", "name", sandbox.Name)
			if err := r.Delete(ctx, &sandbox); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
			return ctrl.Result{}, nil
		}
	}

	// 1. 调度逻辑
	if sandbox.Status.AssignedPod == "" {
		if sandbox.Spec.PoolRef == "" {
			logger.Error(nil, "poolRef is required but empty")
			sandbox.Status.Phase = "Failed"
			r.Status().Update(ctx, &sandbox)
			return ctrl.Result{}, nil
		}
		_, err := r.handleScheduling(ctx, &sandbox)
		if err != nil {
			logger.Info("Scheduling pending...", "reason", err.Error())
		}
	}

	// 1.1 健康检查与自愈逻辑
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
		
		// 更新 Condition
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
			r.Status().Update(ctx, &sandbox)
		}

		// 检查自动恢复策略
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

		// 2. 同步状态到 Agent
		if isConnected {
			r.syncAgent(ctx, sandbox.Status.AssignedPod)
			r.updateStatusFromRegistry(ctx, &sandbox)
		}
	}

	// 计算下一次 Requeue 时间
	nextCheck := 5 * time.Second
	if sandbox.Spec.ExpireTime != nil && !sandbox.Spec.ExpireTime.IsZero() {
		remaining := time.Until(sandbox.Spec.ExpireTime.Time)
		if remaining > 0 && remaining < nextCheck {
			nextCheck = remaining
		}
	}
	return ctrl.Result{RequeueAfter: nextCheck}, nil
}

func (r *SandboxReconciler) handleScheduling(ctx context.Context, sandbox *apiv1alpha1.Sandbox) (ctrl.Result, error) {
	agent, err := r.schedule(*sandbox)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &apiv1alpha1.Sandbox{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(sandbox), latest); err != nil {
			return err
		}
		if latest.Status.AssignedPod != "" {
			return nil
		}
		latest.Status.AssignedPod = agent.PodName
		latest.Status.NodeName = agent.NodeName
		latest.Status.Phase = "Bound"
		return r.Status().Update(ctx, latest)
	})
	return ctrl.Result{Requeue: true}, err
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

func (r *SandboxReconciler) schedule(sandbox apiv1alpha1.Sandbox) (*agentpool.AgentInfo, error) {
	agents := r.Registry.GetAllAgents()
	if len(agents) == 0 { return nil, fmt.Errorf("no agents") }

	var allSandboxes apiv1alpha1.SandboxList
	if err := r.List(context.Background(), &allSandboxes); err != nil {
		return nil, err
	}
	
	usedSlots := make(map[string]int)
	usedPorts := make(map[string]map[int32]bool)

	for _, sb := range allSandboxes.Items {
		if sb.Status.AssignedPod != "" && sb.Name != sandbox.Name {
			usedSlots[sb.Status.AssignedPod]++
			if usedPorts[sb.Status.AssignedPod] == nil {
				usedPorts[sb.Status.AssignedPod] = make(map[int32]bool)
			}
			for _, p := range sb.Spec.ExposedPorts {
				usedPorts[sb.Status.AssignedPod][p] = true
			}
		}
	}

	var candidates []agentpool.AgentInfo
	for _, a := range agents {
		if a.PoolName != sandbox.Spec.PoolRef { continue }
		if usedSlots[a.PodName] >= a.Capacity { continue }
		
		conflict := false
		for _, p := range sandbox.Spec.ExposedPorts {
			if usedPorts[a.PodName][p] { conflict = true; break }
		}
		if conflict { continue }
		
		a.Allocated = usedSlots[a.PodName]
		candidates = append(candidates, a)
	}

	if len(candidates) == 0 { return nil, fmt.Errorf("insufficient capacity") }

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Allocated < candidates[j].Allocated
	})
	return &candidates[0], nil
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
		if sb.Status.AssignedPod == "" { return nil }
		return []string{sb.Status.AssignedPod}
	}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).For(&apiv1alpha1.Sandbox{}).Complete(r)
}