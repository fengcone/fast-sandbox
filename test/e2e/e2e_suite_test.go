package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	sandboxv1alpha1 "fast-sandbox/api/v1alpha1"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	ctx       context.Context
	cancel    context.CancelFunc
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.Background())

	By("bootstrapping test environment")

	// 使用真实的 Kubernetes 集群（KIND）而不是 envtest
	// 这样可以测试真实的 Pod 创建、网络等功能
	// 注意：镜像构建和加载已经在 make e2e-prepare 中完成
	testEnv = &envtest.Environment{
		UseExistingCluster: boolPtr(true),
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = sandboxv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	By("Deploying Controller to cluster")
	err = deployControllerToCluster()
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	By("Cleaning up Controller deployment")
	cleanupController()

	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

func boolPtr(b bool) *bool {
	return &b
}

// getProjectRoot 获取项目根目录（包含 go.mod 的目录）
func getProjectRoot() (string, error) {
	currentDir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	dir := currentDir
	for {
		goModPath := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find project root (go.mod not found)")
		}
		dir = parent
	}
}

// deployControllerToCluster 部署 Controller 到集群
func deployControllerToCluster() error {
	projectRoot, err := getProjectRoot()
	if err != nil {
		return fmt.Errorf("failed to get project root: %w", err)
	}

	deploymentFile := filepath.Join(projectRoot, "test/e2e/fixtures/controller-deployment.yaml")
	GinkgoWriter.Printf("Deploying Controller from %s\n", deploymentFile)

	data, err := os.ReadFile(deploymentFile)
	if err != nil {
		return fmt.Errorf("failed to read deployment file: %w", err)
	}

	// 分割 YAML 文档（由 --- 分隔）
	docs := splitYAMLDocuments(data)
	for i, doc := range docs {
		if len(doc) == 0 {
			continue
		}

		obj := &unstructured.Unstructured{}
		decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(doc), 4096)
		if err := decoder.Decode(obj); err != nil {
			GinkgoWriter.Printf("Warning: failed to decode document %d: %v\n", i, err)
			continue
		}

		GinkgoWriter.Printf("Creating %s %s/%s\n", obj.GetKind(), obj.GetNamespace(), obj.GetName())
		if err := k8sClient.Create(ctx, obj); err != nil {
			// 如果资源已存在，先删除再创建
			if err := k8sClient.Delete(ctx, obj); err == nil {
				time.Sleep(2 * time.Second)
				if err := k8sClient.Create(ctx, obj); err != nil {
					return fmt.Errorf("failed to recreate %s: %w", obj.GetKind(), err)
				}
			}
		}
	}

	// 等待 Controller Deployment 就绪
	GinkgoWriter.Println("Waiting for Controller Deployment to be ready...")
	deploymentKey := types.NamespacedName{Name: "fast-sandbox-controller", Namespace: "default"}
	Eventually(func() bool {
		deployment := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx, deploymentKey, deployment); err != nil {
			return false
		}
		return deployment.Status.ReadyReplicas > 0
	}, time.Minute*2, time.Second*2).Should(BeTrue())

	GinkgoWriter.Println("Controller deployed successfully")
	return nil
}

// cleanupController 清理 Controller 部署
func cleanupController() {
	projectRoot, err := getProjectRoot()
	if err != nil {
		GinkgoWriter.Printf("Warning: failed to get project root: %v\n", err)
		return
	}

	deploymentFile := filepath.Join(projectRoot, "test/e2e/fixtures/controller-deployment.yaml")
	data, err := os.ReadFile(deploymentFile)
	if err != nil {
		GinkgoWriter.Printf("Warning: failed to read deployment file: %v\n", err)
		return
	}

	// 分割并删除所有资源
	docs := splitYAMLDocuments(data)
	for i, doc := range docs {
		if len(doc) == 0 {
			continue
		}

		obj := &unstructured.Unstructured{}
		decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(doc), 4096)
		if err := decoder.Decode(obj); err != nil {
			GinkgoWriter.Printf("Warning: failed to decode document %d: %v\n", i, err)
			continue
		}

		GinkgoWriter.Printf("Deleting %s %s/%s\n", obj.GetKind(), obj.GetNamespace(), obj.GetName())
		k8sClient.Delete(ctx, obj)
	}
}

// splitYAMLDocuments 分割 YAML 文档
func splitYAMLDocuments(data []byte) [][]byte {
	// 简单实现：按 "---" 分割
	return bytes.Split(data, []byte("\n---\n"))
}

// LoadYAMLToObject 从 YAML 文件加载对象
func LoadYAMLToObject(filename string, obj client.Object) error {
	projectRoot, err := getProjectRoot()
	if err != nil {
		return fmt.Errorf("failed to get project root: %w", err)
	}

	filePath := filepath.Join(projectRoot, "test/e2e/fixtures", filename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	if err := decoder.Decode(obj); err != nil {
		return fmt.Errorf("failed to decode YAML: %w", err)
	}

	return nil
}
