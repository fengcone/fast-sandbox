package runtime

import (
	"context"
	"io"

	"fast-sandbox/internal/api"
)

type SandboxMetadata struct {
	api.SandboxSpec
	ContainerID string
	PID         int
	Phase       string
	CreatedAt   int64
}

type Runtime interface {
	Initialize(ctx context.Context, socketPath string) error

	SetNamespace(ns string)

	CreateSandbox(ctx context.Context, config *api.SandboxSpec) (*SandboxMetadata, error)

	DeleteSandbox(ctx context.Context, sandboxID string) error

	GetSandboxLogs(ctx context.Context, sandboxID string, follow bool, stdout io.Writer) error

	ListImages(ctx context.Context) ([]string, error)

	PullImage(ctx context.Context, image string) error

	GetSandboxStatus(ctx context.Context, sandboxID string) (string, error)

	Close() error
}

type RuntimeType string

const (
	RuntimeTypeContainerd RuntimeType = "container"

	RuntimeTypeGVisor RuntimeType = "gvisor"
)

func NewRuntime(ctx context.Context, runtimeType RuntimeType, socketPath string) (Runtime, error) {
	var rt Runtime
	switch runtimeType {
	case RuntimeTypeContainerd:
		rt = newContainerdRuntime("io.containerd.runc.v2")
	case RuntimeTypeGVisor:
		rt = newContainerdRuntime("io.containerd.runsc.v1")
	default:
		return nil, ErrUnsupportedRuntime
	}

	if err := rt.Initialize(ctx, socketPath); err != nil {
		return nil, err
	}
	return rt, nil
}
