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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// maxRetries 定义 CRD 创建的最大重试次数
	maxRetries = 3

	// defaultConsistencyMode 默认一致性模式：Fast
	defaultConsistencyMode = api.ConsistencyModeFast
)

type Server struct {
	fastpathv1.UnimplementedFastPathServiceServer
	K8sClient            client.Client
	Registry             agentpool.AgentRegistry
	AgentClient          *api.AgentClient
	DefaultConsistencyMode api.ConsistencyMode

	// 追踪进行中的异步创建操作，用于幂等性检查
	pendingCreations   map[string]*pendingCreation
	pendingCreationsMu sync.RWMutex
}

type pendingCreation struct {
	agentID    agentpool.AgentID
	sandbox    *apiv1alpha1.Sandbox
	cancelFunc context.CancelFunc
}

func NewServer(k8sClient client.Client, registry agentpool.AgentRegistry, agentClient *api.AgentClient) *Server {
	return &Server{
		K8sClient:            k8sClient,
		Registry:             registry,
		AgentClient:          agentClient,
		DefaultConsistencyMode: defaultConsistencyMode,
		pendingCreations:     make(map[string]*pendingCreation),
	}
}

func (s *Server) CreateSandbox(ctx context.Context, req *fastpathv1.CreateRequest) (*fastpathv1.CreateResponse, error) {
	// 确定一致性模式：优先使用请求指定的，否则使用默认值
	mode := s.DefaultConsistencyMode
	if req.ConsistencyMode == fastpathv1.ConsistencyMode_STRONG {
		mode = api.ConsistencyModeStrong
	}

	// 1. 构造 Sandbox 对象用于调度
	tempSB := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("sb-%d", time.Now().UnixNano()),
			Namespace: s.getNamespace(req.Namespace),
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

	// 3. 根据一致性模式选择创建流程
	if mode == api.ConsistencyModeStrong {
		return s.createStrong(ctx, req, tempSB, agent)
	}
	return s.createFast(ctx, req, tempSB, agent)
}

// createFast 实现快速模式：先创建 Agent，后写 CRD
func (s *Server) createFast(ctx context.Context, req *fastpathv1.CreateRequest, tempSB *apiv1alpha1.Sandbox, agent *agentpool.AgentInfo) (*fastpathv1.CreateResponse, error) {
	logger := log.FromContext(ctx).WithName("fast-path-create")

	// 1. 调用 Agent.CreateSandbox（命令式，不影响其他 sandbox）
	endpoint := fmt.Sprintf("%s:8081", agent.PodIP)
	createReq := &api.CreateSandboxRequest{
		Sandbox: api.SandboxSpec{
			SandboxID: tempSB.Name,
			ClaimUID:  string(tempSB.UID),
			ClaimName: tempSB.Name,
			Image:     tempSB.Spec.Image,
			Command:   tempSB.Spec.Command,
			Args:      tempSB.Spec.Args,
		},
	}

	_, err := s.AgentClient.CreateSandbox(endpoint, createReq)
	if err != nil {
		// Agent 创建失败，回滚 Registry 分配
		s.Registry.Release(agent.ID, tempSB)
		return nil, fmt.Errorf("agent create failed: %v", err)
	}

	// 2. 异步创建 CRD
	asyncCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go s.asyncCreateCRDWithRetry(asyncCtx, tempSB, agent.ID, agent.PodName, agent.NodeName)

	// 记录进行中的创建
	s.pendingCreationsMu.Lock()
	s.pendingCreations[tempSB.Name] = &pendingCreation{
		agentID:    agent.ID,
		sandbox:    tempSB,
		cancelFunc: cancel,
	}
	s.pendingCreationsMu.Unlock()

	logger.Info("Fast-Path create succeeded (Fast mode)", "sandbox", tempSB.Name, "agent", agent.PodName)

	// 3. 立即返回
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

// createStrong 实现强一致性模式：先写 CRD，后创建 Agent
func (s *Server) createStrong(ctx context.Context, req *fastpathv1.CreateRequest, tempSB *apiv1alpha1.Sandbox, agent *agentpool.AgentInfo) (*fastpathv1.CreateResponse, error) {
	logger := log.FromContext(ctx).WithName("fast-path-create-strong")

	// 1. 先创建 CRD (phase=Pending)
	tempSB.Status.Phase = "Pending"
	if err := s.K8sClient.Create(ctx, tempSB); err != nil {
		s.Registry.Release(agent.ID, tempSB)
		return nil, fmt.Errorf("CRD create failed: %v", err)
	}

	// 2. 调用 Agent.CreateSandbox
	endpoint := fmt.Sprintf("%s:8081", agent.PodIP)
	createReq := &api.CreateSandboxRequest{
		Sandbox: api.SandboxSpec{
			SandboxID: tempSB.Name,
			ClaimUID:  string(tempSB.UID),
			ClaimName: tempSB.Name,
			Image:     tempSB.Spec.Image,
			Command:   tempSB.Spec.Command,
			Args:      tempSB.Spec.Args,
		},
	}

	_, err := s.AgentClient.CreateSandbox(endpoint, createReq)
	if err != nil {
		// Agent 创建失败，清理 CRD 和 Registry
		s.K8sClient.Delete(ctx, tempSB)
		s.Registry.Release(agent.ID, tempSB)
		return nil, fmt.Errorf("agent create failed: %v", err)
	}

	// 3. 更新 CRD 状态 (phase=Bound)
	tempSB.Status.Phase = "Bound"
	tempSB.Status.AssignedPod = agent.PodName
	tempSB.Status.NodeName = agent.NodeName
	if err := s.K8sClient.Status().Update(ctx, tempSB); err != nil {
		logger.Error(err, "Failed to update CRD status", "sandbox", tempSB.Name)
		// 不返回错误，因为 sandbox 已经创建成功
	}

	logger.Info("Fast-Path create succeeded (Strong mode)", "sandbox", tempSB.Name, "agent", agent.PodName)

	// 4. 返回响应
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

// getNamespace 返回目标命名空间，默认为 "default"
func (s *Server) getNamespace(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

// asyncCreateCRDWithRetry 带重试机制的异步 CRD 创建
func (s *Server) asyncCreateCRDWithRetry(parentCtx context.Context, sb *apiv1alpha1.Sandbox, agentID agentpool.AgentID, podName, nodeName string) {
	logger := log.FromContext(parentCtx).WithName("fast-path-async")

	// 补齐状态信息
	sb.Status = apiv1alpha1.SandboxStatus{
		Phase:       "Bound",
		AssignedPod: podName,
		NodeName:    nodeName,
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		select {
		case <-parentCtx.Done():
			logger.Info("Context cancelled, stopping CRD creation retry", "name", sb.Name, "attempts", attempt)
			s.cleanupPendingCreation(sb.Name, agentID)
			return
		default:
		}

		// 每次重试前等待一小段时间（指数退避）
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
			select {
			case <-time.After(backoff):
			case <-parentCtx.Done():
				s.cleanupPendingCreation(sb.Name, agentID)
				return
			}
		}

		// 检查 CRD 是否已存在（幂等性检查）
		existingSB := &apiv1alpha1.Sandbox{}
		err := s.K8sClient.Get(parentCtx, client.ObjectKey{Name: sb.Name, Namespace: sb.Namespace}, existingSB)
		if err == nil {
			// CRD 已存在，更新状态而不是创建
			logger.Info("CRD already exists, updating status", "name", sb.Name)
			s.cleanupPendingCreation(sb.Name, agentID)
			return
		}

		// 尝试创建 CRD，使用带超时的 context
		createCtx, cancel := context.WithTimeout(parentCtx, 10*time.Second)
		err = s.K8sClient.Create(createCtx, sb)
		cancel()

		if err == nil {
			logger.Info("Async CRD creation successful", "name", sb.Name, "attempt", attempt+1)
			s.cleanupPendingCreation(sb.Name, agentID)
			return
		}

		lastErr = err
		logger.Error(err, "Failed to create sandbox CRD, will retry", "name", sb.Name, "attempt", attempt+1)
	}

	// 所有重试都失败
	logger.Error(lastErr, "All retries failed for CRD creation", "name", sb.Name, "maxRetries", maxRetries)
	// 清理 pending 创建记录
	s.cleanupPendingCreation(sb.Name, agentID)
	// 注意：此时 Agent 端的容器已创建，但 CRD 不存在
	// Janitor 会将其识别为 orphan 并清理
}

// cleanupPendingCreation 清理进行中的创建记录
func (s *Server) cleanupPendingCreation(sandboxName string, agentID agentpool.AgentID) {
	s.pendingCreationsMu.Lock()
	defer s.pendingCreationsMu.Unlock()
	if _, ok := s.pendingCreations[sandboxName]; ok {
		delete(s.pendingCreations, sandboxName)
	}
}

func (s *Server) DeleteSandbox(ctx context.Context, req *fastpathv1.DeleteRequest) (*fastpathv1.DeleteResponse, error) {
	// 直接调用 K8s Delete，由 SandboxController 的 Finalizer 处理资源释放
	// 这样可以复用已有的优雅删除逻辑
	namespace := req.Namespace
	if namespace == "" {
		namespace = "default"
	}
	sb := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.SandboxId,
			Namespace: namespace,
		},
	}
	if err := s.K8sClient.Delete(ctx, sb); err != nil {
		return &fastpathv1.DeleteResponse{Success: false}, err
	}
	return &fastpathv1.DeleteResponse{Success: true}, nil
}
