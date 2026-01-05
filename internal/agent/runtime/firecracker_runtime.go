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

// FirecrackerRuntime 实现基于 Firecracker MicroVM 的运行时
type FirecrackerRuntime struct {
	ContainerdRuntime
}

func (r *FirecrackerRuntime) CreateSandbox(ctx context.Context, config *SandboxConfig) (*SandboxMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.client == nil {
		return nil, fmt.Errorf("containerd client not initialized")
	}

	ctx = namespaces.WithNamespace(ctx, "k8s.io")

	// 1. 准备镜像
	image, err := r.prepareImage(ctx, config.Image)
	if err != nil {
		return nil, err
	}

	// 2. 准备配置并注入 Firecracker 特有的配置
	specOpts := r.prepareSpecOpts(config, image)

	// 注入 Firecracker 特有的配置
	// 注意：在实际验证环境(如KIND)中，需要确保该路径下有可用的内核镜像
	kernelPath := "/var/lib/firecracker/vmlinux"
	specOpts = append(specOpts, oci.WithAnnotations(map[string]string{
		"io.containerd.firecracker.v1.kernel": kernelPath,
	}))

	// 3. 创建容器 (指定 Firecracker Runtime)
	containerID := config.SandboxID
	labels := r.prepareLabels(config)

	container, err := r.client.NewContainer(
		ctx,
		containerID,
		containerd.WithImage(image),
		containerd.WithNewSnapshot(containerID+"-snapshot", image),
		// 关键点：使用完整的运行时处理器名称
		containerd.WithRuntime("io.containerd.firecracker.v1", nil),
		containerd.WithNewSpec(specOpts...),
		containerd.WithContainerLabels(labels),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create firecracker container: %w", err)
	}

	// 4. 创建并启动 Task
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
	if err != nil {
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, fmt.Errorf("failed to create task in VM: %w", err)
	}

	if err := task.Start(ctx); err != nil {
		task.Delete(ctx)
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, fmt.Errorf("failed to start VM task: %w", err)
	}

	// 5. 元数据记录
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