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

	if socketPath == "" {
		socketPath = "/run/containerd/containerd.sock" // 默认路径
	}
	r.socketPath = socketPath
	r.sandboxes = make(map[string]*SandboxMetadata)

	// 初始化 containerd 客户端
	client, err := containerd.New(socketPath)
	if err != nil {
		return fmt.Errorf("failed to create containerd client: %w", err)
	}
	r.client = client

	// 获取 Pod 的 cgroup 路径
	cgroupPath, err := GetPodCgroupPath()
	if err != nil {
		// 如果无法获取，记录警告但不失败（本地开发环境可能没有 kubepods）
		fmt.Printf("Warning: failed to get pod cgroup path: %v\n", err)
		cgroupPath = "" // 使用空路径
	}
	r.cgroupPath = cgroupPath

	// 获取 Pod 的 network namespace
	netnsPath, err := GetPodNetNS()
	if err != nil {
		// 如果无法获取，记录警告但不失败
		fmt.Printf("Warning: failed to get pod netns path: %v\n", err)
		netnsPath = ""
	}
	r.netnsPath = netnsPath

	fmt.Printf("ContainerdRuntime initialized: socket=%s, cgroup=%s, netns=%s\n",
		socketPath, cgroupPath, netnsPath)

	return nil
}

// CreateSandbox 创建并启动一个 sandbox 容器
func (r *ContainerdRuntime) CreateSandbox(ctx context.Context, config *SandboxConfig) (*SandboxMetadata, error) {
	if config == nil || config.SandboxID == "" || config.Image == "" {
		return nil, ErrInvalidConfig
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// 检查是否已存在
	if _, exists := r.sandboxes[config.SandboxID]; exists {
		return nil, ErrSandboxAlreadyExists
	}

	// 使用 containerd namespace
	ctx = namespaces.WithNamespace(ctx, "fast-sandbox")

	// 1. 拉取镜像（如果不存在）
	if err := r.pullImageIfNotExists(ctx, config.Image); err != nil {
		return nil, fmt.Errorf("failed to pull image: %w", err)
	}

	// 2. 获取镜像
	image, err := r.client.GetImage(ctx, config.Image)
	if err != nil {
		return nil, fmt.Errorf("failed to get image: %w", err)
	}

	// 3. 创建容器
	containerID := "sandbox-" + config.SandboxID
	containerOpts := []containerd.NewContainerOpts{
		containerd.WithImage(image),
		containerd.WithNewSnapshot(containerID+"-snapshot", image),
	}

	// 配置 OCI 规范
	var ociOpts []oci.SpecOpts
	ociOpts = append(ociOpts,
		oci.WithImageConfig(image),
		oci.WithDefaultSpec(),
		oci.WithDefaultUnixDevices,
	)

	// 如果有命令，覆盖默认命令
	if len(config.Command) > 0 {
		args := config.Command
		if len(config.Args) > 0 {
			args = append(args, config.Args...)
		}
		ociOpts = append(ociOpts, oci.WithProcessArgs(args...))
	}

	// 配置环境变量
	if len(config.Env) > 0 {
		envs := make([]string, 0, len(config.Env))
		for k, v := range config.Env {
			envs = append(envs, fmt.Sprintf("%s=%s", k, v))
		}
		ociOpts = append(ociOpts, oci.WithEnv(envs))
	}

	// 配置网络命名空间（使用 Pod 的 netns）
	if r.netnsPath != "" {
		ociOpts = append(ociOpts, oci.WithLinuxNamespace(specs.LinuxNamespace{
			Type: specs.NetworkNamespace,
			Path: r.netnsPath,
		}))
	}

	// 配置 cgroup
	if r.cgroupPath != "" {
		// 将容器加入 Pod 的 cgroup
		ociOpts = append(ociOpts, oci.WithCgroup(r.cgroupPath+"/"+containerID))
	}

	containerOpts = append(containerOpts, containerd.WithNewSpec(ociOpts...))

	container, err := r.client.NewContainer(ctx, containerID, containerOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	// 4. 创建并启动任务
	task, err := container.NewTask(ctx, cio.NullIO)
	if err != nil {
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, fmt.Errorf("failed to create task: %w", err)
	}

	// 启动任务
	if err := task.Start(ctx); err != nil {
		task.Delete(ctx)
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, fmt.Errorf("failed to start task: %w", err)
	}

	// 5. 获取任务状态
	status, err := task.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get task status: %w", err)
	}

	// 6. 创建元数据
	metadata := &SandboxMetadata{
		SandboxID:   config.SandboxID,
		ClaimUID:    config.ClaimUID,
		ClaimName:   config.ClaimName,
		ContainerID: containerID,
		Image:       config.Image,
		Command:     config.Command,
		Args:        config.Args,
		Env:         config.Env,
		Port:        config.Port,
		PID:         int(task.Pid()),
		Status:      string(status.Status),
		CreatedAt:   time.Now().Unix(),
	}

	r.sandboxes[config.SandboxID] = metadata
	fmt.Printf("Created sandbox %s: container=%s, pid=%d\n", config.SandboxID, containerID, metadata.PID)

	return metadata, nil
}

// pullImageIfNotExists 拉取镜像（如果不存在）
func (r *ContainerdRuntime) pullImageIfNotExists(ctx context.Context, imageName string) error {
	// 规范化镜像名称：如果没有 registry 前缀，添加 docker.io/library/
	//if !strings.Contains(imageName, "/") {
	//	// 简单镜像名如 nginx, redis 等，添加 docker.io/library/ 前缀
	//	imageName = "docker.io/library/" + imageName
	//} else if !strings.Contains(imageName, ".") && strings.Count(imageName, "/") == 1 {
	//	// 如 user/image 格式，添加 docker.io/ 前缀
	//	imageName = "docker.io/" + imageName
	//}

	// 检查镜像是否已存在
	_, err := r.client.GetImage(ctx, imageName)
	if err == nil {
		// 镜像已存在
		return nil
	}

	// 拉取镜像
	fmt.Printf("Pulling image %s...\n", imageName)
	_, err = r.client.Pull(ctx, imageName, containerd.WithPullUnpack)
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}

	fmt.Printf("Image %s pulled successfully\n", imageName)
	return nil
}

// DeleteSandbox 停止并删除一个 sandbox 容器
func (r *ContainerdRuntime) DeleteSandbox(ctx context.Context, sandboxID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	metadata, exists := r.sandboxes[sandboxID]
	if !exists {
		return ErrSandboxNotFound
	}

	// 使用 containerd namespace
	ctx = namespaces.WithNamespace(ctx, "fast-sandbox")

	// 1. 获取容器
	container, err := r.client.LoadContainer(ctx, metadata.ContainerID)
	if err != nil {
		// 容器不存在，只清理内存状态
		delete(r.sandboxes, sandboxID)
		return nil
	}

	// 2. 获取任务
	task, err := container.Task(ctx, nil)
	if err == nil {
		// 任务存在，先停止
		status, err := task.Status(ctx)
		if err == nil && status.Status == containerd.Running {
			// 发送 SIGTERM 信号
			if err := task.Kill(ctx, syscall.SIGTERM); err != nil {
				fmt.Printf("Warning: failed to kill task for sandbox %s: %v\n", sandboxID, err)
			}
			// 等待任务退出（最多 10 秒）
			waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			_, _ = task.Wait(waitCtx)
		}

		// 删除任务
		if _, err := task.Delete(ctx); err != nil {
			fmt.Printf("Warning: failed to delete task for sandbox %s: %v\n", sandboxID, err)
		}
	}

	// 3. 删除容器
	if err := container.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
		fmt.Printf("Warning: failed to delete container for sandbox %s: %v\n", sandboxID, err)
	}

	// 4. 清理内存状态
	delete(r.sandboxes, sandboxID)
	fmt.Printf("Deleted sandbox %s\n", sandboxID)

	return nil
}

// GetSandbox 获取指定 sandbox 的元数据
func (r *ContainerdRuntime) GetSandbox(ctx context.Context, sandboxID string) (*SandboxMetadata, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	metadata, exists := r.sandboxes[sandboxID]
	if !exists {
		return nil, nil // 不存在返回 nil, nil
	}

	// 返回副本，避免并发修改
	result := *metadata
	return &result, nil
}

// ListSandboxes 列出所有当前运行的 sandbox
func (r *ContainerdRuntime) ListSandboxes(ctx context.Context) ([]*SandboxMetadata, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*SandboxMetadata, 0, len(r.sandboxes))
	for _, metadata := range r.sandboxes {
		// 返回副本
		m := *metadata
		result = append(result, &m)
	}

	return result, nil
}

// ListImages 列出节点上可用的镜像列表
func (r *ContainerdRuntime) ListImages(ctx context.Context) ([]string, error) {
	// 使用 containerd namespace
	ctx = namespaces.WithNamespace(ctx, "fast-sandbox")

	images, err := r.client.ListImages(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	result := make([]string, 0, len(images))
	for _, img := range images {
		result = append(result, img.Name())
	}

	return result, nil
}

// PullImage 拉取指定的容器镜像
func (r *ContainerdRuntime) PullImage(ctx context.Context, image string) error {
	// 使用 containerd namespace
	ctx = namespaces.WithNamespace(ctx, "fast-sandbox")

	return r.pullImageIfNotExists(ctx, image)
}

// GetPodCgroupPath 获取当前 Pod 的 cgroup 路径
func (r *ContainerdRuntime) GetPodCgroupPath() (string, error) {
	return GetPodCgroupPath()
}

// GetPodNetNS 获取当前 Pod 的 network namespace 路径
func (r *ContainerdRuntime) GetPodNetNS() (string, error) {
	return GetPodNetNS()
}

// Close 关闭运行时客户端连接
func (r *ContainerdRuntime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.client != nil {
		return r.client.Close()
	}

	return nil
}
