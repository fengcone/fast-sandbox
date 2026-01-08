package janitor

import (
	"sync"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"k8s.io/client-go/kubernetes"
	listerv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/util/workqueue"
)

type Janitor struct {
	kubeClient kubernetes.Interface
	ctrdClient *containerd.Client
	nodeName   string
	namespaces []string // K8s 命名空间（用于监听 Pod）

	queue     workqueue.RateLimitingInterface
	podLister listerv1.PodLister
	
	// 用于防止并发清理同一个容器
	cleaning sync.Map // containerID -> struct{}{}

	scanInterval time.Duration
}

// CleanupTask 定义一个清理任务
type CleanupTask struct {
	ContainerID string
	AgentUID    string
	PodName     string
	Namespace   string
}
