package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"fast-sandbox/internal/agent/infra"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
)

// ContainerdRuntime 实现基于 containerd 的容器运行时
type ContainerdRuntime struct {
	mu         sync.RWMutex
	socketPath string
	client     *containerd.Client
	sandboxes  map[string]*SandboxMetadata // sandboxID -> metadata
	cgroupPath string                      // Pod 的 cgroup 路径
	netnsPath  string                      // Pod 的 network namespace 路径
	agentID    string                      // Agent 名称 (Pod Name)
	agentUID   string                      // Agent 唯一标识 (Pod UID)
	infraMgr   *infra.Manager              // 基础设施插件管理
}

// Initialize 初始化 containerd 客户端
func (r *ContainerdRuntime) Initialize(ctx context.Context, socketPath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.socketPath = socketPath
	if r.socketPath == "" {
		r.socketPath = "/run/containerd/containerd.sock"
	}

	client, err := containerd.New(r.socketPath, containerd.WithDefaultNamespace("k8s.io"))
	if err != nil {
		return fmt.Errorf("failed to create containerd client: %w", err)
	}

	r.client = client
	r.sandboxes = make(map[string]*SandboxMetadata)
	r.agentID = os.Getenv("POD_NAME")
	r.agentUID = os.Getenv("POD_UID")

	// 初始化基础设施管理器
	infraPodPath := os.Getenv("INFRA_DIR_IN_POD")
	if infraPodPath == "" {
		infraPodPath = "/opt/fast-sandbox/infra"
	}
	r.infraMgr = infra.NewManager(infraPodPath)

	// 探测 Cgroup 路径 (仅用于日志和未来扩展)
	if err := r.discoverCgroupPath(); err != nil {
		fmt.Printf("Warning: failed to discover cgroup path: %v\n", err)
		r.cgroupPath = "" 
	}

	if err := r.discoverNetNSPath(ctx); err != nil {
		fmt.Printf("Warning: failed to discover network namespace: %v\n", err)
	}

	return nil
}

func (r *ContainerdRuntime) discoverCgroupPath() error {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil { return err }
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "0::") {
			r.cgroupPath = strings.TrimPrefix(line, "0::")
			return nil
		}
		parts := strings.Split(line, ":")
		if len(parts) == 3 && (strings.Contains(parts[1], "pids") || strings.Contains(parts[1], "cpu")) {
			r.cgroupPath = parts[2]
			return nil
		}
	}
	return fmt.Errorf("cgroup path not found")
}

func (r *ContainerdRuntime) discoverNetNSPath(ctx context.Context) error {
	if r.cgroupPath == "" { return fmt.Errorf("cgroup path is required") }
	var containerID string
	if strings.Contains(r.cgroupPath, "cri-containerd-") {
		parts := strings.Split(r.cgroupPath, "cri-containerd-")
		containerID = strings.Split(parts[1], ".")[0]
	} else if strings.Contains(r.cgroupPath, "cri-containerd:") {
		parts := strings.Split(r.cgroupPath, "cri-containerd:")
		containerID = parts[len(parts)-1]
	} else if strings.Contains(r.cgroupPath, "kubepods") {
		parts := strings.Split(strings.Trim(r.cgroupPath, "/"), "/")
		containerID = parts[len(parts)-1]
	} else {
		return fmt.Errorf("could not parse ID")
	}

	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	container, err := r.client.LoadContainer(ctx, containerID)
	if err != nil { return err }
	spec, err := container.Spec(ctx)
	if err != nil { return err }
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			if ns.Path != "" {
				r.netnsPath = ns.Path
				return nil
			}
		}
	}
	return fmt.Errorf("netns not found")
}

func (r *ContainerdRuntime) CreateSandbox(ctx context.Context, config *SandboxConfig) (*SandboxMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx = namespaces.WithNamespace(ctx, "k8s.io")

	image, err := r.prepareImage(ctx, config.Image)
	if err != nil { return nil, err }

	containerID := config.SandboxID
	specOpts := r.prepareSpecOpts(config, image)
	labels := r.prepareLabels(config)

	container, err := r.client.NewContainer(
		ctx,
		containerID,
		containerd.WithImage(image),
		containerd.WithNewSnapshot(containerID+"-snapshot", image),
		containerd.WithNewSpec(specOpts...),
		containerd.WithContainerLabels(labels),
	)
	if err != nil { return nil, err }

	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
	if err != nil {
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, err
	}

	if err := task.Start(ctx); err != nil {
		task.Delete(ctx)
		container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, err
	}

	metadata := &SandboxMetadata{
		SandboxID:   config.SandboxID,
		ClaimUID:    config.ClaimUID,
		ClaimName:   config.ClaimName,
		ContainerID: containerID,
		Image:       config.Image,
		Status:      "running",
		CreatedAt:   time.Now().Unix(),
		PID:         int(task.Pid()),
	}
	r.sandboxes[config.SandboxID] = metadata
	return metadata, nil
}

func (r *ContainerdRuntime) prepareImage(ctx context.Context, imageName string) (containerd.Image, error) {
	image, err := r.client.GetImage(ctx, imageName)
	if err != nil {
		image, err = r.client.Pull(ctx, imageName, containerd.WithPullUnpack)
		if err != nil { return nil, err }
	}
	return image, nil
}

func (r *ContainerdRuntime) prepareSpecOpts(config *SandboxConfig, image containerd.Image) []oci.SpecOpts {
	// 原始命令与参数
	originalArgs := append(config.Command, config.Args...)
	
	// --- 插件注入逻辑 ---
	var mounts []specs.Mount
	finalArgs := originalArgs

	if r.infraMgr != nil {
		plugins := r.infraMgr.GetPlugins()
		for _, p := range plugins {
			hostPath := r.infraMgr.GetHostPath(p.BinName)
			if hostPath == "" {
				continue
			}

			// A. 添加挂载点
			mounts = append(mounts, specs.Mount{
				Source:      hostPath,
				Destination: p.ContainerPath,
				Type:        "bind",
				Options:     []string{"ro", "rbind"}, // 只读绑定
			})

			// B. 命令包装 (如果是 Wrapper)
			if p.IsWrapper {
				// 包装逻辑: [plugin_path, --, original_cmd...]
				// 注意：这里简单实现单包装器，多包装器需递归
				wrapped := []string{p.ContainerPath, "--"}
				finalArgs = append(wrapped, finalArgs...)
			}
		}
	}

	specOpts := []oci.SpecOpts{
		oci.WithImageConfig(image),
		oci.WithProcessArgs(finalArgs...),
		oci.WithEnv(envMapToSlice(config.Env)),
	}

	// 应用挂载点
	if len(mounts) > 0 {
		specOpts = append(specOpts, oci.WithMounts(mounts))
	}

	if r.netnsPath != "" {
		specOpts = append(specOpts, oci.WithLinuxNamespace(specs.LinuxNamespace{
			Type: specs.NetworkNamespace,
			Path: r.netnsPath,
		}))
	}

	// Slot 资源分配逻辑
	if cpu, mem, err := r.calculateSlotResources(); err == nil && (cpu > 0 || mem > 0) {
		fmt.Printf("RESOURCES_VERIFY: Slot allocated for %s: CPU=%dm, Memory=%d bytes\n", config.SandboxID, cpu, mem)
		// 注入环境变量，让沙箱感知软限额
		specOpts = append(specOpts, oci.WithEnv([]string{
			fmt.Sprintf("SANDBOX_CPU_LIMIT_MCORE=%d", cpu),
			fmt.Sprintf("SANDBOX_MEM_LIMIT_BYTES=%d", mem),
		}))
	}

	return specOpts
}

func (r *ContainerdRuntime) calculateSlotResources() (int64, int64, error) {
	capacityStr := os.Getenv("AGENT_CAPACITY")
	var capacity int64 = 5
	fmt.Sscanf(capacityStr, "%d", &capacity)
	if capacity <= 0 { capacity = 1 }

	cpuLimit := os.Getenv("CPU_LIMIT")
	var totalCPU int64
	if strings.HasSuffix(cpuLimit, "m") {
		fmt.Sscanf(strings.TrimSuffix(cpuLimit, "m"), "%d", &totalCPU)
	} else {
		var cpuCores float64
		fmt.Sscanf(cpuLimit, "%f", &cpuCores)
		totalCPU = int64(cpuCores * 1000)
	}

	totalMem := parseMemoryToBytes(os.Getenv("MEMORY_LIMIT"))
	if totalCPU == 0 && totalMem == 0 { return 0, 0, fmt.Errorf("no limits") }
	return totalCPU / capacity, totalMem / capacity, nil
}

func parseMemoryToBytes(s string) int64 {
	var val float64
	if strings.HasSuffix(s, "Gi") {
		fmt.Sscanf(strings.TrimSuffix(s, "Gi"), "%f", &val)
		return int64(val * 1024 * 1024 * 1024)
	}
	if strings.HasSuffix(s, "Mi") {
		fmt.Sscanf(strings.TrimSuffix(s, "Mi"), "%f", &val)
		return int64(val * 1024 * 1024)
	}
	fmt.Sscanf(s, "%f", &val)
	return int64(val)
}

func (r *ContainerdRuntime) prepareLabels(config *SandboxConfig) map[string]string {
	return map[string]string{
		"fast-sandbox.io/managed":      "true",
		"fast-sandbox.io/agent-name":   r.agentID,
		"fast-sandbox.io/agent-uid":    r.agentUID,
		"fast-sandbox.io/id":           config.SandboxID,
		"fast-sandbox.io/claim-uid":    config.ClaimUID,
		"fast-sandbox.io/sandbox-name": config.ClaimName, // 规范化标签名
	}
}

func (r *ContainerdRuntime) DeleteSandbox(ctx context.Context, sandboxID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	container, err := r.client.LoadContainer(ctx, sandboxID)
	if err != nil {
		delete(r.sandboxes, sandboxID)
		return nil
	}
	task, err := container.Task(ctx, nil)
	if err == nil {
		task.Kill(ctx, syscall.SIGKILL)
		task.Delete(ctx)
	}
	container.Delete(ctx, containerd.WithSnapshotCleanup)
	delete(r.sandboxes, sandboxID)
	return nil
}

func (r *ContainerdRuntime) GetSandbox(ctx context.Context, sandboxID string) (*SandboxMetadata, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sandboxes[sandboxID], nil
}

func (r *ContainerdRuntime) ListSandboxes(ctx context.Context) ([]*SandboxMetadata, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.client == nil { return nil, nil }
	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	filter := fmt.Sprintf("labels.\"fast-sandbox.io/managed\"==\"true\",labels.\"fast-sandbox.io/agent-uid\"==\"%s\"", r.agentUID)
	containers, err := r.client.Containers(ctx, filter)
	if err != nil { return nil, err }
	var list []*SandboxMetadata
	for _, c := range containers {
		info, _ := c.Info(ctx)
		status := "unknown"
		if task, err := c.Task(ctx, nil); err == nil {
			if s, err := task.Status(ctx); err == nil { status = string(s.Status) }
		}
		list = append(list, &SandboxMetadata{
			SandboxID:   info.Labels["fast-sandbox.io/id"],
			ClaimUID:    info.Labels["fast-sandbox.io/claim-uid"],
			ClaimName:   info.Labels["fast-sandbox.io/claim-nm"],
			ContainerID: c.ID(),
			Image:       info.Image,
			Status:      status,
			CreatedAt:   info.CreatedAt.Unix(),
		})
	}
	return list, nil
}

func (r *ContainerdRuntime) ListImages(ctx context.Context) ([]string, error) {
	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	images, err := r.client.ListImages(ctx)
	if err != nil { return nil, err }
	var names []string
	for _, img := range images { names = append(names, img.Name()) }
	return names, nil
}

func (r *ContainerdRuntime) PullImage(ctx context.Context, image string) error {
	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	_, err := r.client.GetImage(ctx, image)
	if err == nil { return nil }
	_, err = r.client.Pull(ctx, image, containerd.WithPullUnpack)
	return err
}

func (r *ContainerdRuntime) Close() error {
	if r.client != nil { return r.client.Close() }
	return nil
}

func envMapToSlice(env map[string]string) []string {
	var res []string
	for k, v := range env { res = append(res, fmt.Sprintf("%s=%s", k, v)) }
	return res
}