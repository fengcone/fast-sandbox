package runtime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fast-sandbox/internal/agent/infra"
	"fast-sandbox/internal/api"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
	ctrl "sigs.k8s.io/controller-runtime/pkg/log"
)

type ContainerdRuntime struct {
	socketPath         string
	client             *containerd.Client
	cgroupPath         string
	netnsPath          string
	agentID            string
	agentUID           string
	agentNamespace     string
	infraMgr           *infra.Manager
	allowedPluginPaths []string
	runtimeHandler     string
}

const (
	defaultOperationTimeout = 30 * time.Second
	waitStopTimeout         = 10 * time.Second
)

func newContainerdRuntime(runtimeHandler string) Runtime {
	return &ContainerdRuntime{
		runtimeHandler: runtimeHandler,
	}
}

// Initialize init containerd client
func (r *ContainerdRuntime) Initialize(ctx context.Context, socketPath string) error {
	log := ctrl.Log.WithName("runtime")
	r.socketPath = socketPath
	if r.socketPath == "" {
		r.socketPath = "/run/containerd/containerd.sock"
	}
	log.Info("Initializing runtime", "handler", r.runtimeHandler)

	ctx, cancel := context.WithTimeout(ctx, defaultOperationTimeout)
	defer cancel()

	client, err := containerd.New(r.socketPath, containerd.WithDefaultNamespace("k8s.io"))
	if err != nil {
		return fmt.Errorf("failed to create containerd client: %w", err)
	}

	r.client = client
	r.agentID = os.Getenv("POD_NAME")
	r.agentUID = os.Getenv("POD_UID")

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

	infraPodPath := os.Getenv("INFRA_DIR_IN_POD")
	if infraPodPath == "" {
		infraPodPath = "/opt/fast-sandbox/infra"
	}
	r.infraMgr = infra.NewManager(infraPodPath)

	if err := r.discoverCgroupPath(); err != nil {
		log.Error(err, "Failed to discover cgroup path")
		r.cgroupPath = ""
	}

	if err := r.discoverNetNSPath(ctx); err != nil {
		log.Error(err, "Failed to discover network namespace")
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

func (r *ContainerdRuntime) CreateSandbox(ctx context.Context, config *api.SandboxSpec) (*SandboxMetadata, error) {
	log := ctrl.Log.WithName("runtime").WithValues("sandbox", config.SandboxID)
	log.Info("Creating sandbox", "image", config.Image, "runtime", r.runtimeHandler, "netns", r.netnsPath)
	ctx, cancel := context.WithTimeout(ctx, defaultOperationTimeout)
	defer cancel()
	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	image, err := r.prepareImage(ctx, config.Image)
	if err != nil {
		log.Error(err, "Failed to prepare image")
		return nil, err
	}

	containerID := config.SandboxID
	specOpts := r.prepareSpecOpts(config, image)
	labels := r.prepareLabels(config)

	log.Info("Creating containerd container object")
	container, err := r.client.NewContainer(
		ctx,
		containerID,
		containerd.WithImage(image),
		containerd.WithNewSnapshot(snapShotName(containerID), image),
		containerd.WithRuntime(r.runtimeHandler, nil), // 使用配置的 Runtime
		containerd.WithNewSpec(specOpts...),
		containerd.WithContainerLabels(labels),
	)
	if err != nil {
		log.Error(err, "Failed to create container object")
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	logDir := "/var/log/fast-sandbox"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log dir: %w", err)
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("%s.log", containerID))

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	log.Info("Creating containerd task")
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, logFile, logFile)))
	if err != nil {
		log.Error(err, "Failed to create containerd task", "logPath", logPath)
		logFile.Close()
		_ = container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, fmt.Errorf("failed to create task: %w", err)
	}

	log.Info("Starting containerd task", "pid", task.Pid())
	if err = task.Start(ctx); err != nil {
		log.Error(err, "Failed to start containerd task")
		_, _ = task.Delete(ctx, containerd.WithProcessKill)
		_ = container.Delete(ctx, containerd.WithSnapshotCleanup)
		return nil, fmt.Errorf("failed to start task: %w", err)
	}
	metadata := &SandboxMetadata{
		SandboxSpec: *config,
		ContainerID: containerID,
		Phase:       "running",
		CreatedAt:   time.Now().Unix(),
		PID:         int(task.Pid()),
	}
	log.Info("Sandbox created successfully", "pid", task.Pid())
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

func (r *ContainerdRuntime) prepareSpecOpts(config *api.SandboxSpec, image containerd.Image) []oci.SpecOpts {
	originalArgs := append(config.Command, config.Args...)

	var mounts []specs.Mount
	finalArgs := originalArgs

	if r.infraMgr != nil {
		plugins := r.infraMgr.GetPlugins()
		for _, p := range plugins {
			hostPath := r.infraMgr.GetHostPath(p.BinName)
			if hostPath == "" {
				continue
			}

			if !r.isPluginPathAllowed(hostPath) {
				fmt.Printf("SECURITY: Plugin path %s is not in allowed paths, skipping\n", hostPath)
				continue
			}

			if _, err := os.Stat(hostPath); err != nil {
				fmt.Printf("Warning: Plugin binary %s not accessible: %v\n", hostPath, err)
				continue
			}

			mounts = append(mounts, specs.Mount{
				Source:      hostPath,
				Destination: p.ContainerPath,
				Type:        "bind",
				Options:     []string{"ro", "rbind", "nosuid", "nodev"}, // 只读绑定，添加安全选项
			})

			if p.IsWrapper {
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

	if config.WorkingDir != "" {
		specOpts = append(specOpts, oci.WithProcessCwd(config.WorkingDir))
	}

	if len(mounts) > 0 {
		specOpts = append(specOpts, oci.WithMounts(mounts))
	}

	if r.netnsPath != "" {
		specOpts = append(specOpts, oci.WithLinuxNamespace(specs.LinuxNamespace{
			Type: specs.NetworkNamespace,
			Path: r.netnsPath,
		}))
	}

	return specOpts
}

func (r *ContainerdRuntime) isPluginPathAllowed(pluginPath string) bool {
	resolvedPath, err := filepath.EvalSymlinks(pluginPath)
	if err != nil {
		return false
	}

	for _, allowedPath := range r.allowedPluginPaths {
		cleanAllowed := filepath.Clean(allowedPath)
		if strings.HasPrefix(resolvedPath, cleanAllowed+string(filepath.Separator)) || resolvedPath == cleanAllowed {
			return true
		}
	}
	return false
}

func (r *ContainerdRuntime) prepareLabels(config *api.SandboxSpec) map[string]string {
	return map[string]string{
		"fast-sandbox.io/managed":      "true",
		"fast-sandbox.io/agent-name":   r.agentID,
		"fast-sandbox.io/agent-uid":    r.agentUID,
		"fast-sandbox.io/namespace":    r.agentNamespace,
		"fast-sandbox.io/id":           config.SandboxID,
		"fast-sandbox.io/claim-uid":    config.ClaimUID,
		"fast-sandbox.io/sandbox-name": config.ClaimName,
	}
}

func (r *ContainerdRuntime) SetNamespace(ns string) {
	r.agentNamespace = ns
}

func (r *ContainerdRuntime) DeleteSandbox(ctx context.Context, sandboxID string) error {
	log := ctrl.Log.WithName("runtime").WithValues("sandbox", sandboxID)
	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	container, err := r.client.LoadContainer(ctx, sandboxID)
	if err != nil {
		log.Error(err, "Failed to load container")
		snapErr := r.client.SnapshotService("k8s.io").Remove(ctx, snapShotName(sandboxID))
		return JoinErrors(err, snapErr)
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		delErr := container.Delete(ctx, containerd.WithSnapshotCleanup)
		return JoinErrors(err, delErr)
	}

	if taskKillErr := task.Kill(ctx, syscall.SIGTERM); taskKillErr != nil {
		exitS, taskDelErr := task.Delete(ctx, containerd.WithProcessKill)
		containerDelErr := container.Delete(ctx, containerd.WithSnapshotCleanup)
		log.Info("Failed to kill task, force delete", "taskKillErr", taskKillErr, "taskDelErr", taskDelErr, "containerDelErr", containerDelErr, "exitStatus", exitS)
		return JoinErrors(taskKillErr, taskDelErr, containerDelErr)
	}

	waitCh, err := task.Wait(ctx)
	if err != nil {
		log.Info("Failed to wait for task", "err", err)
	}
	var taskKillErr error
	select {
	case <-waitCh:
	case <-time.After(waitStopTimeout):
		fmt.Printf("Sandbox %s did not exit after %v, sending SIGKILL\n", sandboxID, waitStopTimeout)
		taskKillErr = task.Kill(ctx, syscall.SIGKILL)
		<-waitCh
	}
	exitS, taskDelErr := task.Delete(ctx, containerd.WithProcessKill)
	containerDelErr := container.Delete(ctx, containerd.WithSnapshotCleanup)
	log.Info("kill task", "taskKillErr", taskKillErr, "taskDelErr", taskDelErr, "containerDelErr", containerDelErr, "exitStatus", exitS)
	return JoinErrors(taskDelErr, containerDelErr)
}

func (r *ContainerdRuntime) GetSandboxStatus(ctx context.Context, sandboxID string) (string, error) {
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

func snapShotName(containerID string) string {
	return containerID + "-snapshot"
}
