package controller

import (
	"context"
	"fmt"
	"time"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/controller/agentpool"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// SandboxPoolReconciler reconciles SandboxPool resources.
type SandboxPoolReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Registry agentpool.AgentRegistry
}

// Reconcile manages the lifecycle of Agent Pods based on the demand from Sandboxes.
func (r *SandboxPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pool apiv1alpha1.SandboxPool
	if err := r.Get(ctx, req.NamespacedName, &pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 1. 获取该 Pool 下所有的 Agent Pods
	var childPods corev1.PodList
	if err := r.List(ctx, &childPods, client.InNamespace(req.Namespace), client.MatchingLabels(poolLabels(pool.Name))); err != nil {
		return ctrl.Result{}, err
	}

	// 2. 获取该 Pool 下所有的 Sandboxes，用于计算负载
	var allSandboxes apiv1alpha1.SandboxList
	if err := r.List(ctx, &allSandboxes, client.InNamespace(req.Namespace)); err != nil {
		return ctrl.Result{}, err
	}

	var activeCount, pendingCount int32
	for _, sb := range allSandboxes.Items {
		// 只有属于这个池子的才统计
		if sb.Spec.PoolRef == pool.Name {
			if sb.Status.AssignedPod != "" {
				activeCount++
			} else {
				pendingCount++
			}
		}
	}
	logger.Info("Load statistics", "pool", pool.Name, "active", activeCount, "pending", pendingCount)

	// 3. 动态计算所需 Pod 数量
	maxPerPod := getAgentCapacity(&pool)
	if maxPerPod <= 0 {
		maxPerPod = 1
	}
	
	// 总需求量 = 正在跑的 + 正在排队的 + 最小缓冲区
	totalNeededSlots := activeCount + pendingCount + pool.Spec.Capacity.BufferMin
	desiredPods := (totalNeededSlots + maxPerPod - 1) / maxPerPod

	// 4. 应用 PoolMin / PoolMax 约束
	if desiredPods < pool.Spec.Capacity.PoolMin {
		desiredPods = pool.Spec.Capacity.PoolMin
	}
	if pool.Spec.Capacity.PoolMax > 0 && desiredPods > pool.Spec.Capacity.PoolMax {
		desiredPods = pool.Spec.Capacity.PoolMax
	}

	currentCount := int32(len(childPods.Items))
	logger.Info("Scaling analysis", "pool", pool.Name, "current", currentCount, "desired", desiredPods)

	// 5. 执行扩缩容
	if currentCount < desiredPods {
		diff := desiredPods - currentCount
		logger.Info("Scaling up agent pool", "diff", diff)
		for i := int32(0); i < diff; i++ {
			pod := r.constructPod(&pool)
			if err := r.Create(ctx, pod); err != nil {
				logger.Error(err, "Failed to create agent pod")
				return ctrl.Result{}, err
			}
		}
	} else if currentCount > desiredPods {
		diff := currentCount - desiredPods
		logger.Info("Scaling down agent pool", "diff", diff)
		// 简单删除
		for i := int32(0); i < diff; i++ {
			pod := childPods.Items[i]
			if err := r.Delete(ctx, &pod); err != nil {
				logger.Error(err, "Failed to delete agent pod", "pod", pod.Name)
				return ctrl.Result{}, err
			}
		}
	}

	// 6. 更新 Status
	pool.Status.CurrentPods = currentCount
	pool.Status.TotalAgents = currentCount
	if err := r.Status().Update(ctx, &pool); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// constructPod builds an Agent Pod from the template with necessary runtime configurations injected.
func (r *SandboxPoolReconciler) constructPod(pool *apiv1alpha1.SandboxPool) *corev1.Pod {
	labels := make(map[string]string)
	for k, v := range pool.Spec.AgentTemplate.ObjectMeta.Labels {
		labels[k] = v
	}
	for k, v := range poolLabels(pool.Name) {
		labels[k] = v
	}

	podSpec := pool.Spec.AgentTemplate.Spec.DeepCopy()
	podSpec.HostNetwork = false
	podSpec.HostPID = false // 禁用宿主机 PID 命名空间，提高安全性

	if len(podSpec.Containers) > 0 {
		c := &podSpec.Containers[0]
		if c.SecurityContext == nil {
			c.SecurityContext = &corev1.SecurityContext{}
		}
		c.SecurityContext.Privileged = boolPtr(true)

		c.Env = append(c.Env,
			corev1.EnvVar{
				Name:      "NODE_NAME",
				ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}},
			},
			corev1.EnvVar{
				Name:      "POD_NAME",
				ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}},
			},
			corev1.EnvVar{
				Name:      "POD_IP",
				ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"}},
			},
			corev1.EnvVar{
				Name:      "POD_UID",
				ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}},
			},
			corev1.EnvVar{
				Name:      "CPU_LIMIT",
				ValueFrom: &corev1.EnvVarSource{ResourceFieldRef: &corev1.ResourceFieldSelector{ContainerName: "agent", Resource: "limits.cpu"}},
			},
			corev1.EnvVar{
				Name:      "MEMORY_LIMIT",
				ValueFrom: &corev1.EnvVarSource{ResourceFieldRef: &corev1.ResourceFieldSelector{ContainerName: "agent", Resource: "limits.memory"}},
			},
			corev1.EnvVar{
				Name:  "AGENT_CAPACITY",
				Value: fmt.Sprintf("%d", getAgentCapacity(pool)),
			},
			corev1.EnvVar{
				Name:  "RUNTIME_TYPE",
				Value: string(getRuntimeType(pool)),
			},
			corev1.EnvVar{Name: "RUNTIME_SOCKET", Value: "/run/containerd/containerd.sock"},
			corev1.EnvVar{Name: "INFRA_DIR_IN_POD", Value: "/opt/fast-sandbox/infra"},
		)

		c.VolumeMounts = append(c.VolumeMounts,
			corev1.VolumeMount{Name: "containerd-run", MountPath: "/run/containerd"},
			corev1.VolumeMount{Name: "containerd-root", MountPath: "/var/lib/containerd"},
			corev1.VolumeMount{Name: "tmp", MountPath: "/tmp"},
			corev1.VolumeMount{Name: "infra-tools", MountPath: "/opt/fast-sandbox/infra"},
		)

		if pool.Spec.RuntimeType == apiv1alpha1.RuntimeFirecracker {
			c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
				Name:      "kvm",
				MountPath: "/dev/kvm",
			})
		}
	}

	podSpec.InitContainers = append(podSpec.InitContainers, corev1.Container{
		Name:            "infra-init",
		Image:           "alpine:latest",
		ImagePullPolicy: corev1.PullIfNotPresent,
		// 使用 heredoc 确保脚本格式完美
		Command: []string{"sh", "-c", "cat <<'EOF' > /opt/fast-sandbox/infra/fs-helper\n#!/bin/sh\necho [FS-INFRA] Helper Initiated\nexec \"$@\"\nEOF\nchmod +x /opt/fast-sandbox/infra/fs-helper"},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "infra-tools", MountPath: "/opt/fast-sandbox/infra"},
		},
	})

	hostPathDirectory := corev1.HostPathDirectory
	hostPathFile := corev1.HostPathCharDev

	podSpec.Volumes = append(podSpec.Volumes,
		corev1.Volume{
			Name:         "containerd-run",
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/run/containerd", Type: &hostPathDirectory}},
		},
		corev1.Volume{
			Name:         "containerd-root",
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/containerd", Type: &hostPathDirectory}},
		},
		corev1.Volume{
			Name:         "tmp",
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/tmp", Type: &hostPathDirectory}},
		},
		corev1.Volume{
			Name: "infra-tools",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	)

	if pool.Spec.RuntimeType == apiv1alpha1.RuntimeFirecracker {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "kvm",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: "/dev/kvm", Type: &hostPathFile},
			},
		})
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: pool.Name + "-agent-",
			Namespace:    pool.Namespace,
			Labels:       labels,
		},
		Spec: *podSpec,
	}

	ctrl.SetControllerReference(pool, pod, r.Scheme)
	return pod
}

func poolLabels(poolName string) map[string]string {
	return map[string]string{
		"fast-sandbox.io/pool": poolName,
		"app":                  "sandbox-agent",
	}
}

func getAgentCapacity(pool *apiv1alpha1.SandboxPool) int32 {
	if pool.Spec.MaxSandboxesPerPod > 0 {
		return pool.Spec.MaxSandboxesPerPod
	}
	return 5
}

func getRuntimeType(pool *apiv1alpha1.SandboxPool) apiv1alpha1.RuntimeType {
	if pool.Spec.RuntimeType != "" {
		return pool.Spec.RuntimeType
	}
	return apiv1alpha1.RuntimeContainer
}

func boolPtr(b bool) *bool {
	return &b
}

// SetupWithManager sets up the controller with the Manager.
func (r *SandboxPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.SandboxPool{}).
		Owns(&corev1.Pod{}).
		Watches(&apiv1alpha1.Sandbox{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []ctrl.Request {
			sandbox, ok := obj.(*apiv1alpha1.Sandbox)
			if !ok {
				return nil
			}
			// 对于删除事件，obj 仍然包含被删除对象的信息
			if sandbox.Spec.PoolRef != "" {
				return []ctrl.Request{
					{NamespacedName: client.ObjectKey{Name: sandbox.Spec.PoolRef, Namespace: sandbox.Namespace}},
				}
			}
			return nil
		})).
		Complete(r)
}