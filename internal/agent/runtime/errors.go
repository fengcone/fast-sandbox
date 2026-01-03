package runtime

import "errors"

var (
	// ErrUnsupportedRuntime 不支持的运行时类型
	ErrUnsupportedRuntime = errors.New("unsupported container runtime")

	// ErrSandboxNotFound sandbox 不存在
	ErrSandboxNotFound = errors.New("sandbox not found")

	// ErrSandboxAlreadyExists sandbox 已存在
	ErrSandboxAlreadyExists = errors.New("sandbox already exists")

	// ErrRuntimeNotInitialized 运行时未初始化
	ErrRuntimeNotInitialized = errors.New("runtime not initialized")

	// ErrInvalidConfig 无效的配置
	ErrInvalidConfig = errors.New("invalid sandbox config")
)
