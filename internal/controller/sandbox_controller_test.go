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

// MockAgentClient 用于模拟 AgentClient 行为
type MockAgentClient struct {
	DeleteSandboxFunc func(endpoint string, req *api.DeleteSandboxRequest) (*api.DeleteSandboxResponse, error)
}

func (m *MockAgentClient) CreateSandbox(endpoint string, req *api.CreateSandboxRequest) (*api.CreateSandboxResponse, error) {
	return nil, nil
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

// MockRegistry 用于模拟 Registry 行为
type MockRegistry struct {
	ReleaseCalled bool
}

func (m *MockRegistry) RegisterOrUpdate(info agentpool.AgentInfo) {}
func (m *MockRegistry) GetAllAgents() []agentpool.AgentInfo {
	return []agentpool.AgentInfo{}
}
func (m *MockRegistry) GetAgentByID(id agentpool.AgentID) (agentpool.AgentInfo, bool) {
	return agentpool.AgentInfo{PodName: string(id), PodIP: "10.0.0.1"}, true
}
func (m *MockRegistry) Allocate(sb *apiv1alpha1.Sandbox) (*agentpool.AgentInfo, error) {
	return &agentpool.AgentInfo{PodName: "test-agent"}, nil
}
func (m *MockRegistry) Release(id agentpool.AgentID, sb *apiv1alpha1.Sandbox) {
	m.ReleaseCalled = true
}
func (m *MockRegistry) Restore(ctx context.Context, c client.Reader) error {
	return nil
}
func (m *MockRegistry) Remove(id agentpool.AgentID)                                 {}
func (m *MockRegistry) CleanupStaleAgents(timeout time.Duration) int               { return 0 }

func setupTestReconciler(_ *testing.T, scheme *runtime.Scheme, objs []client.Object) (*SandboxReconciler, *MockRegistry, *MockAgentClient) {
	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...)

	agentClient := &MockAgentClient{}
	reg := &MockRegistry{}

	reconciler := &SandboxReconciler{
		Client:      builder.Build(),
		Scheme:      scheme,
		Ctx:         context.Background(),
		Registry:    reg,
		AgentClient: agentClient,
	}

	return reconciler, reg, agentClient
}

// TestFinalizer_ErrorHandling 测试 Finalizer 删除时的错误处理
func TestFinalizer_ErrorHandling(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1alpha1.AddToScheme(scheme))

	tests := []struct {
		name              string
		deleteSandboxErr  error
		expectRetry       bool
		expectReleaseCall bool
		describe          string
	}{
		{
			name:              "删除成功",
			deleteSandboxErr:  nil,
			expectRetry:       false,
			expectReleaseCall: true,
			describe:          "deleteFromAgent 成功时，应该释放 Registry 并移除 Finalizer",
		},
		{
			name:              "删除失败-网络错误",
			deleteSandboxErr:  errors.New("network error: connection refused"),
			expectRetry:       true,
			expectReleaseCall: false,
			describe:          "deleteFromAgent 失败时，应该返回错误触发重试，不释放 Registry",
		},
		{
			name:              "删除失败-超时",
			deleteSandboxErr:  errors.New("timeout waiting for agent response"),
			expectRetry:       true,
			expectReleaseCall: false,
			describe:          "deleteFromAgent 超时时，应该返回错误触发重试，不释放 Registry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 创建带有 DeletionTimestamp 的 Sandbox（模拟用户删除）
			sandbox := &apiv1alpha1.Sandbox{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-sandbox",
					Namespace:         "default",
					DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
					Finalizers:        []string{"sandbox.fast.io/cleanup"},
				},
				Spec: apiv1alpha1.SandboxSpec{
					Image:   "alpine",
					PoolRef: "test-pool",
				},
				Status: apiv1alpha1.SandboxStatus{
					AssignedPod: "test-agent",
					Phase:       "Running",
				},
			}

			reconciler, reg, agentClient := setupTestReconciler(t, scheme, []client.Object{sandbox})

			// 设置 mock 行为
			agentClient.DeleteSandboxFunc = func(endpoint string, req *api.DeleteSandboxRequest) (*api.DeleteSandboxResponse, error) {
				if tt.deleteSandboxErr != nil {
					return nil, tt.deleteSandboxErr
				}
				return &api.DeleteSandboxResponse{Success: true}, nil
			}

			// 执行 Reconcile
			ctx := context.Background()
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{Namespace: "default", Name: "test-sandbox"},
			}

			result, err := reconciler.Reconcile(ctx, req)

			// 验证结果
			if tt.expectRetry {
				// 应该返回错误
				assert.Error(t, err, tt.describe)
				assert.Contains(t, err.Error(), "failed to delete from agent", tt.describe)
				// 不应该释放 Registry
				assert.False(t, reg.ReleaseCalled, tt.describe)
			} else {
				// 不应该返回错误
				assert.NoError(t, err, tt.describe)
				assert.Empty(t, result, tt.describe)
				// 应该释放 Registry
				assert.True(t, reg.ReleaseCalled, tt.describe)
			}
		})
	}
}

// TestFinalizer_AgentNotFound 测试 Agent 不在 Registry 中的场景
func TestFinalizer_AgentNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1alpha1.AddToScheme(scheme))

	sandbox := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-sandbox",
			Namespace:         "default",
			DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
			Finalizers:        []string{"sandbox.fast.io/cleanup"},
		},
		Spec: apiv1alpha1.SandboxSpec{
			Image:   "alpine",
			PoolRef: "test-pool",
		},
		Status: apiv1alpha1.SandboxStatus{
			AssignedPod: "unknown-agent",
		},
	}

	// 使用返回 not found 的 Registry
	reg := &MockNotFoundRegistry{}

	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sandbox)

	reconciler := &SandboxReconciler{
		Client:      builder.Build(),
		Scheme:      scheme,
		Ctx:         context.Background(),
		Registry:    reg,
		AgentClient: &MockAgentClient{},
	}

	// 执行 Reconcile
	ctx := context.Background()
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "test-sandbox"},
	}

	result, err := reconciler.Reconcile(ctx, req)

	// Agent 不存在时应该跳过删除，继续完成 Finalizer 清理
	assert.NoError(t, err, "Agent 不存在时应该继续完成清理")
	assert.Empty(t, result, "不应该重新入队")
}

// MockNotFoundRegistry 模拟 Agent 不存在的场景
type MockNotFoundRegistry struct{}

func (m *MockNotFoundRegistry) RegisterOrUpdate(info agentpool.AgentInfo)                       {}
func (m *MockNotFoundRegistry) GetAllAgents() []agentpool.AgentInfo                              { return []agentpool.AgentInfo{} }
func (m *MockNotFoundRegistry) GetAgentByID(id agentpool.AgentID) (agentpool.AgentInfo, bool)    { return agentpool.AgentInfo{}, false }
func (m *MockNotFoundRegistry) Allocate(sb *apiv1alpha1.Sandbox) (*agentpool.AgentInfo, error)   { return nil, nil }
func (m *MockNotFoundRegistry) Release(id agentpool.AgentID, sb *apiv1alpha1.Sandbox)           {}
func (m *MockNotFoundRegistry) Restore(ctx context.Context, c client.Reader) error               { return nil }
func (m *MockNotFoundRegistry) Remove(id agentpool.AgentID)                                      {}
func (m *MockNotFoundRegistry) CleanupStaleAgents(timeout time.Duration) int                    { return 0 }

// TestFinalizer_NoAssignedPod 测试没有 AssignedPod 的场景
func TestFinalizer_NoAssignedPod(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1alpha1.AddToScheme(scheme))

	sandbox := &apiv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-sandbox",
			Namespace:         "default",
			DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
			Finalizers:        []string{"sandbox.fast.io/cleanup"},
		},
		Spec: apiv1alpha1.SandboxSpec{
			Image:   "alpine",
			PoolRef: "test-pool",
		},
		Status: apiv1alpha1.SandboxStatus{
			AssignedPod: "", // 空的 AssignedPod
		},
	}

	reg := &MockRegistry{}

	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sandbox)

	reconciler := &SandboxReconciler{
		Client:      builder.Build(),
		Scheme:      scheme,
		Ctx:         context.Background(),
		Registry:    reg,
		AgentClient: &MockAgentClient{},
	}

	// 执行 Reconcile
	ctx := context.Background()
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "test-sandbox"},
	}

	result, err := reconciler.Reconcile(ctx, req)

	// 没有 AssignedPod 时应该跳过 Agent 删除，直接完成 Finalizer 清理
	assert.NoError(t, err, "没有 AssignedPod 时应该继续完成清理")
	assert.Empty(t, result, "不应该重新入队")
}
