package janitor

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (j *Janitor) doCleanup(ctx context.Context, task CleanupTask) error {
	logger := log.FromContext(ctx).WithValues("container", task.ContainerID, "agent", task.PodName)
	logger.Info("Starting cleanup of orphan sandbox")

	// 0. 双重验证：通过直接 K8s API 检查 Pod 是否真的不存在
	// 这是安全网，防止 Scanner 的 Lister 错误导致误删
	if j.verifyPodExistsDirectly(ctx, task.AgentUID, task.Namespace) {
		logger.Info("Pod still exists via direct API check, aborting cleanup",
			"pod-name", task.PodName, "agent-uid", task.AgentUID, "namespace", task.Namespace)
		return nil // Pod 存在，跳过清理
	}

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

// verifyPodExistsDirectly 通过直接 K8s API 验证 Pod 是否存在
// 这是清理前的最后安全检查，防止 Scanner Lister 错误导致误删
func (j *Janitor) verifyPodExistsDirectly(ctx context.Context, podUID, namespace string) bool {
	if j.kubeClient == nil {
		return false
	}

	// 如果没有提供 namespace，使用 default
	if namespace == "" {
		namespace = "default"
	}

	// 通过 UID 查找需要列出所有 Pod 然后匹配
	podList, err := j.kubeClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Log.Error(err, "Failed to list pods for direct verification", "namespace", namespace)
		return false
	}

	for _, p := range podList.Items {
		if string(p.UID) == podUID {
			// Pod 存在，且不是正在删除状态
			if p.DeletionTimestamp == nil {
				return true
			}
			// Pod 正在删除，允许清理其容器
			log.Log.Info("Pod is being deleted, allowing container cleanup", "pod", p.Name, "uid", podUID)
			return false
		}
	}
	return false
}