package runtime

import (
	"context"
	"fmt"
	"os"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
)

// FirecrackerRuntime 实现基于 Firecracker MicroVM 的运行时
// 预期生产环境：Linux 物理机 + KVM + Devmapper
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

	// 2. 准备配置 (使用标准的 Containerd 逻辑)
	specOpts := r.prepareSpecOpts(config, image)

	// 3. 创建容器 (指定 Firecracker 运行时)
	containerID := config.SandboxID
	labels := r.prepareLabels(config)

	// 生产环境下通常使用 devmapper 以支持块设备挂载给 VM
	snapshotter := os.Getenv("FIRECRACKER_SNAPSHOTTER")
	if snapshotter == "" {
		snapshotter = "devmapper"
	}

	container, err := r.client.NewContainer(
		ctx,
		containerID,
		containerd.WithImage(image),
		containerd.WithSnapshotter(snapshotter),
		containerd.WithNewSnapshot(containerID+"-snapshot", image),
		// 显式指定运行时处理器名称
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
		Status:      "running",
		CreatedAt:   time.Now().Unix(),
		PID:         int(task.Pid()),
	}

	r.sandboxes[config.SandboxID] = metadata
	return metadata, nil
}
