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

type Janitor struct {
	kubeClient kubernetes.Interface
	K8sClient  client.Client // 增加通用客户端以访问 CRD
	ctrdClient *containerd.Client
	nodeName   string
	namespaces []string 

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
