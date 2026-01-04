package runtime

import (
	"context"
	"sync"

	containerd "github.com/containerd/containerd/v2/client"
)

// ContainerdRuntime 实现基于 containerd 的容器运行时
type ContainerdRuntime struct {
	mu         sync.RWMutex
	socketPath string
	client     *containerd.Client
	sandboxes  map[string]*SandboxMetadata // sandboxID -> metadata
	cgroupPath string                      // Pod 的 cgroup 路径
	netnsPath  string                      // Pod 的 network namespace 路径
}

// Initialize 初始化 containerd 客户端
func (r *ContainerdRuntime) Initialize(ctx context.Context, socketPath string) error {
	return nil
}

func (r *ContainerdRuntime) CreateSandbox(ctx context.Context, config *SandboxConfig) (*SandboxMetadata, error) {
	return nil, nil
}

func (r *ContainerdRuntime) DeleteSandbox(ctx context.Context, sandboxID string) error {
	return nil
}

func (r *ContainerdRuntime) GetSandbox(ctx context.Context, sandboxID string) (*SandboxMetadata, error) {

	return nil, nil
}

func (r *ContainerdRuntime) ListSandboxes(ctx context.Context) ([]*SandboxMetadata, error) {

	return nil, nil
}

func (r *ContainerdRuntime) ListImages(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (r *ContainerdRuntime) PullImage(ctx context.Context, image string) error {
	return nil
}

func (r *ContainerdRuntime) Close() error {

	return nil
}
