package cmd

import (
	"context"
	"os"
	"testing"

	fastpathv1 "fast-sandbox/api/proto/v1"
	"github.com/spf13/viper"
	"google.golang.org/grpc"
)

// MockClient 模拟 gRPC 客户端
type MockClient struct {
	fastpathv1.UnimplementedFastPathServiceServer
	CreateFunc func(ctx context.Context, req *fastpathv1.CreateRequest) (*fastpathv1.CreateResponse, error)
}

func (m *MockClient) CreateSandbox(ctx context.Context, in *fastpathv1.CreateRequest, opts ...grpc.CallOption) (*fastpathv1.CreateResponse, error) {
	if m.CreateFunc != nil {
		return m.CreateFunc(ctx, in)
	}
	return &fastpathv1.CreateResponse{}, nil
}

// 模拟其他方法以满足接口
func (m *MockClient) DeleteSandbox(ctx context.Context, in *fastpathv1.DeleteRequest, opts ...grpc.CallOption) (*fastpathv1.DeleteResponse, error) {
	return &fastpathv1.DeleteResponse{Success: true}, nil
}
func (m *MockClient) ListSandboxes(ctx context.Context, in *fastpathv1.ListRequest, opts ...grpc.CallOption) (*fastpathv1.ListResponse, error) {
	return &fastpathv1.ListResponse{}, nil
}
func (m *MockClient) GetSandbox(ctx context.Context, in *fastpathv1.GetRequest, opts ...grpc.CallOption) (*fastpathv1.SandboxInfo, error) {
	return &fastpathv1.SandboxInfo{}, nil
}

func TestRunCommand(t *testing.T) {
	// 1. 注入 Mock
	mockClient := &MockClient{}
	ClientFactory = func() (fastpathv1.FastPathServiceClient, *grpc.ClientConn, error) {
		return mockClient, nil, nil
	}

	// 2. 验证参数传递
	var capturedReq *fastpathv1.CreateRequest
	mockClient.CreateFunc = func(ctx context.Context, req *fastpathv1.CreateRequest) (*fastpathv1.CreateResponse, error) {
		capturedReq = req
		return &fastpathv1.CreateResponse{
			SandboxId: "test-sb-id",
			AgentPod:  "test-agent",
		}, nil
	}

	// 3. 执行命令 (Flag 模式)
	viper.Reset()
	viper.Set("namespace", "test-ns")

	// 重置全局变量 (因为是包级变量，可能残留)
	pool = ""
	mode = ""
	image = ""
	ports = nil

	// 构造命令: run my-sandbox --image=alpine --pool=test-pool
	rootCmd.SetArgs([]string{"run", "my-sandbox", "--image=alpine", "--pool=test-pool", "--mode=strong"})
	
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// 4. 断言
	if capturedReq == nil {
		t.Fatal("CreateSandbox was not called")
	}
	if capturedReq.Name != "my-sandbox" {
		t.Errorf("expected name 'my-sandbox', got '%s'", capturedReq.Name)
	}
	if capturedReq.Image != "alpine" {
		t.Errorf("expected image 'alpine', got '%s'", capturedReq.Image)
	}
	// ... (其他断言)
}

func TestRunCommandWithFile(t *testing.T) {
	// ... (Mock 注入同上)
	mockClient := &MockClient{}
	ClientFactory = func() (fastpathv1.FastPathServiceClient, *grpc.ClientConn, error) {
		return mockClient, nil, nil // nil conn
	}
	var capturedReq *fastpathv1.CreateRequest
	mockClient.CreateFunc = func(ctx context.Context, req *fastpathv1.CreateRequest) (*fastpathv1.CreateResponse, error) {
		capturedReq = req
		return &fastpathv1.CreateResponse{}, nil
	}

	// 创建临时配置文件
	tmpFile, _ := os.CreateTemp("", "config.yaml")
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString(`
image: nginx
pool_ref: file-pool
consistency_mode: fast
`)
	tmpFile.Close()

	// 重置
	pool = ""
	image = ""
	
	// 执行: run my-sandbox -f config.yaml --pool=override-pool
	rootCmd.SetArgs([]string{"run", "my-sandbox", "-f", tmpFile.Name(), "--pool=override-pool"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// 断言：Image 来自文件，Pool 来自 Flag (覆盖)
	if capturedReq.Image != "nginx" {
		t.Errorf("expected image 'nginx' (from file), got '%s'", capturedReq.Image)
	}
	if capturedReq.PoolRef != "override-pool" {
		t.Errorf("expected pool 'override-pool' (from flag), got '%s'", capturedReq.PoolRef)
	}
}
