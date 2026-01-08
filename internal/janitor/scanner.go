package janitor

import (
	"context"
	"fmt"
	"time"

	"github.com/containerd/containerd/v2/pkg/namespaces"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (j *Janitor) Scan(ctx context.Context) {
	logger := log.FromContext(ctx)
	logger.Info("Starting periodic containerd scan")

	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	// 筛选出所有 managed 容器
	containers, err := j.ctrdClient.Containers(ctx, "labels.\"fast-sandbox.io/managed\"==\"true\"")
	if err != nil {
		logger.Error(err, "Failed to list containers")
		return
	}

	for _, c := range containers {
		labelsMap, err := c.Labels(ctx)
		if err != nil {
			continue
		}

		agentUID := labelsMap["fast-sandbox.io/agent-uid"]
		agentName := labelsMap["fast-sandbox.io/agent-name"]
		
		if agentUID == "" {
			continue
		}

		if !j.podExists(agentUID) {
			// 增加安全缓冲：仅清理创建超过 2 分钟的
			info, _ := c.Info(ctx)
			if time.Since(info.CreatedAt) > 2*time.Minute {
				logger.Info("Found orphan container via scanner", "container", c.ID(), "agentUID", agentUID)
				j.queue.Add(CleanupTask{
					ContainerID: c.ID(),
					AgentUID:    agentUID,
					PodName:     agentName,
				})
			}
		}
	}
}

func (j *Janitor) podExists(uid string) bool {
	pods, err := j.podLister.List(labels.Everything())
	if err != nil {
		return true // 安全起见，出错认为存在
	}
	for _, p := range pods {
		if string(p.UID) == uid {
			return true
		}
	}
	return false
}

func (j *Janitor) enqueueOrphansByUID(ctx context.Context, uid string, name string, ns string) {
	logger := log.FromContext(ctx)
	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	
	filter := fmt.Sprintf("labels.\"fast-sandbox.io/agent-uid\"==\"%s\"", uid)
	containers, err := j.ctrdClient.Containers(ctx, filter)
	if err != nil {
		return
	}

	for _, c := range containers {
		logger.Info("Enqueuing orphan container for cleanup", "container", c.ID(), "agent", name)
		j.queue.Add(CleanupTask{
			ContainerID: c.ID(),
			AgentUID:    uid,
			PodName:     name,
			Namespace:   ns,
		})
	}
}