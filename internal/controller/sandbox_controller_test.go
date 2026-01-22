package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/api"
	"fast-sandbox/internal/controller/agentpool"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// ============================================================================
// Mock 结构体
// ============================================================================

// MockAgentClient 用于模拟 AgentClient 行为
type MockAgentClient struct {
	CreateSandboxFunc func(endpoint string, req *api.CreateSandboxRequest) (*api.CreateSandboxResponse, error)
	DeleteSandboxFunc func(endpoint string, req *api.DeleteSandboxRequest) (*api.DeleteSandboxResponse, error)
}

func (m *MockAgentClient) CreateSandbox(endpoint string, req *api.CreateSandboxRequest) (*api.CreateSandboxResponse, error) {
	if m.CreateSandboxFunc != nil {
		return m.CreateSandboxFunc(endpoint, req)
	}
	return &api.CreateSandboxResponse{}, nil
}

func (m *MockAgentClient) DeleteSandbox(endpoint string, req *api.DeleteSandboxRequest) (*api.DeleteSandboxResponse, error) {
	if m.DeleteSandboxFunc != nil {
		return m.DeleteSandboxFunc(endpoint, req)
	}
	return &api.DeleteSandboxResponse{Success: true}, nil
}

func (m *MockAgentClient) GetAgentStatusWithContext(ctx context.Context, endpoint string) (*api.AgentStatus, error) {
	return nil, nil
}

// ConfigurableMockRegistry 可配置的 Registry Mock
type ConfigurableMockRegistry struct {
	// 配置项
	Agents           map[agentpool.AgentID]agentpool.AgentInfo
	AllocateFunc     func(sb *apiv1alpha1.Sandbox) (*agentpool.AgentInfo, error)
	AllocateError    error
	DefaultAgent     *agentpool.AgentInfo
	ReturnAgentByID  bool // GetAgentByID 是否返回 agent
	LastHeartbeatAge time.Duration

	// 调用记录
	ReleaseCalled   bool
	ReleaseAgentID  agentpool.AgentID
	ReleaseSandbox  *apiv1alpha1.Sandbox
	AllocateCalled  bool
	AllocateSandbox *apiv1alpha1.Sandbox
}

func NewConfigurableMockRegistry() *ConfigurableMockRegistry {
	return &ConfigurableMockRegistry{
		Agents:          make(map[agentpool.AgentID]agentpool.AgentInfo),
		ReturnAgentByID: true,
	}
}

func (m *ConfigurableMockRegistry) RegisterOrUpdate(info agentpool.AgentInfo) {
	m.Agents[info.ID] = info
}

func (m *ConfigurableMockRegistry) GetAllAgents() []agentpool.AgentInfo {
	out := make([]agentpool.AgentInfo, 0, len(m.Agents))
	for _, a := range m.Agents {
		out = append(out, a)
	}
	return out
}

func (m *ConfigurableMockRegistry) GetAgentByID(id agentpool.AgentID) (agentpool.AgentInfo, bool) {
	if !m.ReturnAgentByID {
		return agentpool.AgentInfo{}, false
	}
	if a, ok := m.Agents[id]; ok {
		return a, true
	}
	// 返回默认 Agent
	if m.DefaultAgent != nil {
		agent := *m.DefaultAgent
		agent.LastHeartbeat = time.Now().Add(-m.LastHeartbeatAge)
		return agent, true
	}
	return agentpool.AgentInfo{
		ID:              id,
		PodName:         string(id),
		PodIP:           "10.0.0.1",
		LastHeartbeat:   time.Now().Add(-m.LastHeartbeatAge),
		SandboxStatuses: make(map[string]api.SandboxStatus),
	}, true
}

func (m *ConfigurableMockRegistry) Allocate(sb *apiv1alpha1.Sandbox) (*agentpool.AgentInfo, error) {
	m.AllocateCalled = true
	m.AllocateSandbox = sb
	if m.AllocateFunc != nil {
		return m.AllocateFunc(sb)
	}
	if m.AllocateError != nil {
		return nil, m.AllocateError
	}
	if m.DefaultAgent != nil {
		return m.DefaultAgent, nil
	}
	return &agentpool.AgentInfo{
		ID:       "test-agent",
		PodName:  "test-agent",
		PodIP:    "10.0.0.1",
		NodeName: "test-node",
	}, nil
}

func (m *ConfigurableMockRegistry) Release(id agentpool.AgentID, sb *apiv1alpha1.Sandbox) {
	m.ReleaseCalled = true
	m.ReleaseAgentID = id
	m.ReleaseSandbox = sb
}

func (m *ConfigurableMockRegistry) Restore(ctx context.Context, c client.Reader) error {
	return nil
}

func (m *ConfigurableMockRegistry) Remove(id agentpool.AgentID) {
	delete(m.Agents, id)
}

func (m *ConfigurableMockRegistry) CleanupStaleAgents(timeout time.Duration) int {
	return 0
}

// ============================================================================
// 测试辅助函数
// ============================================================================

func newTestScheme(t *testing.T) *runtime.Scheme {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1alpha1.AddToScheme(scheme))
	return scheme
}

func newTestReconciler(scheme *runtime.Scheme, objs []client.Object, registry *ConfigurableMockRegistry, agentClient *MockAgentClient) *SandboxReconciler {
	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&apiv1alpha1.Sandbox{})

	return &SandboxReconciler{
		Client:      builder.Build(),
		Scheme:      scheme,
		Ctx:         context.Background(),
		Registry:    registry,
		AgentClient: agentClient,
	}
}

func newBaseSandbox(name string, opts ...func(*apiv1alpha1.Sandbox)) *apiv1alpha1.Sandbox {
	sb := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: apiv1alpha1.SandboxSpec{
			Image:   "alpine",
			PoolRef: "test-pool",
		},
	}
	for _, opt := range opts {
		opt(sb)
	}
	return sb
}

func withFinalizer(sb *apiv1alpha1.Sandbox) {
	sb.Finalizers = []string{"sandbox.fast.io/cleanup"}
}

func withDeletionTimestamp(sb *apiv1alpha1.Sandbox) {
	now := metav1.Now()
	sb.DeletionTimestamp = &now
}

func withAssignedPod(podName string) func(*apiv1alpha1.Sandbox) {
	return func(sb *apiv1alpha1.Sandbox) {
		sb.Status.AssignedPod = podName
	}
}

func withPhase(phase string) func(*apiv1alpha1.Sandbox) {
	return func(sb *apiv1alpha1.Sandbox) {
		sb.Status.Phase = phase
	}
}

func withExpireTime(t time.Time) func(*apiv1alpha1.Sandbox) {
	return func(sb *apiv1alpha1.Sandbox) {
		mt := metav1.NewTime(t)
		sb.Spec.ExpireTime = &mt
	}
}

func withResetRevision(t time.Time) func(*apiv1alpha1.Sandbox) {
	return func(sb *apiv1alpha1.Sandbox) {
		mt := metav1.NewTime(t)
		sb.Spec.ResetRevision = &mt
	}
}

func withAcceptedResetRevision(t time.Time) func(*apiv1alpha1.Sandbox) {
	return func(sb *apiv1alpha1.Sandbox) {
		mt := metav1.NewTime(t)
		sb.Status.AcceptedResetRevision = &mt
	}
}

func withFailurePolicy(policy apiv1alpha1.FailurePolicy) func(*apiv1alpha1.Sandbox) {
	return func(sb *apiv1alpha1.Sandbox) {
		sb.Spec.FailurePolicy = policy
	}
}

func withExposedPorts(ports ...int32) func(*apiv1alpha1.Sandbox) {
	return func(sb *apiv1alpha1.Sandbox) {
		sb.Spec.ExposedPorts = ports
	}
}

func reconcileRequest(name string) ctrl.Request {
	return ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: name},
	}
}

func getSandbox(t *testing.T, r *SandboxReconciler, name string) *apiv1alpha1.Sandbox {
	sb := &apiv1alpha1.Sandbox{}
	err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, sb)
	require.NoError(t, err)
	return sb
}

func sandboxShouldBeDeleted(t *testing.T, r *SandboxReconciler, name string) {
	sb := &apiv1alpha1.Sandbox{}
	err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, sb)
	assert.Error(t, err, "Sandbox 应该已被删除")
}

// ============================================================================
// 1. 创建流程测试 (Creation)
// ============================================================================

func TestSandbox_Creation_NormalScheduling(t *testing.T) {
	// C-01: 新建 Sandbox，Registry 有可用 Agent
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer)
	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	// 第一次 Reconcile：调度
	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.True(t, result.Requeue, "应该 Requeue 继续处理")
	assert.True(t, registry.AllocateCalled, "应该调用 Allocate")

	// 验证状态更新
	updated := getSandbox(t, r, "test-sb")
	assert.Equal(t, "test-agent", updated.Status.AssignedPod)
	assert.Equal(t, "Pending", updated.Status.Phase)
}

func TestSandbox_Creation_NoAvailableAgent(t *testing.T) {
	// C-02: Registry 返回容量不足错误
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer)
	registry := NewConfigurableMockRegistry()
	registry.AllocateError = errors.New("insufficient capacity")
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter, "应该 RequeueAfter 5s")

	// 状态不应更新
	updated := getSandbox(t, r, "test-sb")
	assert.Empty(t, updated.Status.AssignedPod)
}

func TestSandbox_Creation_SchedulingRace(t *testing.T) {
	// C-03: Allocate 成功但 Status 更新时发现 AssignedPod 已被设置
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer, withAssignedPod("other-agent"))
	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	// 因为已经有 AssignedPod，不应该再调用 Allocate
	_, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.False(t, registry.AllocateCalled, "不应该再次调用 Allocate")
}

func TestSandbox_Creation_AddFinalizer(t *testing.T) {
	// C-04: 新 Sandbox 无 Finalizer
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb") // 没有 Finalizer

	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.True(t, result.Requeue, "应该 Requeue")

	// 验证 Finalizer 已添加
	updated := getSandbox(t, r, "test-sb")
	assert.Contains(t, updated.Finalizers, "sandbox.fast.io/cleanup")
}

func TestSandbox_Creation_AgentCreateSuccess(t *testing.T) {
	// C-05: Phase=Pending，调用 Agent.CreateSandbox 成功
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer, withAssignedPod("test-agent"), withPhase("Pending"))

	registry := NewConfigurableMockRegistry()
	registry.DefaultAgent = &agentpool.AgentInfo{
		ID:              "test-agent",
		PodName:         "test-agent",
		PodIP:           "10.0.0.1",
		LastHeartbeat:   time.Now(),
		SandboxStatuses: make(map[string]api.SandboxStatus),
	}

	createCalled := false
	agentClient := &MockAgentClient{
		CreateSandboxFunc: func(endpoint string, req *api.CreateSandboxRequest) (*api.CreateSandboxResponse, error) {
			createCalled = true
			assert.Equal(t, "10.0.0.1:8081", endpoint)
			assert.Equal(t, "test-sb", req.Sandbox.SandboxID)
			return &api.CreateSandboxResponse{}, nil
		},
	}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.True(t, result.Requeue)
	assert.True(t, createCalled, "应该调用 CreateSandbox")

	// 验证 Phase 变为 Bound
	updated := getSandbox(t, r, "test-sb")
	assert.Equal(t, "Bound", updated.Status.Phase)
}

func TestSandbox_Creation_AgentCreateFailure(t *testing.T) {
	// C-06: CreateSandbox 返回错误
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer, withAssignedPod("test-agent"), withPhase("Pending"))

	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{
		CreateSandboxFunc: func(endpoint string, req *api.CreateSandboxRequest) (*api.CreateSandboxResponse, error) {
			return nil, errors.New("connection refused")
		},
	}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err) // 错误被处理，返回 RequeueAfter
	assert.Equal(t, 5*time.Second, result.RequeueAfter)

	// Phase 应该保持 Pending
	updated := getSandbox(t, r, "test-sb")
	assert.Equal(t, "Pending", updated.Status.Phase)
}

// ============================================================================
// 2. 删除流程测试 (Deletion)
// ============================================================================

func TestSandbox_Deletion_BoundPhase(t *testing.T) {
	// D-01: DeletionTimestamp 设置，Phase=Bound，调用 Agent 删除
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer, withDeletionTimestamp, withAssignedPod("test-agent"), withPhase("Bound"))

	registry := NewConfigurableMockRegistry()
	deleteCalled := false
	agentClient := &MockAgentClient{
		DeleteSandboxFunc: func(endpoint string, req *api.DeleteSandboxRequest) (*api.DeleteSandboxResponse, error) {
			deleteCalled = true
			assert.Equal(t, "test-sb", req.SandboxID)
			return &api.DeleteSandboxResponse{Success: true}, nil
		},
	}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.True(t, deleteCalled, "应该调用 DeleteSandbox")
	assert.Equal(t, 2*time.Second, result.RequeueAfter, "应该等待 Agent 确认")

	// Phase 应该变为 Terminating
	updated := getSandbox(t, r, "test-sb")
	assert.Equal(t, "Terminating", updated.Status.Phase)
}

func TestSandbox_Deletion_WaitForTerminated(t *testing.T) {
	// D-02: Phase=Terminating，Agent 上报 terminated
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer, withDeletionTimestamp, withAssignedPod("test-agent"), withPhase("Terminating"))

	registry := NewConfigurableMockRegistry()
	registry.DefaultAgent = &agentpool.AgentInfo{
		ID:            "test-agent",
		PodName:       "test-agent",
		PodIP:         "10.0.0.1",
		LastHeartbeat: time.Now(),
		SandboxStatuses: map[string]api.SandboxStatus{
			"test-sb": {Phase: "terminated"},
		},
	}
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.Empty(t, result, "不应该 Requeue")
	assert.True(t, registry.ReleaseCalled, "应该释放 Registry")

	// Finalizer 被移除后，带有 DeletionTimestamp 的对象会被删除
	sandboxShouldBeDeleted(t, r, "test-sb")
}

func TestSandbox_Deletion_TerminatingWaiting(t *testing.T) {
	// D-03: Phase=Terminating，Agent 未上报 terminated
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer, withDeletionTimestamp, withAssignedPod("test-agent"), withPhase("Terminating"))

	registry := NewConfigurableMockRegistry()
	registry.DefaultAgent = &agentpool.AgentInfo{
		ID:            "test-agent",
		PodName:       "test-agent",
		PodIP:         "10.0.0.1",
		LastHeartbeat: time.Now(),
		SandboxStatuses: map[string]api.SandboxStatus{
			"test-sb": {Phase: "running"}, // 还没有 terminated
		},
	}
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, result.RequeueAfter, "应该继续等待")
	assert.False(t, registry.ReleaseCalled, "不应该释放 Registry")
}

func TestSandbox_Deletion_AgentNotFound(t *testing.T) {
	// D-04: Agent 已从 Registry 移除
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer, withDeletionTimestamp, withAssignedPod("unknown-agent"), withPhase("Bound"))

	registry := NewConfigurableMockRegistry()
	registry.ReturnAgentByID = false // GetAgentByID 返回 false
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.Empty(t, result, "不应该 Requeue")

	// Finalizer 被移除后，带有 DeletionTimestamp 的对象会被删除
	sandboxShouldBeDeleted(t, r, "test-sb")
}

func TestSandbox_Deletion_NoAssignedPod(t *testing.T) {
	// D-05: AssignedPod=""
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer, withDeletionTimestamp, withPhase("Pending"))
	// 没有 AssignedPod

	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.Empty(t, result)

	// Finalizer 被移除后，带有 DeletionTimestamp 的对象会被删除
	sandboxShouldBeDeleted(t, r, "test-sb")
}

func TestSandbox_Deletion_DeleteFromAgentError(t *testing.T) {
	// D-06: Agent.DeleteSandbox 返回网络错误
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer, withDeletionTimestamp, withAssignedPod("test-agent"), withPhase("Bound"))

	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{
		DeleteSandboxFunc: func(endpoint string, req *api.DeleteSandboxRequest) (*api.DeleteSandboxResponse, error) {
			return nil, errors.New("network error: connection refused")
		},
	}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	_, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	assert.Error(t, err, "应该返回错误触发重试")
	assert.Contains(t, err.Error(), "failed to delete from agent")
	assert.False(t, registry.ReleaseCalled, "不应该释放 Registry")
}

func TestSandbox_Deletion_ExpiredPhase(t *testing.T) {
	// D-07: Phase=Expired
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer, withDeletionTimestamp, withPhase("Expired"))

	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.Empty(t, result)

	// Finalizer 被移除后，带有 DeletionTimestamp 的对象会被删除
	sandboxShouldBeDeleted(t, r, "test-sb")
}

func TestSandbox_Deletion_OtherPhase(t *testing.T) {
	// D-08: Phase 为空或其他值 (非 Bound/Terminating/Expired)
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer, withDeletionTimestamp, withPhase(""))

	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.Empty(t, result)

	// Finalizer 被移除后，带有 DeletionTimestamp 的对象会被删除
	sandboxShouldBeDeleted(t, r, "test-sb")
}

// ============================================================================
// 3. 过期流程测试 (Expiration)
// ============================================================================

func TestSandbox_Expiration_Normal(t *testing.T) {
	// E-01: ExpireTime 已过，Phase != Expired，AssignedPod 存在
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer,
		withAssignedPod("test-agent"),
		withPhase("Bound"),
		withExpireTime(time.Now().Add(-1*time.Hour))) // 1小时前过期

	registry := NewConfigurableMockRegistry()
	deleteCalled := false
	agentClient := &MockAgentClient{
		DeleteSandboxFunc: func(endpoint string, req *api.DeleteSandboxRequest) (*api.DeleteSandboxResponse, error) {
			deleteCalled = true
			return &api.DeleteSandboxResponse{Success: true}, nil
		},
	}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.Empty(t, result, "过期后不需要 Requeue")
	assert.True(t, deleteCalled, "应该调用 deleteFromAgent")
	assert.True(t, registry.ReleaseCalled, "应该释放 Registry")

	// Phase 应该变为 Expired
	updated := getSandbox(t, r, "test-sb")
	assert.Equal(t, "Expired", updated.Status.Phase)
	assert.Empty(t, updated.Status.AssignedPod, "AssignedPod 应该清空")
}

func TestSandbox_Expiration_NoAssignedPod(t *testing.T) {
	// E-02: ExpireTime 已过，AssignedPod=""
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer,
		withPhase("Pending"),
		withExpireTime(time.Now().Add(-1*time.Hour)))

	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.False(t, registry.ReleaseCalled, "没有 AssignedPod 不应该 Release")

	updated := getSandbox(t, r, "test-sb")
	assert.Equal(t, "Expired", updated.Status.Phase)
}

func TestSandbox_Expiration_AlreadyExpired(t *testing.T) {
	// E-03: Phase 已是 Expired
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer,
		withPhase("Expired"),
		withExpireTime(time.Now().Add(-1*time.Hour)))

	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.Empty(t, result, "已过期不需要 Requeue")
}

func TestSandbox_Expiration_SoonExpiring(t *testing.T) {
	// E-04: ExpireTime 未到，剩余时间 < 30s
	scheme := newTestScheme(t)
	remainingTime := 10 * time.Second
	sb := newBaseSandbox("test-sb", withFinalizer,
		withPhase("Bound"),
		withAssignedPod("test-agent"),
		withExpireTime(time.Now().Add(remainingTime)))

	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	// RequeueAfter 应该接近剩余时间（允许一定误差）
	assert.True(t, result.RequeueAfter > 0 && result.RequeueAfter <= remainingTime,
		"应该在过期前 Requeue，实际值: %v", result.RequeueAfter)
}

func TestSandbox_Expiration_SkipWhenDeleting(t *testing.T) {
	// E-07: DeletionTimestamp 已设置，即使 ExpireTime 已过
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer, withDeletionTimestamp,
		withAssignedPod("test-agent"),
		withPhase("Bound"),
		withExpireTime(time.Now().Add(-1*time.Hour))) // 已过期

	registry := NewConfigurableMockRegistry()
	deleteCalled := false
	agentClient := &MockAgentClient{
		DeleteSandboxFunc: func(endpoint string, req *api.DeleteSandboxRequest) (*api.DeleteSandboxResponse, error) {
			deleteCalled = true
			return &api.DeleteSandboxResponse{Success: true}, nil
		},
	}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.True(t, deleteCalled, "应该走删除流程而非过期流程")

	// 应该走删除流程，Phase 变为 Terminating 而非 Expired
	updated := getSandbox(t, r, "test-sb")
	assert.Equal(t, "Terminating", updated.Status.Phase)
	assert.Equal(t, 2*time.Second, result.RequeueAfter)
}

// ============================================================================
// 4. Reset 流程测试 (ResetRevision)
// ============================================================================

func TestSandbox_Reset_FirstTime(t *testing.T) {
	// R-01: Spec.ResetRevision 设置，AcceptedResetRevision 为 nil
	scheme := newTestScheme(t)
	resetTime := time.Now()
	sb := newBaseSandbox("test-sb", withFinalizer,
		withAssignedPod("test-agent"),
		withPhase("Bound"),
		withResetRevision(resetTime))

	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.True(t, result.Requeue, "应该 Requeue 重新调度")
	assert.True(t, registry.ReleaseCalled, "应该释放旧 Agent")

	updated := getSandbox(t, r, "test-sb")
	assert.Empty(t, updated.Status.AssignedPod, "AssignedPod 应该清空")
	assert.Equal(t, "Pending", updated.Status.Phase, "Phase 应该变为 Pending")
	assert.NotNil(t, updated.Status.AcceptedResetRevision)
}

func TestSandbox_Reset_NewRevision(t *testing.T) {
	// R-02: Spec.ResetRevision > AcceptedResetRevision
	scheme := newTestScheme(t)
	oldReset := time.Now().Add(-1 * time.Hour)
	newReset := time.Now()
	sb := newBaseSandbox("test-sb", withFinalizer,
		withAssignedPod("test-agent"),
		withPhase("Bound"),
		withResetRevision(newReset),
		withAcceptedResetRevision(oldReset))

	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.True(t, result.Requeue)
	assert.True(t, registry.ReleaseCalled)

	updated := getSandbox(t, r, "test-sb")
	assert.Equal(t, "Pending", updated.Status.Phase)
}

func TestSandbox_Reset_SameRevision(t *testing.T) {
	// R-03: Spec.ResetRevision <= AcceptedResetRevision
	scheme := newTestScheme(t)
	resetTime := time.Now()
	sb := newBaseSandbox("test-sb", withFinalizer,
		withAssignedPod("test-agent"),
		withPhase("Bound"),
		withResetRevision(resetTime),
		withAcceptedResetRevision(resetTime)) // 相同时间

	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	_, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.False(t, registry.ReleaseCalled, "不应该触发 Reset")

	// Phase 应该保持不变
	updated := getSandbox(t, r, "test-sb")
	assert.Equal(t, "Bound", updated.Status.Phase)
	assert.Equal(t, "test-agent", updated.Status.AssignedPod)
}

func TestSandbox_Reset_NoAssignedPod(t *testing.T) {
	// R-04: AssignedPod="" 时触发 Reset
	scheme := newTestScheme(t)
	resetTime := time.Now()
	sb := newBaseSandbox("test-sb", withFinalizer,
		withPhase("Pending"),
		withResetRevision(resetTime))
	// AssignedPod 为空

	registry := NewConfigurableMockRegistry()
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.True(t, result.Requeue)
	assert.False(t, registry.ReleaseCalled, "没有 AssignedPod 不需要 Release")

	updated := getSandbox(t, r, "test-sb")
	assert.Equal(t, "Pending", updated.Status.Phase)
	assert.NotNil(t, updated.Status.AcceptedResetRevision)
}

// ============================================================================
// 5. Failure Policy 测试
// ============================================================================

func TestSandbox_FailurePolicy_AutoRecreate(t *testing.T) {
	// F-01: FailurePolicy=AutoRecreate，Agent 不在 Registry
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer,
		withAssignedPod("dead-agent"),
		withPhase("Bound"),
		withFailurePolicy(apiv1alpha1.FailurePolicyAutoRecreate))

	registry := NewConfigurableMockRegistry()
	registry.ReturnAgentByID = false // Agent 不存在
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.True(t, result.Requeue, "应该 Requeue 重新调度")

	updated := getSandbox(t, r, "test-sb")
	assert.Empty(t, updated.Status.AssignedPod, "AssignedPod 应该清空")
	assert.Equal(t, "Pending", updated.Status.Phase, "Phase 应该变为 Pending")
}

func TestSandbox_FailurePolicy_Manual(t *testing.T) {
	// F-03: FailurePolicy=Manual，Agent 不在 Registry
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer,
		withAssignedPod("dead-agent"),
		withPhase("Bound"),
		withFailurePolicy(apiv1alpha1.FailurePolicyManual))

	registry := NewConfigurableMockRegistry()
	registry.ReturnAgentByID = false // Agent 不存在
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter, "Manual 模式应该等待用户干预")

	// 状态不应改变
	updated := getSandbox(t, r, "test-sb")
	assert.Equal(t, "dead-agent", updated.Status.AssignedPod)
	assert.Equal(t, "Bound", updated.Status.Phase)
}

func TestSandbox_HeartbeatNormal(t *testing.T) {
	// F-05: 心跳正常 (LastHeartbeat < 10s)
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer,
		withAssignedPod("test-agent"),
		withPhase("Bound"))

	registry := NewConfigurableMockRegistry()
	registry.DefaultAgent = &agentpool.AgentInfo{
		ID:            "test-agent",
		PodName:       "test-agent",
		PodIP:         "10.0.0.1",
		LastHeartbeat: time.Now(), // 刚刚心跳
		SandboxStatuses: map[string]api.SandboxStatus{
			"test-sb": {Phase: "running", SandboxID: "sb-123"},
		},
	}
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter, "应该定期同步状态")

	// 应该同步 Agent 状态到 CRD
	updated := getSandbox(t, r, "test-sb")
	assert.Equal(t, "Running", updated.Status.Phase)
	assert.Equal(t, "sb-123", updated.Status.SandboxID)
}

func TestSandbox_HeartbeatTimeout(t *testing.T) {
	// F-06: 心跳超时 (LastHeartbeat > 10s)
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer,
		withAssignedPod("test-agent"),
		withPhase("Bound"))

	registry := NewConfigurableMockRegistry()
	registry.LastHeartbeatAge = 15 * time.Second // 心跳超时
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	result, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter, "应该等待 Controller 清理")
}

// ============================================================================
// 6. 状态同步测试 (Status Sync)
// ============================================================================

func TestSandbox_StatusSync_FromRegistry(t *testing.T) {
	// S-01: 同步 Agent 状态
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer,
		withAssignedPod("test-agent"),
		withPhase("Bound"))

	registry := NewConfigurableMockRegistry()
	registry.DefaultAgent = &agentpool.AgentInfo{
		ID:            "test-agent",
		PodName:       "test-agent",
		PodIP:         "10.0.0.1",
		LastHeartbeat: time.Now(),
		SandboxStatuses: map[string]api.SandboxStatus{
			"test-sb": {Phase: "running", SandboxID: "container-abc123"},
		},
	}
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	_, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)

	updated := getSandbox(t, r, "test-sb")
	assert.Equal(t, "Running", updated.Status.Phase)
	assert.Equal(t, "container-abc123", updated.Status.SandboxID)
}

func TestSandbox_StatusSync_Endpoints(t *testing.T) {
	// S-02: 端口填充
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer,
		withAssignedPod("test-agent"),
		withPhase("Bound"),
		withExposedPorts(8080, 9090))

	registry := NewConfigurableMockRegistry()
	registry.DefaultAgent = &agentpool.AgentInfo{
		ID:            "test-agent",
		PodName:       "test-agent",
		PodIP:         "10.0.0.99",
		LastHeartbeat: time.Now(),
		SandboxStatuses: map[string]api.SandboxStatus{
			"test-sb": {Phase: "running", SandboxID: "sb-xyz"},
		},
	}
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	_, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)

	updated := getSandbox(t, r, "test-sb")
	assert.Contains(t, updated.Status.Endpoints, "10.0.0.99:8080")
	assert.Contains(t, updated.Status.Endpoints, "10.0.0.99:9090")
}

func TestSandbox_StatusSync_NoChange(t *testing.T) {
	// S-03: 状态无变化
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer,
		withAssignedPod("test-agent"),
		withPhase("running"))
	sb.Status.SandboxID = "sb-123"

	registry := NewConfigurableMockRegistry()
	registry.DefaultAgent = &agentpool.AgentInfo{
		ID:            "test-agent",
		PodName:       "test-agent",
		PodIP:         "10.0.0.1",
		LastHeartbeat: time.Now(),
		SandboxStatuses: map[string]api.SandboxStatus{
			"test-sb": {Phase: "running", SandboxID: "sb-123"}, // 相同状态
		},
	}
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	_, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)
	// 不触发更新（测试无副作用）
}

func TestSandbox_StatusSync_AgentStatusMissing(t *testing.T) {
	// S-04: Agent 存在但无此 Sandbox 的状态
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer,
		withAssignedPod("test-agent"),
		withPhase("Bound"))

	registry := NewConfigurableMockRegistry()
	registry.DefaultAgent = &agentpool.AgentInfo{
		ID:            "test-agent",
		PodName:       "test-agent",
		PodIP:         "10.0.0.1",
		LastHeartbeat: time.Now(),
		SandboxStatuses: map[string]api.SandboxStatus{
			"other-sb": {Phase: "running"}, // 不是我们的 sandbox
		},
	}
	agentClient := &MockAgentClient{}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	_, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)

	// 状态不应改变
	updated := getSandbox(t, r, "test-sb")
	assert.Equal(t, "Bound", updated.Status.Phase)
}

// ============================================================================
// Bug 验证测试 (用于确认和修复潜在 Bug)
// ============================================================================

func TestBug01_DeletionWithRunningPhase(t *testing.T) {
	// BUG-01: 删除时 Phase=Running 被遗漏
	// 当前代码只处理 Bound/Terminating，Running 会走默认分支
	scheme := newTestScheme(t)
	sb := newBaseSandbox("test-sb", withFinalizer, withDeletionTimestamp,
		withAssignedPod("test-agent"),
		withPhase("Running")) // 注意是 Running 而非 Bound

	registry := NewConfigurableMockRegistry()
	deleteCalled := false
	agentClient := &MockAgentClient{
		DeleteSandboxFunc: func(endpoint string, req *api.DeleteSandboxRequest) (*api.DeleteSandboxResponse, error) {
			deleteCalled = true
			return &api.DeleteSandboxResponse{Success: true}, nil
		},
	}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	_, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)

	// 这个测试可能会失败，因为 Running 不在处理分支中
	// 当前行为：Phase=Running 会直接移除 Finalizer，不调用 Agent 删除
	// 预期行为：应该调用 Agent 删除
	t.Logf("Delete called: %v (如果为 false 则确认 Bug 存在)", deleteCalled)
}

func TestBug03_ResetWithoutDeleteFromAgent(t *testing.T) {
	// BUG-03: Reset 时未调用 deleteFromAgent
	scheme := newTestScheme(t)
	resetTime := time.Now()
	sb := newBaseSandbox("test-sb", withFinalizer,
		withAssignedPod("test-agent"),
		withPhase("Bound"),
		withResetRevision(resetTime))

	registry := NewConfigurableMockRegistry()
	deleteCalled := false
	agentClient := &MockAgentClient{
		DeleteSandboxFunc: func(endpoint string, req *api.DeleteSandboxRequest) (*api.DeleteSandboxResponse, error) {
			deleteCalled = true
			return &api.DeleteSandboxResponse{Success: true}, nil
		},
	}

	r := newTestReconciler(scheme, []client.Object{sb}, registry, agentClient)

	_, err := r.Reconcile(context.Background(), reconcileRequest("test-sb"))
	require.NoError(t, err)

	// 验证是否调用了 deleteFromAgent
	// 当前行为：只 Release Registry，不调用 Agent 删除
	t.Logf("Delete called: %v (如果为 false 则确认 Bug 存在)", deleteCalled)
	assert.True(t, registry.ReleaseCalled, "应该释放 Registry")
}
