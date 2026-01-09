package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentpool"

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

	// 0. 自动过期清理逻辑 (ExpireTime GC)
	if sandbox.Spec.ExpireTime != nil && !sandbox.Spec.ExpireTime.IsZero() {
		now := time.Now().UTC()
		expireAt := sandbox.Spec.ExpireTime.UTC()
		
		if now.After(expireAt) || now.Equal(expireAt) {
			logger.Info("Sandbox expired, deleting", 
				"name", sandbox.Name, 
				"now", now.Format(time.RFC3339), 
				"expireAt", expireAt.Format(time.RFC3339))
			if err := r.Delete(ctx, &sandbox); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
			return ctrl.Result{}, nil
		}
	}

	// 1. 调度逻辑
	if sandbox.Status.AssignedPod == "" {
		// 防御性校验：poolRef 必填
		if sandbox.Spec.PoolRef == "" {
			logger.Error(nil, "poolRef is required but empty")
			sandbox.Status.Phase = "Failed"
			if err := r.Status().Update(ctx, &sandbox); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		_, err := r.handleScheduling(ctx, &sandbox)
		if err != nil {
			logger.Info("Scheduling pending...", "reason", err.Error())
		}
		// 调度后不立即返回，继续执行后面的 Requeue 逻辑以支持过期检查
	}

	// 1.1 校验绑定的 Agent 是否依然存在
	if sandbox.Status.AssignedPod != "" {
		if _, ok := r.Registry.GetAgentByID(agentpool.AgentID(sandbox.Status.AssignedPod)); !ok {
			logger.Info("Assigned agent disappeared, resetting for re-scheduling", "agent", sandbox.Status.AssignedPod)
			sandbox.Status.AssignedPod = ""
			sandbox.Status.NodeName = ""
			sandbox.Status.Phase = "Pending"
			if err := r.Status().Update(ctx, &sandbox); err != nil {
				return ctrl.Result{}, err
			}
			// 状态更新会触发下一次 Reconcile
			return ctrl.Result{}, nil
		}

		// 2. 同步状态到 Agent
		if err := r.syncAgent(ctx, sandbox.Status.AssignedPod); err != nil {
			logger.Error(err, "Failed to sync with agent", "agent", sandbox.Status.AssignedPod)
		}

		// 3. 从 Registry 同步运行状态回 CR (Bound -> Running)
		if err := r.updateStatusFromRegistry(ctx, &sandbox); err != nil {
			logger.Error(err, "Failed to update sandbox status")
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
	logger := log.FromContext(ctx)

	// 实时调度
	agent, err := r.schedule(*sandbox)
	if err != nil {
		logger.Error(err, "Failed to schedule sandbox")
		// 返回错误但不带 Result，让主循环最后的 RequeueAfter 逻辑生效
		return ctrl.Result{}, err
	}

	// 使用 RetryOnConflict 保证状态更新成功
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &apiv1alpha1.Sandbox{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(sandbox), latest); err != nil {
			return err
		}
		if latest.Status.AssignedPod != "" {
			return nil // 已经被调度了
		}
		latest.Status.AssignedPod = agent.PodName
		latest.Status.NodeName = agent.NodeName
		latest.Status.Phase = "Bound"
		return r.Status().Update(ctx, latest)
	})

	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *SandboxReconciler) updateStatusFromRegistry(ctx context.Context, sandbox *apiv1alpha1.Sandbox) error {
	agentInfo, ok := r.Registry.GetAgentByID(agentpool.AgentID(sandbox.Status.AssignedPod))
	if !ok {
		return nil
	}

	updated := false
	if status, ok := agentInfo.SandboxStatuses[sandbox.Name]; ok {
		if sandbox.Status.Phase != status.Phase || sandbox.Status.SandboxID != status.SandboxID {
			sandbox.Status.Phase = status.Phase
			sandbox.Status.SandboxID = status.SandboxID
			updated = true
		}
	}

	// 填充 Endpoints: PodIP + ExposedPorts
	if len(sandbox.Spec.ExposedPorts) > 0 && agentInfo.PodIP != "" {
		var newEndpoints []string
		for _, port := range sandbox.Spec.ExposedPorts {
			newEndpoints = append(newEndpoints, fmt.Sprintf("%s:%d", agentInfo.PodIP, port))
		}
		// 简单比较并更新
		if len(sandbox.Status.Endpoints) != len(newEndpoints) {
			sandbox.Status.Endpoints = newEndpoints
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
	if len(agents) == 0 {
		return nil, fmt.Errorf("no agents available")
	}

	// 1. 实时统计：已占用插槽和已占用端口
	var allSandboxes apiv1alpha1.SandboxList
	if err := r.List(context.Background(), &allSandboxes); err != nil {
		return nil, err
	}
	
	usedSlots := make(map[string]int)
	usedPorts := make(map[string]map[int32]bool) // PodName -> Set[Port]

	for _, sb := range allSandboxes.Items {
		if sb.Status.AssignedPod != "" && sb.Name != sandbox.Name {
			usedSlots[sb.Status.AssignedPod]++
			
			// 记录端口占用
			if usedPorts[sb.Status.AssignedPod] == nil {
				usedPorts[sb.Status.AssignedPod] = make(map[int32]bool)
			}
			for _, p := range sb.Spec.ExposedPorts {
				usedPorts[sb.Status.AssignedPod][p] = true
			}
		}
	}

	// 2. 过滤与打分
	var candidates []agentpool.AgentInfo
	for _, a := range agents {
		// Pool 匹配
		if a.PoolName != sandbox.Spec.PoolRef {
			continue
		}

		// A. 容量检查
		allocated := usedSlots[a.PodName]
		if allocated >= a.Capacity {
			continue
		}

		// B. 端口互斥检查
		hasConflict := false
		occupied := usedPorts[a.PodName]
		for _, p := range sandbox.Spec.ExposedPorts {
			if occupied[p] {
				hasConflict = true
				break
			}
		}
		if hasConflict {
			continue
		}
		
		// 临时更新用于排序的 Allocated
		a.Allocated = allocated
		candidates = append(candidates, a)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("insufficient capacity")
	}

	// 3. 排序：镜像亲和性优先，然后负载最低优先
	sort.Slice(candidates, func(i, j int) bool {
		scoreI := scoreAgent(candidates[i], sandbox)
		scoreJ := scoreAgent(candidates[j], sandbox)
		if scoreI != scoreJ {
			return scoreI > scoreJ
		}
		return candidates[i].Allocated < candidates[j].Allocated
	})

	return &candidates[0], nil
}

func scoreAgent(agent agentpool.AgentInfo, sandbox apiv1alpha1.Sandbox) int {
	score := 0
	for _, img := range agent.Images {
		if img == sandbox.Spec.Image {
			score += 100
			break
		}
	}
	return score
}

func (r *SandboxReconciler) syncAgent(ctx context.Context, agentPodName string) error {
	agentID := agentpool.AgentID(agentPodName)
	agentInfo, ok := r.Registry.GetAgentByID(agentID)
	if !ok {
		return fmt.Errorf("agent %s not found in registry", agentPodName)
	}

	var sandboxList apiv1alpha1.SandboxList
	if err := r.List(ctx, &sandboxList, client.MatchingFields{"status.assignedPod": agentPodName}); err != nil {
		return err
	}

	var specs []api.SandboxSpec
	for _, sb := range sandboxList.Items {
		envs := make(map[string]string)
		for _, e := range sb.Spec.Envs {
			envs[e.Name] = e.Value
		}
		specs = append(specs, api.SandboxSpec{
			SandboxID: sb.Name,
			ClaimUID:  string(sb.UID),
			ClaimName: sb.Name,
			Image:     sb.Spec.Image,
			Command:   sb.Spec.Command,
			Args:      sb.Spec.Args,
			Env:       envs,
		})
	}

	req := &api.SandboxesRequest{
		AgentID:      agentPodName,
		SandboxSpecs: specs,
	}

	endpoint := fmt.Sprintf("%s:8081", agentInfo.PodIP)
	return r.AgentClient.SyncSandboxes(endpoint, req)
}

// SetupWithManager sets up the controller with the Manager.
func (r *SandboxReconciler) SetupWithManager(mgr ctrl.Manager) error {
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