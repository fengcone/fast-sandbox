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
		return r.handleScheduling(ctx, &sandbox)
	}

	// 1.1 校验绑定的 Agent 是否依然存在
	if _, ok := r.Registry.GetAgentByID(agentpool.AgentID(sandbox.Status.AssignedPod)); !ok {
		logger.Info("Assigned agent disappeared, resetting for re-scheduling", "agent", sandbox.Status.AssignedPod)
		sandbox.Status.AssignedPod = ""
		sandbox.Status.NodeName = ""
		sandbox.Status.Phase = "Pending"
		if err := r.Status().Update(ctx, &sandbox); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 2. 同步状态到 Agent
	if err := r.syncAgent(ctx, sandbox.Status.AssignedPod); err != nil {
		logger.Error(err, "Failed to sync with agent", "agent", sandbox.Status.AssignedPod)
		return ctrl.Result{Requeue: true}, nil
	}

	// 3. 从 Registry 同步运行状态回 CR (Bound -> Running)
	if err := r.updateStatusFromRegistry(ctx, &sandbox); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *SandboxReconciler) handleScheduling(ctx context.Context, sandbox *apiv1alpha1.Sandbox) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 实时调度
	agent, err := r.schedule(*sandbox)
	if err != nil {
		logger.Error(err, "Failed to schedule sandbox")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
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

	if status, ok := agentInfo.SandboxStatuses[sandbox.Name]; ok {
		if sandbox.Status.Phase != status.Phase || sandbox.Status.SandboxID != status.SandboxID {
			sandbox.Status.Phase = status.Phase
			sandbox.Status.SandboxID = status.SandboxID
			return r.Status().Update(ctx, sandbox)
		}
	}
	return nil
}

func (r *SandboxReconciler) schedule(sandbox apiv1alpha1.Sandbox) (*agentpool.AgentInfo, error) {
	agents := r.Registry.GetAllAgents()
	if len(agents) == 0 {
		return nil, fmt.Errorf("no agents available")
	}

	// 1. 实时统计已占用的插槽 (Bypass Registry Memory)
	var allSandboxes apiv1alpha1.SandboxList
	if err := r.List(context.Background(), &allSandboxes); err != nil {
		return nil, err
	}
	usedMap := make(map[string]int)
	for _, sb := range allSandboxes.Items {
		if sb.Status.AssignedPod != "" && sb.Name != sandbox.Name {
			usedMap[sb.Status.AssignedPod]++
		}
	}

	// 2. 过滤与打分
	var candidates []agentpool.AgentInfo
	for _, a := range agents {
		// Pool 匹配（poolRef 必填，必须精确匹配）
		if a.PoolName != sandbox.Spec.PoolRef {
			continue
		}

		// 容量检查 (基于实时统计)
		allocated := usedMap[a.PodName]
		if allocated >= a.Capacity {
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