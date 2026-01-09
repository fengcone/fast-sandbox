package janitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (j *Janitor) Scan(ctx context.Context) {
	logger := log.FromContext(ctx)
	logger.Info("Starting periodic containerd scan with CRD reconciliation")

	ctx = namespaces.WithNamespace(ctx, "k8s.io")
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
		sandboxName := labelsMap["fast-sandbox.io/sandbox-name"]
		claimUID := labelsMap["fast-sandbox.io/claim-uid"]
		
		if agentUID == "" || sandboxName == "" {
			continue
		}

		info, _ := c.Info(ctx)
		// 安全缓冲：仅清理创建超过 60 秒的容器
		if time.Since(info.CreatedAt) < 60*time.Second {
			continue
		}

		shouldCleanup := false
		reason := ""

		// 判定逻辑 1: Agent Pod 是否已经彻底消失？
		if !j.podExists(agentUID) {
			shouldCleanup = true
			reason = "AgentPodDisappeared"
		}

		// 判定逻辑 2: Sandbox CRD 是否存在？
		if !shouldCleanup {
			var sb apiv1alpha1.Sandbox
			err := j.K8sClient.Get(ctx, client.ObjectKey{Name: sandboxName, Namespace: "default"}, &sb)
			if err != nil {
				if strings.Contains(err.Error(), "not found") {
					shouldCleanup = true
					reason = "SandboxCRDNotFound"
				}
			} else {
				// 判定逻辑 3: 检查 UID 是否匹配（防止重置后的旧残留）
				if claimUID != "" && string(sb.UID) != claimUID {
					shouldCleanup = true
					reason = "UIDMismatch"
				}
			}
		}

		if shouldCleanup {
			logger.Info("Found orphan container via CRD reconciliation", 
				"container", c.ID(), 
				"name", sandboxName, 
				"reason", reason)
			j.queue.Add(CleanupTask{
				ContainerID: c.ID(),
				AgentUID:    agentUID,
				PodName:     agentName,
			})
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