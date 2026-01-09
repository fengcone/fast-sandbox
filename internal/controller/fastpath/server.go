package fastpath

import (
	"context"
	"fmt"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	fastpathv1 "fast-sandbox/api/proto/v1"
	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentpool"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Server struct {
	fastpathv1.UnimplementedFastPathServiceServer
	K8sClient   client.Client
	Registry    agentpool.AgentRegistry
	AgentClient *api.AgentClient
}

func (s *Server) CreateSandbox(ctx context.Context, req *fastpathv1.CreateRequest) (*fastpathv1.CreateResponse, error) {
	// 1. 构造 Sandbox 对象用于调度（不存入 K8s）
	tempSB := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("sb-%d", time.Now().UnixNano()), // 临时 ID
			Namespace: "default",
		},
		Spec: apiv1alpha1.SandboxSpec{
			Image:        req.Image,
			PoolRef:      req.PoolRef,
			ExposedPorts: req.ExposedPorts,
			Command:      req.Command,
			Args:         req.Args,
		},
	}

	// 2. 原子调度选点
	agent, err := s.Registry.Allocate(tempSB)
	if err != nil {
		return nil, fmt.Errorf("scheduling failed: %v", err)
	}

	// 3. 直连下发指令给 Agent
	// 我们需要封装一个新的 Sync 调用，仅包含当前新沙箱
	// 实际上，为了兼容性，我们暂时构造一个只包含一个 SB 的 Sync 请求
	endpoint := fmt.Sprintf("%s:8081", agent.PodIP)
	syncReq := &api.SandboxesRequest{
		AgentID: agent.PodName,
		SandboxSpecs: []api.SandboxSpec{
			{
				SandboxID: tempSB.Name,
				ClaimUID:  string(tempSB.UID), // 暂时为空
				ClaimName: tempSB.Name,
				Image:     tempSB.Spec.Image,
				Command:   tempSB.Spec.Command,
				Args:      tempSB.Spec.Args,
			},
		},
	}

	if err := s.AgentClient.SyncSandboxes(endpoint, syncReq); err != nil {
		// 回滚
		s.Registry.Release(agent.ID, tempSB)
		return nil, fmt.Errorf("agent sync failed: %v", err)
	}

	// 4. 异步补齐 CRD
	go s.asyncCreateCRD(tempSB, agent.PodName, agent.NodeName)

	// 5. 立即返回
	var endpoints []string
	for _, p := range tempSB.Spec.ExposedPorts {
		endpoints = append(endpoints, fmt.Sprintf("%s:%d", agent.PodIP, p))
	}

	return &fastpathv1.CreateResponse{
		SandboxId: tempSB.Name,
		AgentPod:  agent.PodName,
		Endpoints: endpoints,
	}, nil
}

func (s *Server) asyncCreateCRD(sb *apiv1alpha1.Sandbox, podName, nodeName string) {
	ctx := context.Background()
	logger := log.FromContext(ctx).WithName("fast-path-async")

	// 补齐状态信息
	sb.Status = apiv1alpha1.SandboxStatus{
		Phase:       "Bound",
		AssignedPod: podName,
		NodeName:    nodeName,
	}

	// 尝试创建 CRD
	if err := s.K8sClient.Create(ctx, sb); err != nil {
		logger.Error(err, "Failed to create sandbox CRD asynchronously", "name", sb.Name)
		// 注意：如果创建失败，Janitor 最终会回收宿主机上的容器
	} else {
		logger.Info("Async CRD creation successful", "name", sb.Name)
	}
}

func (s *Server) DeleteSandbox(ctx context.Context, req *fastpathv1.DeleteRequest) (*fastpathv1.DeleteResponse, error) {
	// 直接调用 K8s Delete，由 SandboxController 的 Finalizer 处理资源释放
	// 这样可以复用已有的优雅删除逻辑
	sb := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.SandboxId,
			Namespace: "default",
		},
	}
	if err := s.K8sClient.Delete(ctx, sb); err != nil {
		return &fastpathv1.DeleteResponse{Success: false}, err
	}
	return &fastpathv1.DeleteResponse{Success: true}, nil
}
