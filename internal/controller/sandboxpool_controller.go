package controller

import (
	"context"
	"fmt"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/controller/agentpool"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// SandboxPoolReconciler reconciles SandboxPool resources.
type SandboxPoolReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Registry agentpool.AgentRegistry
}

// Reconcile currently manages SandboxPool pods and updates status.
func (r *SandboxPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var pool apiv1alpha1.SandboxPool
	if err := r.Get(ctx, req.NamespacedName, &pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 1. List Child Pods managed by this pool
	var childPods corev1.PodList
	if err := r.List(ctx, &childPods, client.InNamespace(req.Namespace), client.MatchingLabels(poolLabels(pool.Name))); err != nil {
		return ctrl.Result{}, err
	}

	// 2. Update Status (Simple count)
	// In a real implementation, we should count Ready/Running pods
	// pool.Status.CurrentPods = int32(len(childPods.Items))
	// r.Status().Update(ctx, &pool)

	// 3. Scaling Logic (Ensure Min Capacity)
	targetCount := int(pool.Spec.Capacity.PoolMin)
	currentCount := len(childPods.Items)

	if currentCount < targetCount {
		// Scale Up
		diff := targetCount - currentCount
		log.Info("Scaling up agent pool", "pool", pool.Name, "current", currentCount, "target", targetCount, "diff", diff)

		for i := 0; i < diff; i++ {
			pod := r.constructPod(&pool)
			if err := r.Create(ctx, pod); err != nil {
				log.Error(err, "Failed to create agent pod", "pool", pool.Name)
				return ctrl.Result{}, err
			}
		}
	} else if currentCount > targetCount {
		// Scale Down (Simple: delete extra pods)
		// TODO: Implement smarter scale down (prefer idle agents)
		diff := currentCount - targetCount
		log.Info("Scaling down agent pool", "pool", pool.Name, "current", currentCount, "target", targetCount, "diff", diff)

		// Delete last N pods for now
		for i := 0; i < diff; i++ {
			pod := childPods.Items[i]
			if err := r.Delete(ctx, &pod); err != nil {
				log.Error(err, "Failed to delete agent pod", "pod", pod.Name)
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, nil
}

// constructPod builds an Agent Pod from the template with necessary runtime configurations injected.
func (r *SandboxPoolReconciler) constructPod(pool *apiv1alpha1.SandboxPool) *corev1.Pod {
	labels := make(map[string]string)
	// Copy labels from template
	for k, v := range pool.Spec.AgentTemplate.ObjectMeta.Labels {
		labels[k] = v
	}
	// Add controller management labels
	for k, v := range poolLabels(pool.Name) {
		labels[k] = v
	}

	// Deep copy spec to avoid mutating cache
	podSpec := pool.Spec.AgentTemplate.Spec.DeepCopy()

	// --- Injection Logic Start ---

	// 1. Network Namespace (HostPID removed for security)
	podSpec.HostNetwork = false // 禁用宿主机网络，使用 Pod 独立网络

	// 2. Containers Injection
	if len(podSpec.Containers) > 0 {
		// Inject into the first container (assumed to be the agent)
		c := &podSpec.Containers[0]

		// Security Context
		if c.SecurityContext == nil {
			c.SecurityContext = &corev1.SecurityContext{}
		}
		c.SecurityContext.Privileged = boolPtr(true)

		// Environment Variables
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
				Name:  "AGENT_CAPACITY",
				Value: fmt.Sprintf("%d", getAgentCapacity(pool)),
			},
			corev1.EnvVar{
				Name:  "RUNTIME_TYPE",
				Value: string(getRuntimeType(pool)),
			},
			corev1.EnvVar{Name: "RUNTIME_SOCKET", Value: "/run/containerd/containerd.sock"},
		)

		// Volume Mounts
		c.VolumeMounts = append(c.VolumeMounts,
			corev1.VolumeMount{Name: "containerd-run", MountPath: "/run/containerd"},
			corev1.VolumeMount{Name: "containerd-root", MountPath: "/var/lib/containerd"},
			corev1.VolumeMount{Name: "tmp", MountPath: "/tmp"},
		)

		// Firecracker 模式需要透传 KVM 设备
		if pool.Spec.RuntimeType == apiv1alpha1.RuntimeFirecracker {
			c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
				Name:      "kvm",
				MountPath: "/dev/kvm",
			})
		}
	}

	// 3. Volumes Injection
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
	)

	if pool.Spec.RuntimeType == apiv1alpha1.RuntimeFirecracker {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "kvm",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: "/dev/kvm", Type: &hostPathFile},
			},
		})
	}

	// --- Injection Logic End ---

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: pool.Name + "-agent-",
			Namespace:    pool.Namespace,
			Labels:       labels,
		},
		Spec: *podSpec,
	}

	// Set Owner Reference
	ctrl.SetControllerReference(pool, pod, r.Scheme)

	return pod
}

func poolLabels(poolName string) map[string]string {
	return map[string]string{
		"fast-sandbox.io/pool": poolName,
		"app":                  "sandbox-agent", // Standard label for agent discovery
	}
}

func getAgentCapacity(pool *apiv1alpha1.SandboxPool) int32 {
	if pool.Spec.MaxSandboxesPerPod > 0 {
		return pool.Spec.MaxSandboxesPerPod
	}
	return 5 // 默认容量
}

func getRuntimeType(pool *apiv1alpha1.SandboxPool) apiv1alpha1.RuntimeType {
	if pool.Spec.RuntimeType != "" {
		return pool.Spec.RuntimeType
	}
	return apiv1alpha1.RuntimeContainer // 默认使用普通容器
}

func boolPtr(b bool) *bool {
	return &b
}

// SetupWithManager sets up the controller with the Manager.
func (r *SandboxPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.SandboxPool{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
