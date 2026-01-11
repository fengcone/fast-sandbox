package runtime

import (
	"context"
)

// SandboxMetadata 包含 sandbox 容器的元数据
type SandboxMetadata struct {
	SandboxID   string            // sandbox 的唯一标识符
	ClaimUID    string            // 关联的 Sandbox UID
	ClaimName   string            // 关联的 Sandbox 名称
	ContainerID string            // 底层容器运行时的容器 ID
	Image       string            // 容器镜像
	Command     []string          // 启动命令
	Args        []string          // 启动参数
	Env         map[string]string // 环境变量
	Port        int32             // 监听端口
	PID         int               // 容器主进程 PID
	Status      string            // 容器状态: "running", "stopped", "failed"
	CreatedAt   int64             // 创建时间戳
}

// SandboxConfig 定义创建 sandbox 的配置
type SandboxConfig struct {
	SandboxID string            // sandbox 的唯一标识符
	ClaimUID  string            // 关联的 Sandbox UID
	ClaimName string            // 关联的 Sandbox 名称
	Image     string            // 容器镜像
	Command   []string          // 启动命令（可选）
	Args      []string          // 启动参数（可选）
	Env       map[string]string // 环境变量（可选）
	CPU       string            // CPU 配额，如 "500m"
	Memory    string            // 内存配额，如 "1Gi"
	Port      int32             // 期望的监听端口，0 表示自动分配
}

// Runtime 定义容器运行时的抽象接口
// 不同的容器运行时（containerd、Docker、CRI-O 等）实现此接口
type Runtime interface {
	// Initialize 初始化运行时客户端
	// socketPath: 容器运行时的 socket 路径
	Initialize(ctx context.Context, socketPath string) error

	// SetNamespace 设置 Agent 运行的命名空间
	// 用于在容器标签中标记命名空间信息
	SetNamespace(ns string)

	// CreateSandbox 创建并启动一个 sandbox 容器
	// 返回创建的 sandbox 元数据
	CreateSandbox(ctx context.Context, config *SandboxConfig) (*SandboxMetadata, error)

	// DeleteSandbox 停止并删除一个 sandbox 容器
	DeleteSandbox(ctx context.Context, sandboxID string) error

	// GetSandbox 获取指定 sandbox 的元数据
	// 如果不存在返回 nil, nil
	GetSandbox(ctx context.Context, sandboxID string) (*SandboxMetadata, error)

	// ListSandboxes 列出所有当前运行的 sandbox
	ListSandboxes(ctx context.Context) ([]*SandboxMetadata, error)

	// ListImages 列出节点上可用的镜像列表
	ListImages(ctx context.Context) ([]string, error)

	// PullImage 拉取指定的容器镜像
	// 如果镜像已存在则跳过
	PullImage(ctx context.Context, image string) error
	// Close 关闭运行时客户端连接
	Close() error
}

// RuntimeType 定义运行时类型
type RuntimeType string

const (
	// RuntimeTypeContainerd containerd 运行时 (普通容器)
	RuntimeTypeContainerd RuntimeType = "container"

	// RuntimeTypeFirecracker Firecracker VM 运行时 (MicroVM)
	RuntimeTypeFirecracker RuntimeType = "firecracker"

	// RuntimeTypeGVisor gVisor 运行时 (安全容器)
	RuntimeTypeGVisor RuntimeType = "gvisor"
)

// NewRuntime 根据类型创建运行时实例
// runtimeType: 运行时类型（container, firecracker, gvisor）
// socketPath: 运行时 socket 路径
func NewRuntime(ctx context.Context, runtimeType RuntimeType, socketPath string) (Runtime, error) {
	var rt Runtime

	switch runtimeType {
	case RuntimeTypeContainerd:
		rt = &ContainerdRuntime{}
	case RuntimeTypeFirecracker:
		rt = &FirecrackerRuntime{}
	case RuntimeTypeGVisor:
		rt = &GVisorRuntime{}
	default:
		return nil, ErrUnsupportedRuntime
	}

	// 初始化运行时
	if err := rt.Initialize(ctx, socketPath); err != nil {
		return nil, err
	}

	return rt, nil
}
