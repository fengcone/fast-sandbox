package runtime

import (
	"context"
	"fmt"
	"sync"
	"syscall"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
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
	r.mu.Lock()
	defer r.mu.Unlock()

	r.socketPath = socketPath
	// 使用默认路径如果未提供
	if r.socketPath == "" {
		r.socketPath = "/run/containerd/containerd.sock"
	}

	client, err := containerd.New(r.socketPath, containerd.WithDefaultNamespace("k8s.io"))
	if err != nil {
		return fmt.Errorf("failed to create containerd client: %w", err)
	}

	r.client = client
	r.sandboxes = make(map[string]*SandboxMetadata)
	// TODO: 自动探测 Pod 的 Cgroup 路径和 NetNS 路径
	// r.cgroupPath = ...
	// r.netnsPath = ...

	return nil
}

func (r *ContainerdRuntime) CreateSandbox(ctx context.Context, config *SandboxConfig) (*SandboxMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.client == nil {
		return nil, fmt.Errorf("containerd client not initialized")
	}

	// 确保使用 k8s.io 命名空间
	ctx = namespaces.WithNamespace(ctx, "k8s.io")

	// 1. 确保镜像存在
	image, err := r.client.GetImage(ctx, config.Image)
	if err != nil {
		// 尝试拉取镜像
		image, err = r.client.Pull(ctx, config.Image, containerd.WithPullUnpack)
		if err != nil {
			return nil, fmt.Errorf("failed to pull image %s: %w", config.Image, err)
		}
	}

	// 2. 生成 Container ID
	containerID := config.SandboxID
	if containerID == "" {
		return nil, fmt.Errorf("sandbox ID is required")
	}

	// 3. 准备 OCI Spec
	// 复用 Host 网络 (相对于 Agent Pod)
	specOpts := []oci.SpecOpts{
		oci.WithImageConfig(image),
		oci.WithProcessArgs(append(config.Command, config.Args...)...),
		oci.WithEnv(envMapToSlice(config.Env)),
		oci.WithHostNamespace(specs.NetworkNamespace),
	}

	// 4. 创建容器
	labels := map[string]string{
		"fast-sandbox.io/managed":   "true",
		"fast-sandbox.io/id":        config.SandboxID,
		"fast-sandbox.io/claim-uid": config.ClaimUID,
		"fast-sandbox.io/claim-nm":  config.ClaimName,
	}

	container, err := r.client.NewContainer(
		ctx,
		containerID,
		containerd.WithImage(image),
		containerd.WithNewSnapshot(containerID+"-snapshot", image),
		containerd.WithNewSpec(specOpts...),
		containerd.WithContainerLabels(labels),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	// 5. 创建 Task
	// 使用 cio.WithStdio 将容器输出重定向到 Agent 的标准输出，方便调试
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
	if err != nil {
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, fmt.Errorf("failed to create task: %w", err)
	}

	// 6. 启动 Task
	if err := task.Start(ctx); err != nil {
		task.Delete(ctx)
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, fmt.Errorf("failed to start task: %w", err)
	}

	// 7. 构建 Metadata
	metadata := &SandboxMetadata{
		SandboxID:   config.SandboxID,
		ClaimUID:    config.ClaimUID,
		ClaimName:   config.ClaimName,
		ContainerID: containerID,
		Image:       config.Image,
		Command:     config.Command,
		Args:        config.Args,
		Env:         config.Env,
		Status:      "running",
		CreatedAt:   time.Now().Unix(),
		PID:         int(task.Pid()),
	}

	r.sandboxes[config.SandboxID] = metadata
	return metadata, nil
}

func (r *ContainerdRuntime) DeleteSandbox(ctx context.Context, sandboxID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.client == nil {
		return fmt.Errorf("containerd client not initialized")
	}

	ctx = namespaces.WithNamespace(ctx, "k8s.io")

	// 1. 加载容器
	container, err := r.client.LoadContainer(ctx, sandboxID)
	if err != nil {
		// 如果容器不存在，只清理 metadata 并返回 nil
		delete(r.sandboxes, sandboxID)
		return nil
	}

	// 2. 处理任务
	task, err := container.Task(ctx, nil)
	if err == nil {
		// 任务存在，先 Kill
		task.Kill(ctx, syscall.SIGKILL)
		// 等待退出
		// 简单的等待，生产环境可能需要更复杂的重试逻辑
		if exitCh, err := task.Wait(ctx); err == nil {
			select {
			case <-exitCh:
			case <-time.After(2 * time.Second):
			}
		}
		// 删除任务
		task.Delete(ctx)
	}

	// 3. 删除容器
	if err := container.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
		return fmt.Errorf("failed to delete container: %w", err)
	}

	// 4. 清理 metadata
	delete(r.sandboxes, sandboxID)
	return nil
}

func (r *ContainerdRuntime) GetSandbox(ctx context.Context, sandboxID string) (*SandboxMetadata, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if meta, ok := r.sandboxes[sandboxID]; ok {
		return meta, nil
	}
	return nil, nil
}

func (r *ContainerdRuntime) ListSandboxes(ctx context.Context) ([]*SandboxMetadata, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.client == nil {
		return nil, fmt.Errorf("containerd client not initialized")
	}

	ctx = namespaces.WithNamespace(ctx, "k8s.io")

	// 过滤出由 fast-sandbox 管理的容器
	containers, err := r.client.Containers(ctx, "labels.\"fast-sandbox.io/managed\"==\"true\"")
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var list []*SandboxMetadata
	for _, c := range containers {
		info, err := c.Info(ctx)
		if err != nil {
			continue
		}

		// 获取任务状态
		status := "unknown"
		pid := 0
		if task, err := c.Task(ctx, nil); err == nil {
			if s, err := task.Status(ctx); err == nil {
				status = string(s.Status)
			}
			pid = int(task.Pid())
		} else {
			status = "created" // 或者是 stopped/exited
		}

		meta := &SandboxMetadata{
			SandboxID:   info.Labels["fast-sandbox.io/id"],
			ClaimUID:    info.Labels["fast-sandbox.io/claim-uid"],
			ClaimName:   info.Labels["fast-sandbox.io/claim-nm"],
			ContainerID: c.ID(),
			Image:       info.Image,
			Status:      status,
			PID:         pid,
			CreatedAt:   info.CreatedAt.Unix(),
		}
		list = append(list, meta)
	}

	return list, nil
}

func (r *ContainerdRuntime) ListImages(ctx context.Context) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.client == nil {
		return nil, fmt.Errorf("containerd client not initialized")
	}

	// 确保使用 k8s.io 命名空间以复用宿主机镜像
	ctx = namespaces.WithNamespace(ctx, "k8s.io")

	images, err := r.client.ListImages(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	var imageNames []string
	for _, img := range images {
		imageNames = append(imageNames, img.Name())
	}
	return imageNames, nil
}

func (r *ContainerdRuntime) PullImage(ctx context.Context, image string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.client == nil {
		return fmt.Errorf("containerd client not initialized")
	}

	ctx = namespaces.WithNamespace(ctx, "k8s.io")

	// 检查镜像是否存在
	_, err := r.client.GetImage(ctx, image)
	if err == nil {
		return nil // 镜像已存在
	}

	// 拉取镜像
	_, err = r.client.Pull(ctx, image, containerd.WithPullUnpack)
	return err
}

func (r *ContainerdRuntime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil {
		return r.client.Close()
	}
	return nil
}

// helper function
func envMapToSlice(env map[string]string) []string {
	var res []string
	for k, v := range env {
		res = append(res, fmt.Sprintf("%s=%s", k, v))
	}
	return res
}
