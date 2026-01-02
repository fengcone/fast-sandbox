package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sandboxv1alpha1 "fast-sandbox/api/v1alpha1"
)

var _ = Describe("SandboxClaim Scheduling", func() {
	const (
		timeout  = time.Second * 60
		interval = time.Second * 2
	)

	var (
		poolName     = "test-sandbox-pool"
		claimName    = "test-claim-e2e"
		namespace    = "default"
		sandboxPool  *sandboxv1alpha1.SandboxPool
		sandboxClaim *sandboxv1alpha1.SandboxClaim
	)

	BeforeEach(func() {
		By("Cleaning up existing resources")
		// 删除旧的 SandboxClaim
		claimList := &sandboxv1alpha1.SandboxClaimList{}
		err := k8sClient.List(ctx, claimList, client.InNamespace(namespace))
		if err == nil {
			for i := range claimList.Items {
				k8sClient.Delete(ctx, &claimList.Items[i])
			}
		}

		// 删除旧的 SandboxPool
		poolList := &sandboxv1alpha1.SandboxPoolList{}
		err = k8sClient.List(ctx, poolList, client.InNamespace(namespace))
		if err == nil {
			for i := range poolList.Items {
				k8sClient.Delete(ctx, &poolList.Items[i])
			}
		}

		// 等待资源清理完成
		time.Sleep(5 * time.Second)
	})

	AfterEach(func() {
		By("Cleaning up test resources")
		if sandboxClaim != nil {
			k8sClient.Delete(ctx, sandboxClaim)
		}
		if sandboxPool != nil {
			k8sClient.Delete(ctx, sandboxPool)
		}
	})

	Context("When creating a SandboxPool", func() {
		It("Should create Agent Pods successfully", func() {
			By("Creating a SandboxPool")
			sandboxPool = &sandboxv1alpha1.SandboxPool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      poolName,
					Namespace: namespace,
				},
				Spec: sandboxv1alpha1.SandboxPoolSpec{
					Capacity: sandboxv1alpha1.PoolCapacity{
						PoolMin:   2,
						PoolMax:   5,
						BufferMin: 1,
						BufferMax: 3,
					},
					AgentTemplate: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": "sandbox-agent",
							},
						},
						Spec: corev1.PodSpec{
							ServiceAccountName: "default",
							Containers: []corev1.Container{
								{
									Name:            "agent",
									Image:           "fast-sandbox-agent:dev",
									ImagePullPolicy: corev1.PullIfNotPresent,
									Env: []corev1.EnvVar{
										{
											Name: "POD_NAME",
											ValueFrom: &corev1.EnvVarSource{
												FieldRef: &corev1.ObjectFieldSelector{
													FieldPath: "metadata.name",
												},
											},
										},
										{
											Name: "POD_IP",
											ValueFrom: &corev1.EnvVarSource{
												FieldRef: &corev1.ObjectFieldSelector{
													FieldPath: "status.podIP",
												},
											},
										},
										{
											Name: "NODE_NAME",
											ValueFrom: &corev1.EnvVarSource{
												FieldRef: &corev1.ObjectFieldSelector{
													FieldPath: "spec.nodeName",
												},
											},
										},
										{
											Name: "NAMESPACE",
											ValueFrom: &corev1.EnvVarSource{
												FieldRef: &corev1.ObjectFieldSelector{
													FieldPath: "metadata.namespace",
												},
											},
										},
									},
									Ports: []corev1.ContainerPort{
										{
											Name:          "agent-http",
											ContainerPort: 8081,
											Protocol:      corev1.ProtocolTCP,
										},
										{
											Name:          "dlv-debug",
											ContainerPort: 2345,
											Protocol:      corev1.ProtocolTCP,
										},
									},
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU:    mustParseQuantity("100m"),
											corev1.ResourceMemory: mustParseQuantity("128Mi"),
										},
										Limits: corev1.ResourceList{
											corev1.ResourceCPU:    mustParseQuantity("100m"),
											corev1.ResourceMemory: mustParseQuantity("128Mi"),
										},
									},
								},
							},
							RestartPolicy: corev1.RestartPolicyAlways,
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, sandboxPool)).Should(Succeed())

			By("Waiting for Agent Pods to be ready")
			Eventually(func() int {
				podList := &corev1.PodList{}
				err := k8sClient.List(ctx, podList,
					client.InNamespace(namespace),
					client.MatchingLabels{"sandbox.fast.io/pool": poolName})
				if err != nil {
					return 0
				}

				readyCount := 0
				for _, pod := range podList.Items {
					if pod.Status.Phase == corev1.PodRunning {
						for _, cond := range pod.Status.Conditions {
							if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
								readyCount++
								break
							}
						}
					}
				}
				return readyCount
			}, timeout, interval).Should(BeNumerically(">=", 2))

			By("Verifying SandboxPool status")
			poolKey := types.NamespacedName{Name: poolName, Namespace: namespace}
			Eventually(func() int32 {
				pool := &sandboxv1alpha1.SandboxPool{}
				err := k8sClient.Get(ctx, poolKey, pool)
				if err != nil {
					return 0
				}
				return pool.Status.ReadyPods
			}, timeout, interval).Should(BeNumerically(">=", 2))
		})
	})

	Context("When creating a SandboxClaim with poolRef", func() {
		BeforeEach(func() {
			By("Creating a SandboxPool first")
			sandboxPool = &sandboxv1alpha1.SandboxPool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      poolName,
					Namespace: namespace,
				},
				Spec: sandboxv1alpha1.SandboxPoolSpec{
					Capacity: sandboxv1alpha1.PoolCapacity{
						PoolMin:   2,
						PoolMax:   5,
						BufferMin: 1,
						BufferMax: 3,
					},
					AgentTemplate: createAgentPodTemplate(),
				},
			}
			Expect(k8sClient.Create(ctx, sandboxPool)).Should(Succeed())

			// 等待 Agent Pods 就绪
			Eventually(func() int {
				podList := &corev1.PodList{}
				k8sClient.List(ctx, podList,
					client.InNamespace(namespace),
					client.MatchingLabels{"sandbox.fast.io/pool": poolName})

				readyCount := 0
				for _, pod := range podList.Items {
					if isPodReady(&pod) {
						readyCount++
					}
				}
				return readyCount
			}, timeout, interval).Should(BeNumerically(">=", 2))
		})

		It("Should schedule to an Agent Pod from the specified pool", func() {
			By("Creating a SandboxClaim with poolRef")
			sandboxClaim = &sandboxv1alpha1.SandboxClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      claimName,
					Namespace: namespace,
				},
				Spec: sandboxv1alpha1.SandboxClaimSpec{
					Image:  "nginx:latest",
					CPU:    "100m",
					Memory: "128Mi",
					Port:   8080,
					PoolRef: &sandboxv1alpha1.PoolReference{
						Name:      poolName,
						Namespace: namespace,
					},
				},
			}

			Expect(k8sClient.Create(ctx, sandboxClaim)).Should(Succeed())

			By("Waiting for SandboxClaim to be scheduled")
			claimKey := types.NamespacedName{Name: claimName, Namespace: namespace}
			Eventually(func() string {
				claim := &sandboxv1alpha1.SandboxClaim{}
				err := k8sClient.Get(ctx, claimKey, claim)
				if err != nil {
					return ""
				}
				return claim.Status.Phase
			}, timeout, interval).Should(Equal("Scheduling"))

			By("Verifying the assigned Agent Pod belongs to the pool")
			claim := &sandboxv1alpha1.SandboxClaim{}
			Expect(k8sClient.Get(ctx, claimKey, claim)).Should(Succeed())
			Expect(claim.Status.AssignedAgentPod).ShouldNot(BeEmpty())

			// 验证分配的 Pod 属于指定的 Pool
			assignedPodName := claim.Status.AssignedAgentPod
			pod := &corev1.Pod{}
			podKey := types.NamespacedName{Name: assignedPodName, Namespace: namespace}
			Expect(k8sClient.Get(ctx, podKey, pod)).Should(Succeed())
			Expect(pod.Labels["sandbox.fast.io/pool"]).Should(Equal(poolName))
		})
	})
})

// Helper functions

func mustParseQuantity(s string) resource.Quantity {
	q := resource.MustParse(s)
	return q
}

func isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func createAgentPodTemplate() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"app": "sandbox-agent",
			},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: "default",
			Containers: []corev1.Container{
				{
					Name:            "agent",
					Image:           "fast-sandbox-agent:dev",
					ImagePullPolicy: corev1.PullIfNotPresent,
					Env: []corev1.EnvVar{
						{
							Name: "POD_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "metadata.name",
								},
							},
						},
						{
							Name: "POD_IP",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "status.podIP",
								},
							},
						},
						{
							Name: "NODE_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "spec.nodeName",
								},
							},
						},
						{
							Name: "NAMESPACE",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "metadata.namespace",
								},
							},
						},
					},
					Ports: []corev1.ContainerPort{
						{
							Name:          "agent-http",
							ContainerPort: 8081,
							Protocol:      corev1.ProtocolTCP,
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyAlways,
		},
	}
}
