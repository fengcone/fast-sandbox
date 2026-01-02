package controller

import (
	"context"
	"fmt"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentclient"
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
	Scheme      *runtime.Scheme
	Ctx         context.Context
	Registry    agentpool.AgentRegistry
	Scheduler   scheduler.Scheduler
	AgentClient *agentclient.AgentClient
}

// Reconcile is the main reconciliation loop.
func (r *SandboxClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var claim apiv1alpha1.SandboxClaim
	if err := r.Get(ctx, req.NamespacedName, &claim); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 处理 Pending 或空 Phase: 调度 Agent
	if claim.Status.Phase == "" || claim.Status.Phase == "Pending" {
		agents := r.Registry.GetAllAgents()
		agent, err := r.Scheduler.Schedule(ctx, &claim, agents)
		if err != nil {
			logger.Info("No available agent, requeuing", "claim", claim.Name)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		// 记录调度结果
		claim.Status.AssignedAgentPod = agent.PodName
		claim.Status.NodeName = agent.NodeName
		claim.Status.Phase = "Scheduling"

		if err := r.Status().Update(ctx, &claim); err != nil {
			return ctrl.Result{}, err
		}

		// 继续处理，进入 Allocating 阶段
		return ctrl.Result{Requeue: true}, nil
	}

	// 处理 Scheduling Phase: 调用 Agent 创建 Sandbox
	if claim.Status.Phase == "Scheduling" {
		agentID := agentpool.AgentID(claim.Status.AssignedAgentPod)
		agent, ok := r.Registry.GetAgentByID(agentID)
		if !ok {
			logger.Error(fmt.Errorf("agent not found"), "Agent disappeared", "agentID", agentID)
			claim.Status.Phase = "Failed"
			return ctrl.Result{}, r.Status().Update(ctx, &claim)
		}

		// 调用 Agent 创建 Sandbox
		createReq := &api.CreateSandboxRequest{
			ClaimUID:  string(claim.UID),
			ClaimName: claim.Name,
			Image:     claim.Spec.Image,
			CPU:       claim.Spec.CPU,
			Memory:    claim.Spec.Memory,
			Port:      claim.Spec.Port,
			Command:   claim.Spec.Command,
			Args:      claim.Spec.Args,
			Env:       claim.Spec.Env,
		}

		logger.Info("Creating sandbox on agent", "agentIP", agent.PodIP, "claim", claim.Name)
		createResp, err := r.AgentClient.CreateSandbox(agent.PodIP, 8081, createReq)
		if err != nil {
			logger.Error(err, "Failed to create sandbox", "claim", claim.Name)
			claim.Status.Phase = "Failed"
			return ctrl.Result{}, r.Status().Update(ctx, &claim)
		}

		if !createResp.Success {
			logger.Error(fmt.Errorf("sandbox creation failed"), createResp.Message, "claim", claim.Name)
			claim.Status.Phase = "Failed"
			return ctrl.Result{}, r.Status().Update(ctx, &claim)
		}

		// 更新状态为 Running
		claim.Status.SandboxID = createResp.SandboxID
		claim.Status.Address = fmt.Sprintf("%s:%d", agent.PodIP, createResp.Port)
		claim.Status.Phase = "Running"

		logger.Info("Sandbox created successfully", "claim", claim.Name, "sandboxID", createResp.SandboxID)

		// 分配 slot
		r.Registry.AllocateSlot(agent.ID)

		if err := r.Status().Update(ctx, &claim); err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SandboxClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.SandboxClaim{}).
		Complete(r)
}
