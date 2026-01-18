package fastpath

import (
	"context"
	"fmt"
	"time"

	fastpathv1 "fast-sandbox/api/proto/v1"
	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentpool"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maxRetries = 3
)

type Server struct {
	fastpathv1.UnimplementedFastPathServiceServer
	K8sClient              client.Client
	Registry               agentpool.AgentRegistry
	AgentClient            *api.AgentClient
	DefaultConsistencyMode api.ConsistencyMode
}

// 强制编译时检查接口实现情况
var _ fastpathv1.FastPathServiceServer = &Server{}

func (s *Server) CreateSandbox(ctx context.Context, req *fastpathv1.CreateRequest) (*fastpathv1.CreateResponse, error) {
	mode := s.DefaultConsistencyMode
	if req.ConsistencyMode == fastpathv1.ConsistencyMode_STRONG {
		mode = api.ConsistencyModeStrong
	}

	sandboxName := req.Name
	if sandboxName == "" {
		sandboxName = fmt.Sprintf("sb-%d", time.Now().UnixNano())
	}

	tempSB := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandboxName,
			Namespace: req.Namespace,
		},
		Spec: apiv1alpha1.SandboxSpec{
			Image:        req.Image,
			PoolRef:      req.PoolRef,
			ExposedPorts: req.ExposedPorts,
			Command:      req.Command,
			Args:         req.Args,
		},
	}

	agent, err := s.Registry.Allocate(tempSB)
	if err != nil {
		return nil, err
	}

	if mode == api.ConsistencyModeStrong {
		return s.createStrong(ctx, tempSB, agent)
	}
	return s.createFast(ctx, tempSB, agent)
}

func (s *Server) createFast(ctx context.Context, tempSB *apiv1alpha1.Sandbox, agent *agentpool.AgentInfo) (*fastpathv1.CreateResponse, error) {
	endpoint := fmt.Sprintf("%s:8081", agent.PodIP)
	_, err := s.AgentClient.CreateSandbox(endpoint, &api.CreateSandboxRequest{
		Sandbox: api.SandboxSpec{
			SandboxID: tempSB.Name,
			ClaimName: tempSB.Name,
			Image:     tempSB.Spec.Image,
			Command:   tempSB.Spec.Command,
			Args:      tempSB.Spec.Args,
		},
	})
	if err != nil {
		s.Registry.Release(agent.ID, tempSB)
		return nil, err
	}

	asyncCtx, _ := context.WithTimeout(context.Background(), 30*time.Second)
	go s.asyncCreateCRDWithRetry(asyncCtx, tempSB, agent.ID, agent.PodName, agent.NodeName)
	return &fastpathv1.CreateResponse{SandboxId: tempSB.Name, AgentPod: agent.PodName, Endpoints: s.getEndpoints(agent.PodIP, tempSB)}, nil
}

func (s *Server) createStrong(ctx context.Context, tempSB *apiv1alpha1.Sandbox, agent *agentpool.AgentInfo) (*fastpathv1.CreateResponse, error) {
	if err := s.K8sClient.Create(ctx, tempSB); err != nil {
		s.Registry.Release(agent.ID, tempSB)
		return nil, err
	}

	endpoint := fmt.Sprintf("%s:8081", agent.PodIP)
	_, err := s.AgentClient.CreateSandbox(endpoint, &api.CreateSandboxRequest{
		Sandbox: api.SandboxSpec{
			SandboxID: tempSB.Name,
			ClaimUID:  string(tempSB.UID),
			ClaimName: tempSB.Name,
			Image:     tempSB.Spec.Image,
			Command:   tempSB.Spec.Command,
			Args:      tempSB.Spec.Args,
		},
	})
	if err != nil {
		s.K8sClient.Delete(ctx, tempSB)
		s.Registry.Release(agent.ID, tempSB)
		return nil, err
	}

	retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &apiv1alpha1.Sandbox{}
		if err := s.K8sClient.Get(ctx, client.ObjectKeyFromObject(tempSB), latest); err != nil {
			return err
		}
		latest.Status.Phase = "Bound"
		latest.Status.AssignedPod = agent.PodName
		latest.Status.NodeName = agent.NodeName
		return s.K8sClient.Status().Update(ctx, latest)
	})

	return &fastpathv1.CreateResponse{SandboxId: tempSB.Name, AgentPod: agent.PodName, Endpoints: s.getEndpoints(agent.PodIP, tempSB)}, nil
}

func (s *Server) asyncCreateCRDWithRetry(ctx context.Context, sb *apiv1alpha1.Sandbox, agentID agentpool.AgentID, podName, nodeName string) {
	for attempt := 0; attempt < maxRetries; attempt++ {
		sb.Status.Phase = "Bound"
		sb.Status.AssignedPod = podName
		sb.Status.NodeName = nodeName
		if err := s.K8sClient.Create(ctx, sb); err == nil {
			return
		}
		time.Sleep(time.Duration(1<<uint(attempt)) * time.Second)
	}
}

func (s *Server) getEndpoints(ip string, sb *apiv1alpha1.Sandbox) []string {
	var res []string
	for _, p := range sb.Spec.ExposedPorts {
		res = append(res, fmt.Sprintf("%s:%d", ip, p))
	}
	return res
}

func (s *Server) ListSandboxes(ctx context.Context, req *fastpathv1.ListRequest) (*fastpathv1.ListResponse, error) {
	namespace := req.Namespace
	// 1. 从 K8s 获取已有的 CRD
	var sbList apiv1alpha1.SandboxList
	if err := s.K8sClient.List(ctx, &sbList, client.InNamespace(namespace)); err != nil {
		return nil, err
	}

	res := &fastpathv1.ListResponse{}
	for _, sb := range sbList.Items {
		res.Items = append(res.Items, &fastpathv1.SandboxInfo{
			SandboxId: sb.Name,
			Phase:     sb.Status.Phase,
			AgentPod:  sb.Status.AssignedPod,
			Endpoints: sb.Status.Endpoints,
			Image:     sb.Spec.Image,
			PoolRef:   sb.Spec.PoolRef,
			CreatedAt: sb.CreationTimestamp.Unix(),
		})
	}

	// 2. 补充 Registry 中正在创建（Pending）但还没写 CRD 的沙箱
	// 这里目前简化处理，主要靠 CRD 对账
	return res, nil
}

func (s *Server) GetSandbox(ctx context.Context, req *fastpathv1.GetRequest) (*fastpathv1.SandboxInfo, error) {
	namespace := req.Namespace
	var sb apiv1alpha1.Sandbox
	if err := s.K8sClient.Get(ctx, client.ObjectKey{Name: req.SandboxId, Namespace: namespace}, &sb); err != nil {
		return nil, err
	}

	return &fastpathv1.SandboxInfo{
		SandboxId: sb.Name,
		Phase:     sb.Status.Phase,
		AgentPod:  sb.Status.AssignedPod,
		Endpoints: sb.Status.Endpoints,
		Image:     sb.Spec.Image,
		PoolRef:   sb.Spec.PoolRef,
		CreatedAt: sb.CreationTimestamp.Unix(),
	}, nil
}

func (s *Server) DeleteSandbox(ctx context.Context, req *fastpathv1.DeleteRequest) (*fastpathv1.DeleteResponse, error) {
	ns := req.Namespace
	sb := &apiv1alpha1.Sandbox{ObjectMeta: metav1.ObjectMeta{Name: req.SandboxId, Namespace: ns}}
	if err := s.K8sClient.Delete(ctx, sb); err != nil {
		return &fastpathv1.DeleteResponse{Success: false}, err
	}
	return &fastpathv1.DeleteResponse{Success: true}, nil
}
