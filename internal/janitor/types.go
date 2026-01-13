package janitor

import (
	"sync"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"k8s.io/client-go/kubernetes"
	listerv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// defaultOrphanTimeout 是孤儿清理的默认超时时间
	// 在 Fast 模式下，如果容器创建后 CRD 在此时间内仍未出现，则判定为孤儿
	defaultOrphanTimeout = 10 * time.Second
)

type Janitor struct {
	kubeClient kubernetes.Interface
	K8sClient  client.Client
	ctrdClient *containerd.Client
	nodeName   string
	namespaces []string

	queue     workqueue.RateLimitingInterface
	podLister listerv1.PodLister

	// 用于防止并发清理同一个容器

	cleaning sync.Map // containerID -> struct{}{}

	ScanInterval time.Duration

	OrphanTimeout time.Duration // Fast 模式下的孤儿清理超时时间

}

// CleanupTask 定义一个清理任务
type CleanupTask struct {
	ContainerID string
	AgentUID    string
	PodName     string
	Namespace   string
}
