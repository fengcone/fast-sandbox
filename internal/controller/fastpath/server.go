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

// maxRetries 定义 CRD 创建的最大重试次数
const maxRetries = 3

type Server struct {
	fastpathv1.UnimplementedFastPathServiceServer
	K8sClient   client.Client
	Registry    agentpool.AgentRegistry
	AgentClient *api.AgentClient

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
		K8sClient:        k8sClient,
		Registry:         registry,
		AgentClient:      agentClient,
		pendingCreations: make(map[string]*pendingCreation),
	}
}

func (s *Server) CreateSandbox(ctx context.Context, req *fastpathv1.CreateRequest) (*fastpathv1.CreateResponse, error) {
	// 1. 构造 Sandbox 对象用于调度（不存入 K8s）
	tempSB := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("sb-%d", time.Now().UnixNano()), // 临时 ID
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
		// Agent 同步失败，回滚 Registry 分配
		s.Registry.Release(agent.ID, tempSB)
		return nil, fmt.Errorf("agent sync failed: %v", err)
	}

	// 4. 启动异步 CRD 创建，使用带超时和取消的 context
	asyncCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go s.asyncCreateCRDWithRetry(asyncCtx, tempSB, agent.ID, agent.PodName, agent.NodeName)

	// 记录进行中的创建，用于后续可能的取消
	s.pendingCreationsMu.Lock()
	s.pendingCreations[tempSB.Name] = &pendingCreation{
		agentID:    agent.ID,
		sandbox:    tempSB,
		cancelFunc: cancel,
	}
	s.pendingCreationsMu.Unlock()

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
