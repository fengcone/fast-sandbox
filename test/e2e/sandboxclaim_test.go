package e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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
			By("Creating a SandboxPool from YAML")
			sandboxPool = &sandboxv1alpha1.SandboxPool{}
			err := LoadYAMLToObject("sandboxpool.yaml", sandboxPool)
			Expect(err).NotTo(HaveOccurred())

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
			By("Creating a SandboxPool first from YAML")
			sandboxPool = &sandboxv1alpha1.SandboxPool{}
			err := LoadYAMLToObject("sandboxpool.yaml", sandboxPool)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Create(ctx, sandboxPool)).Should(Succeed())

			// 等待 Agent Pods 就绪
			GinkgoWriter.Println("Waiting for Agent Pods to be created and ready...")
			Eventually(func() int {
				podList := &corev1.PodList{}
				k8sClient.List(ctx, podList,
					client.InNamespace(namespace),
					client.MatchingLabels{"sandbox.fast.io/pool": poolName})

				readyCount := 0
				for _, pod := range podList.Items {
					if isPodReady(&pod) {
						GinkgoWriter.Printf("  Agent Pod Ready: %s\n", pod.Name)
						readyCount++
					}
				}
				GinkgoWriter.Printf("  Ready count: %d/2\n", readyCount)
				return readyCount
			}, timeout, interval).Should(BeNumerically(">=", 2))

			// 额外等待一下，确保 Controller 已注册 Agents
			GinkgoWriter.Println("Waiting a bit more to ensure Agents are registered...")
			time.Sleep(3 * time.Second)
		})

		It("Should schedule to an Agent Pod from the specified pool", func() {
			By("Creating a SandboxClaim from YAML")
			sandboxClaim = &sandboxv1alpha1.SandboxClaim{}
			err := LoadYAMLToObject("sandboxclaim.yaml", sandboxClaim)
			Expect(err).NotTo(HaveOccurred())

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

			assignedPodName := claim.Status.AssignedAgentPod
			GinkgoWriter.Printf("Assigned Agent Pod: %s\n", assignedPodName)

			// 列出当前所有 Agent Pods
			podList := &corev1.PodList{}
			k8sClient.List(ctx, podList,
				client.InNamespace(namespace),
				client.MatchingLabels{"sandbox.fast.io/pool": poolName})
			GinkgoWriter.Println("Current Agent Pods:")
			for _, pod := range podList.Items {
				GinkgoWriter.Printf("  - %s (Phase: %s, Ready: %v)\n", pod.Name, pod.Status.Phase, isPodReady(&pod))
			}

			// 验证分配的 Pod 属于指定的 Pool
			// 注意：Agent Pod 可能刚创建不久，需要使用 Eventually
			Eventually(func() bool {
				pod := &corev1.Pod{}
				podKey := types.NamespacedName{Name: assignedPodName, Namespace: namespace}
				if err := k8sClient.Get(ctx, podKey, pod); err != nil {
					GinkgoWriter.Printf("Failed to get pod %s: %v\n", assignedPodName, err)
					return false
				}
				return pod.Labels["sandbox.fast.io/pool"] == poolName
			}, timeout, interval).Should(BeTrue(), "Assigned Agent Pod should exist and belong to the pool")
		})
	})
})

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
