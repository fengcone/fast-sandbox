package runtime

import (
	"context"
	"fmt"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
)

// GVisorRuntime 实现基于 gVisor (runsc) 的安全沙箱运行时
type GVisorRuntime struct {
	ContainerdRuntime
}

func (r *GVisorRuntime) CreateSandbox(ctx context.Context, config *SandboxConfig) (*SandboxMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.client == nil {
		return nil, fmt.Errorf("containerd client not initialized")
	}

	ctx = namespaces.WithNamespace(ctx, "k8s.io")

	// 1. 准备镜像 (gVisor 支持标准 OCI 镜像)
	image, err := r.prepareImage(ctx, config.Image)
	if err != nil {
		return nil, err
	}

	// 2. 准备 Spec 配置
	// 特殊处理：针对 gVisor (runsc)，在 Cgroup v2 环境下，如果父级 Cgroup (Agent Pod) 已经有进程，
	// runsc 无法在该层开启 subtree_control。因此我们移除之前 prepareSpecOpts 注入的 Cgroup 路径，
	// 让 runsc 自动管理 Cgroup 或者直接使用默认。
	specOpts := []oci.SpecOpts{
		oci.WithImageConfig(image),
		oci.WithProcessArgs(append(config.Command, config.Args...)...),
		oci.WithEnv(envMapToSlice(config.Env)),
	}

	// 3. 创建容器 (指定 runsc 运行时)
	containerID := config.SandboxID
	labels := r.prepareLabels(config)

	container, err := r.client.NewContainer(
		ctx,
		containerID,
		containerd.WithImage(image),
		containerd.WithNewSnapshot(containerID+"-snapshot", image),
		// 关键点：指定使用 gVisor 运行时处理器
		containerd.WithRuntime("io.containerd.runsc.v1", nil),
		containerd.WithNewSpec(specOpts...),
		containerd.WithContainerLabels(labels),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create gVisor container: %w", err)
	}

	// 4. 创建并启动 Task
	// 使用空流避免 IO 重定向干扰，确保 gVisor 能够稳定启动
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, nil, nil)))
	if err != nil {
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, fmt.Errorf("failed to create task in gVisor: %w", err)
	}

	if err := task.Start(ctx); err != nil {
		task.Delete(ctx)
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, fmt.Errorf("failed to start gVisor task: %w", err)
	}

	// 5. 记录元数据
	metadata := &SandboxMetadata{
		SandboxID:   config.SandboxID,
		ClaimUID:    config.ClaimUID,
		ClaimName:   config.ClaimName,
		ContainerID: containerID,
		Image:       config.Image,
		Status:      "running",
		CreatedAt:   time.Now().Unix(),
		PID:         int(task.Pid()),
	}

	r.sandboxes[config.SandboxID] = metadata
	return metadata, nil
}
