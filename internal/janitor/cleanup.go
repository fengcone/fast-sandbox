package janitor

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (j *Janitor) doCleanup(ctx context.Context, task CleanupTask) error {
	logger := log.FromContext(ctx).WithValues("container", task.ContainerID, "agent", task.PodName)
	logger.Info("Starting cleanup of orphan sandbox")

	// 确保使用 k8s.io 命名空间
	ctx = namespaces.WithNamespace(ctx, "k8s.io")

	// 1. 加载容器
	c, err := j.ctrdClient.LoadContainer(ctx, task.ContainerID)
	if err != nil {
		// 如果容器不存在，认为是清理完成
		return nil
	}

	// 2. 处理任务
	t, err := c.Task(ctx, nil)
	if err == nil {
		logger.Info("Killing task")
		t.Kill(ctx, syscall.SIGKILL)
		
		// 等待退出
		exitCh, err := t.Wait(ctx)
		if err == nil {
			select {
			case <-exitCh:
			case <-time.After(5 * time.Second):
				logger.Info("Task exit timeout, proceeding to delete")
			}
		}
		t.Delete(ctx)
	}

	// 3. 删除容器 (带 Snapshot 清理)
	if err := c.Delete(ctx, client.WithSnapshotCleanup); err != nil {
		logger.Error(err, "Failed to delete container metadata")
	}

	// 4. 清理 FIFO 文件
	j.cleanupFIFOs(task.ContainerID)

	logger.Info("Cleanup completed successfully")
	return nil
}

func (j *Janitor) cleanupFIFOs(containerID string) {
	fifoDir := "/run/containerd/fifo"
	pattern := filepath.Join(fifoDir, containerID+"*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	for _, m := range matches {
		os.Remove(m)
	}
}