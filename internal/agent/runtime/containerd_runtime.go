package runtime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	mu                 sync.RWMutex
	socketPath         string
	client             *containerd.Client
	sandboxes          map[string]*SandboxMetadata // sandboxID -> metadata
	cgroupPath         string                      // Pod 的 cgroup 路径
	netnsPath          string                      // Pod 的 network namespace 路径
	agentID            string                      // Agent 名称 (Pod Name)
	agentUID           string                      // Agent 唯一标识 (Pod UID)
	agentNamespace     string                      // Agent 运行的命名空间
	infraMgr           *infra.Manager              // 基础设施插件管理
	allowedPluginPaths []string                    // 允许的插件路径白名单
}

const (
	// 默认操作超时时间
	defaultOperationTimeout = 30 * time.Second
	// 容器停止超时时间
	containerStopTimeout = 10 * time.Second
)

// Initialize 初始化 containerd 客户端
func (r *ContainerdRuntime) Initialize(ctx context.Context, socketPath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.socketPath = socketPath
	if r.socketPath == "" {
		r.socketPath = "/run/containerd/containerd.sock"
	}

	// 添加超时保护
	ctx, cancel := context.WithTimeout(ctx, defaultOperationTimeout)
	defer cancel()

	client, err := containerd.New(r.socketPath, containerd.WithDefaultNamespace("k8s.io"))
	if err != nil {
		return fmt.Errorf("failed to create containerd client: %w", err)
	}

	r.client = client
	r.sandboxes = make(map[string]*SandboxMetadata)
	r.agentID = os.Getenv("POD_NAME")
	r.agentUID = os.Getenv("POD_UID")

	// 配置允许的插件路径白名单
	// 从环境变量读取，默认为 /opt/fast-sandbox/infra
	allowedPaths := os.Getenv("ALLOWED_PLUGIN_PATHS")
	if allowedPaths != "" {
		r.allowedPluginPaths = strings.Split(allowedPaths, ":")
	} else {
		infraPodPath := os.Getenv("INFRA_DIR_IN_POD")
		if infraPodPath == "" {
			infraPodPath = "/opt/fast-sandbox/infra"
		}
		r.allowedPluginPaths = []string{infraPodPath}
	}

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

	// 探测 Agent Pod 的网络命名空间路径（用于共享给所有 Sandbox）
	if err := r.discoverNetNSPath(ctx); err != nil {
		fmt.Printf("Warning: failed to discover network namespace: %v\n", err)
	}

	return nil
}

func (r *ContainerdRuntime) discoverCgroupPath() error {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return err
	}
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
	if r.cgroupPath == "" {
		return fmt.Errorf("cgroup path is required")
	}
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
	if err != nil {
		return err
	}
	spec, err := container.Spec(ctx)
	if err != nil {
		return err
	}
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

	// 添加超时保护
	ctx, cancel := context.WithTimeout(ctx, defaultOperationTimeout)
	defer cancel()

	ctx = namespaces.WithNamespace(ctx, "k8s.io")

	image, err := r.prepareImage(ctx, config.Image)
	if err != nil {
		return nil, err
	}

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
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	// 准备日志文件
	logDir := "/var/log/fast-sandbox"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log dir: %w", err)
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("%s.log", containerID))

	// 打开日志文件 (追加模式)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}
	// 注意：Task 结束时 containerd 会关闭流，但我们需要确保这里的 handle 不泄露
	// 使用 cio.NewCreator 接管流

	// 使用 WithStreams 重定向 stdout/stderr
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, logFile, logFile)))
	if err != nil {
		logFile.Close() // 创建失败需手动关闭
		// 清理容器和快照
		_ = container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, fmt.Errorf("failed to create task: %w", err)
	}

	if err := task.Start(ctx); err != nil {
		// 清理 task 和容器
		_, _ = task.Delete(ctx, containerd.WithProcessKill)
		_ = container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, fmt.Errorf("failed to start task: %w", err)
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
		if err != nil {
			return nil, err
		}
	}
	return image, nil
}

func (r *ContainerdRuntime) prepareSpecOpts(config *SandboxConfig, image containerd.Image) []oci.SpecOpts {
	// 原始命令与参数
	originalArgs := append(config.Command, config.Args...)

	// --- 插件注入逻辑 (带路径验证) ---
	var mounts []specs.Mount
	finalArgs := originalArgs

	if r.infraMgr != nil {
		plugins := r.infraMgr.GetPlugins()
		for _, p := range plugins {
			hostPath := r.infraMgr.GetHostPath(p.BinName)
			if hostPath == "" {
				continue
			}

			// 验证插件路径是否在允许的白名单内
			if !r.isPluginPathAllowed(hostPath) {
				fmt.Printf("SECURITY: Plugin path %s is not in allowed paths, skipping\n", hostPath)
				continue
			}

			// 验证文件是否存在且可执行
			if _, err := os.Stat(hostPath); err != nil {
				fmt.Printf("Warning: Plugin binary %s not accessible: %v\n", hostPath, err)
				continue
			}

			// A. 添加挂载点
			mounts = append(mounts, specs.Mount{
				Source:      hostPath,
				Destination: p.ContainerPath,
				Type:        "bind",
				Options:     []string{"ro", "rbind", "nosuid", "nodev"}, // 只读绑定，添加安全选项
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

	// 设置工作目录
	if config.WorkingDir != "" {
		specOpts = append(specOpts, oci.WithProcessCwd(config.WorkingDir))
	}

	// 应用挂载点
	if len(mounts) > 0 {
		specOpts = append(specOpts, oci.WithMounts(mounts))
	}

	// 网络命名空间配置：共享 Agent Pod 的网络命名空间
	// 这是 Fast Sandbox 的默认行为，可以实现毫秒级启动
	// 对于 gVisor 运行时，它有自己的用户态网络栈，但仍然共享主机网络接口
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

// isPluginPathAllowed 检查插件路径是否在允许的白名单内
func (r *ContainerdRuntime) isPluginPathAllowed(pluginPath string) bool {
	// 清理路径，解析符号链接
	resolvedPath, err := filepath.EvalSymlinks(pluginPath)
	if err != nil {
		return false
	}

	for _, allowedPath := range r.allowedPluginPaths {
		// 清理允许的路径
		cleanAllowed := filepath.Clean(allowedPath)
		// 检查插件路径是否以允许的路径为前缀
		if strings.HasPrefix(resolvedPath, cleanAllowed+string(filepath.Separator)) || resolvedPath == cleanAllowed {
			return true
		}
	}
	return false
}

func (r *ContainerdRuntime) calculateSlotResources() (int64, int64, error) {
	capacityStr := os.Getenv("AGENT_CAPACITY")
	var capacity int64 = 5
	fmt.Sscanf(capacityStr, "%d", &capacity)
	if capacity <= 0 {
		capacity = 1
	}

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
	if totalCPU == 0 && totalMem == 0 {
		return 0, 0, fmt.Errorf("no limits")
	}
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
		"fast-sandbox.io/namespace":    r.agentNamespace,
		"fast-sandbox.io/id":           config.SandboxID,
		"fast-sandbox.io/claim-uid":    config.ClaimUID,
		"fast-sandbox.io/sandbox-name": config.ClaimName, // 规范化标签名
	}
}

// SetNamespace 设置 Agent 运行的命名空间
func (r *ContainerdRuntime) SetNamespace(ns string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agentNamespace = ns
}

func (r *ContainerdRuntime) DeleteSandbox(ctx context.Context, sandboxID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	container, err := r.client.LoadContainer(ctx, sandboxID)
	if err != nil {
		// 容器不存在，仍需尝试清理快照
		delete(r.sandboxes, sandboxID)
		// 直接删除快照
		snapshotName := sandboxID + "-snapshot"
		if err := r.client.SnapshotService("k8s.io").Remove(ctx, snapshotName); err != nil {
			// 快照不存在或已被删除，忽略错误
			fmt.Printf("Snapshot cleanup for %s: %v\n", snapshotName, err)
		}
		return nil
	}

	// 停止并删除任务（立即删除，用于非优雅关闭场景）
	task, err := container.Task(ctx, nil)
	if err == nil {
		// 直接发送 SIGKILL
		_ = task.Kill(ctx, syscall.SIGKILL)
		_, _ = task.Delete(ctx, containerd.WithProcessKill)
	}

	// 删除容器及其快照
	if err := container.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
		fmt.Printf("Container delete error for %s: %v\n", sandboxID, err)
	}
	delete(r.sandboxes, sandboxID)
	return nil
}

// GracefulDeleteSandbox 优雅删除 sandbox（SIGTERM → wait → SIGKILL）
// 返回是否成功执行优雅关闭（false 表示需要强制删除）
// 优化: 拆分为三阶段，减少持锁时间从 10-15s 到 <100ms
func (r *ContainerdRuntime) GracefulDeleteSandbox(ctx context.Context, sandboxID string, waitTimeout time.Duration) bool {
	ctx = namespaces.WithNamespace(ctx, "k8s.io")

	// ===== 阶段1: 获取任务引用 (短暂持锁) =====
	r.mu.Lock()
	container, err := r.client.LoadContainer(ctx, sandboxID)
	if err != nil {
		// 容器不存在，标记为已删除
		delete(r.sandboxes, sandboxID)
		r.mu.Unlock()
		return true
	}

	task, err := container.Task(ctx, nil)
	if err != nil {
		// 任务不存在，直接删除容器
		_ = container.Delete(ctx, containerd.WithSnapshotCleanup)
		delete(r.sandboxes, sandboxID)
		r.mu.Unlock()
		return true
	}
	r.mu.Unlock() // 释放锁，后续等待不再持锁

	// ===== 阶段2: 等待退出 (不持锁，可能 10s+) =====
	// 发送 SIGTERM
	if err := task.Kill(ctx, syscall.SIGTERM); err != nil {
		fmt.Printf("Failed to send SIGTERM to %s: %v, force delete\n", sandboxID, err)
		// 直接强制删除
		r.mu.Lock()
		_, _ = task.Delete(ctx, containerd.WithProcessKill)
		_ = container.Delete(ctx, containerd.WithSnapshotCleanup)
		delete(r.sandboxes, sandboxID)
		r.mu.Unlock()
		return false
	}

	// 等待进程退出
	waitCh, err := task.Wait(ctx)
	if err != nil {
		fmt.Printf("Failed to wait for %s: %v\n", sandboxID, err)
	}

	// 超时检查（不持锁等待）
	select {
	case <-waitCh:
		// 进程正常退出
	case <-time.After(waitTimeout):
		// 超时，发送 SIGKILL
		fmt.Printf("Sandbox %s did not exit after %v, sending SIGKILL\n", sandboxID, waitTimeout)
		_ = task.Kill(ctx, syscall.SIGKILL)
		<-waitCh // 等待 SIGKILL 生效
	}

	// ===== 阶段3: 清理资源 (短暂持锁) =====
	r.mu.Lock()
	defer r.mu.Unlock()

	_, _ = task.Delete(ctx, containerd.WithProcessKill)
	_ = container.Delete(ctx, containerd.WithSnapshotCleanup)
	delete(r.sandboxes, sandboxID)
	return true
}

func (r *ContainerdRuntime) GetSandbox(ctx context.Context, sandboxID string) (*SandboxMetadata, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sandboxes[sandboxID], nil
}

// GetSandboxStatus 获取 sandbox 的运行时状态
// 返回: Phase (running/stopped/terminated), error
func (r *ContainerdRuntime) GetSandboxStatus(ctx context.Context, sandboxID string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 先检查本地缓存
	if meta, ok := r.sandboxes[sandboxID]; ok {
		return meta.Status, nil
	}

	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	container, err := r.client.LoadContainer(ctx, sandboxID)
	if err != nil {
		// 容器不存在
		return "terminated", nil
	}

	task, err := container.Task(ctx, nil)
	if err != nil {
		// 任务不存在，容器已停止
		return "stopped", nil
	}

	status, err := task.Status(ctx)
	if err != nil {
		return "unknown", err
	}

	return string(status.Status), nil
}

func (r *ContainerdRuntime) ListSandboxes(ctx context.Context) ([]*SandboxMetadata, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.client == nil {
		return nil, nil
	}
	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	filter := fmt.Sprintf("labels.\"fast-sandbox.io/managed\"==\"true\",labels.\"fast-sandbox.io/agent-uid\"==\"%s\"", r.agentUID)
	containers, err := r.client.Containers(ctx, filter)
	if err != nil {
		return nil, err
	}
	var list []*SandboxMetadata
	for _, c := range containers {
		info, _ := c.Info(ctx)
		status := "unknown"
		if task, err := c.Task(ctx, nil); err == nil {
			if s, err := task.Status(ctx); err == nil {
				status = string(s.Status)
			}
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
	if err != nil {
		return nil, err
	}
	var names []string
	for _, img := range images {
		names = append(names, img.Name())
	}
	return names, nil
}

func (r *ContainerdRuntime) PullImage(ctx context.Context, image string) error {
	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	_, err := r.client.GetImage(ctx, image)
	if err == nil {
		return nil
	}
	_, err = r.client.Pull(ctx, image, containerd.WithPullUnpack)
	return err
}

func (r *ContainerdRuntime) Close() error {
	if r.client != nil {
		return r.client.Close()
	}
	return nil
}

// GetSandboxLogs 读取沙箱日志

func (r *ContainerdRuntime) GetSandboxLogs(ctx context.Context, sandboxID string, follow bool, stdout io.Writer) error {
	logPath := filepath.Join("/var/log/fast-sandbox", fmt.Sprintf("%s.log", sandboxID))
	file, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {

			return fmt.Errorf("log file not found")

		}
		return err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	// 读取现有内容
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			if _, wErr := stdout.Write([]byte(line)); wErr != nil {

				return wErr

			}
		}
		if err != nil {
			if err == io.EOF {

				break

			}
			return err
		}
	}
	if !follow {

		return nil

	}
	// Follow 模式：轮询新内容
	// 注意：更高效的做法是用 fsnotify，但轮询简单且依赖少
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			for {
				line, err := reader.ReadString('\n')
				if line != "" {
					if _, wErr := stdout.Write([]byte(line)); wErr != nil {

						return wErr

					}
				}
				if err == io.EOF {

					break

				}
				if err != nil {

					return err

				}
			}
			// 检查文件是否被截断或删除（可选，暂略）
		}
	}
}

func envMapToSlice(env map[string]string) []string {
	var res []string
	for k, v := range env {
		res = append(res, fmt.Sprintf("%s=%s", k, v))
	}
	return res
}
