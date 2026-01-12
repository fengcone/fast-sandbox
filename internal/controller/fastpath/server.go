package fastpath

import (
	"context"
	"fmt"
	"sync"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	fastpathv1 "fast-sandbox/api/proto/v1"
	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentpool"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maxRetries = 3
	defaultConsistencyMode = api.ConsistencyModeFast
)

type Server struct {
	fastpathv1.UnimplementedFastPathServiceServer
	K8sClient            client.Client
	Registry             agentpool.AgentRegistry
	AgentClient          *api.AgentClient
	DefaultConsistencyMode api.ConsistencyMode
	pendingCreations   map[string]*pendingCreation
	pendingCreationsMu sync.RWMutex
}

type pendingCreation struct {
	agentID    agentpool.AgentID
	sandbox    *apiv1alpha1.Sandbox
	cancelFunc context.CancelFunc
}

func (s *Server) CreateSandbox(ctx context.Context, req *fastpathv1.CreateRequest) (*fastpathv1.CreateResponse, error) {
	s.pendingCreationsMu.Lock()
	if s.pendingCreations == nil { s.pendingCreations = make(map[string]*pendingCreation) }
	s.pendingCreationsMu.Unlock()

	mode := s.DefaultConsistencyMode
	if req.ConsistencyMode == fastpathv1.ConsistencyMode_STRONG { mode = api.ConsistencyModeStrong }

	sandboxName := req.Name
	if sandboxName == "" { sandboxName = fmt.Sprintf("sb-%d", time.Now().UnixNano()) }

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
	if tempSB.Namespace == "" { tempSB.Namespace = "default" }

	agent, err := s.Registry.Allocate(tempSB)
	if err != nil { return nil, err }

	if mode == api.ConsistencyModeStrong {
		return s.createStrong(ctx, req, tempSB, agent)
	}
	return s.createFast(ctx, req, tempSB, agent)
}

func (s *Server) createFast(ctx context.Context, req *fastpathv1.CreateRequest, tempSB *apiv1alpha1.Sandbox, agent *agentpool.AgentInfo) (*fastpathv1.CreateResponse, error) {
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

func (s *Server) createStrong(ctx context.Context, req *fastpathv1.CreateRequest, tempSB *apiv1alpha1.Sandbox, agent *agentpool.AgentInfo) (*fastpathv1.CreateResponse, error) {
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
		if err := s.K8sClient.Get(ctx, client.ObjectKeyFromObject(tempSB), latest); err != nil { return err }
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
		if err := s.K8sClient.Create(ctx, sb); err == nil { return }
		time.Sleep(time.Duration(1<<uint(attempt)) * time.Second)
	}
}

func (s *Server) getEndpoints(ip string, sb *apiv1alpha1.Sandbox) []string {
	var res []string
	for _, p := range sb.Spec.ExposedPorts { res = append(res, fmt.Sprintf("%s:%d", ip, p)) }
	return res
}

func (s *Server) DeleteSandbox(ctx context.Context, req *fastpathv1.DeleteRequest) (*fastpathv1.DeleteResponse, error) {
	ns := req.Namespace
	if ns == "" { ns = "default" }
	sb := &apiv1alpha1.Sandbox{ObjectMeta: metav1.ObjectMeta{Name: req.SandboxId, Namespace: ns}}
	if err := s.K8sClient.Delete(ctx, sb); err != nil { return &fastpathv1.DeleteResponse{Success: false}, err }
	return &fastpathv1.DeleteResponse{Success: true}, nil
}