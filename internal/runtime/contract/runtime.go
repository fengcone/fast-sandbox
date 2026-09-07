// Package contract defines the runtime-neutral lifecycle boundary consumed by
// Fastlet. Runtime implementations deliberately exclude exec, file, and proxy
// protocols from this interface.
package contract

import (
	"context"
	"fmt"
	"time"

	apiv1alpha2 "fast-sandbox/api/v1alpha2"
	runtimecatalog "fast-sandbox/internal/catalog/runtime"
	dataplane "fast-sandbox/internal/dataplane/contract"
	infracontract "fast-sandbox/internal/infra/contract"
	fastletapi "fast-sandbox/internal/protocol/fastlet"
)

type Metadata struct {
	Config                 fastletapi.RuntimeSandboxConfig
	Allocation             fastletapi.RuntimeAllocation
	ContainerID            string
	PID                    int
	Phase                  string
	CreatedAt              int64
	UserProcessStartedAt   time.Time
	UserProcessStartSource fastletapi.UserProcessStartSource
	InfraServices          []infracontract.ServiceEndpoint
	InfraDiagnostics       []infracontract.ComponentDiagnostic
	AcceptedGeneration     int64
	AppliedGeneration      int64
	ActionBindingStatuses  []fastletapi.ActionBindingStatus
}

type Driver interface {
	Initialize(ctx context.Context, socketPath string) error
	SetNamespace(ns string)
	ProbeCapabilities(ctx context.Context) CapabilityReport
	EnsureSandbox(ctx context.Context, input *fastletapi.EnsureSandboxInput) (*Metadata, error)
	InspectSandbox(ctx context.Context, sandboxID string) (*Metadata, error)
	DeleteSandbox(ctx context.Context, sandboxID string) error
	ListManagedSandboxes(ctx context.Context) ([]*Metadata, error)
	Close() error
}

type ArtifactCache interface {
	ListImages(ctx context.Context) ([]string, error)
	PullImage(ctx context.Context, image string) error
}

// ImageDeliveryStatus reports the delivery state of an image that is not
// (yet) present in the local cache.
type ImageDeliveryStatus string

const (
	// ImageDelivering reports that the artifact delivery is in flight on the
	// node; the caller must poll again later instead of blocking.
	ImageDelivering ImageDeliveryStatus = "Delivering"
	// ImageDelivered reports that the image is committed in the local cache
	// and the Sandbox can boot from it.
	ImageDelivered ImageDeliveryStatus = "Delivered"
)

// ImageDelivery is the optional runtime extension for asynchronous artifact
// delivery. Runtimes whose first boot can require a cold transfer of the
// Sandbox image (Firecracker) implement it so the create path never blocks on
// the network: a missing image parks the Sandbox in a delivery phase while a
// node-side pull runs in the background.
//
// DeliverImage guarantees that an attempt to deliver image is in flight (it is
// idempotent and safe to call concurrently) and reports the current state
// without waiting for the transfer to finish. A non-nil error means delivery
// is impossible right now (e.g. no runtime-agent in local mode) and the caller
// must not park; delivery failures that happen asynchronously are reported by
// a later call.
type ImageDelivery interface {
	DeliverImage(ctx context.Context, image string) (ImageDeliveryStatus, error)
}

type ResourceRecoverer interface {
	RecoverRuntimeResources(ctx context.Context, managed []*Metadata) error
}

type ResourceAdmission interface {
	RuntimeResourceAvailable() bool
}

type AccessDescriptorProvider interface {
	GetAccessDescriptor(sandboxID string) (dataplane.AccessDescriptor, error)
}

type Config struct {
	Namespace   string
	Snapshotter string
	Handler     string
	RuntimePath string
	ConfigPath  string
	NeedsTTY    bool
	OptionsType string
}

type CapabilityReport struct {
	Runtime     apiv1alpha2.RuntimeName        `json:"runtime"`
	ProfileHash string                         `json:"profileHash"`
	State       runtimecatalog.CapabilityState `json:"state"`
	Reason      string                         `json:"reason,omitempty"`
	Message     string                         `json:"message,omitempty"`
	Missing     []string                       `json:"missing,omitempty"`
}

func (r CapabilityReport) Ready() bool {
	return r.State == runtimecatalog.CapabilityReady
}

type CapabilityProber interface {
	Probe(ctx context.Context, profile runtimecatalog.RuntimeProfile, socketPath string) CapabilityReport
}

func ValidateProfile(existing *Metadata, requested *fastletapi.RuntimeSandboxConfig) error {
	if existing == nil || requested == nil {
		return fmt.Errorf("%w: existing and requested runtime specs are required", ErrSandboxProfileMismatch)
	}
	if existing.Config.Spec.RuntimeProfileHash != requested.Spec.RuntimeProfileHash ||
		existing.Config.Spec.ResourceProfileHash != requested.Spec.ResourceProfileHash ||
		existing.Config.Spec.InfraRevision != requested.Spec.InfraRevision ||
		existing.Config.Spec.CPU != requested.Spec.CPU || existing.Config.Spec.Memory != requested.Spec.Memory || existing.Config.Spec.PIDs != requested.Spec.PIDs {
		return fmt.Errorf("%w: existing runtime identity %q has different runtime/resource profile", ErrSandboxProfileMismatch, requested.Identity.SandboxUID)
	}
	return nil
}

// SameRuntimeIdentity reports whether an observed runtime belongs to the exact
// Sandbox incarnation and placement represented by requested. Desired runtime
// configuration is intentionally excluded and validated separately.
func SameRuntimeIdentity(existing, requested fastletapi.SandboxIdentity) bool {
	return existing.SandboxUID == requested.SandboxUID &&
		existing.Namespace == requested.Namespace &&
		existing.Name == requested.Name &&
		existing.FastletPodUID == requested.FastletPodUID &&
		existing.InstanceGeneration == requested.InstanceGeneration &&
		existing.RuntimeInstanceID == requested.RuntimeInstanceID &&
		existing.AssignmentAttempt == requested.AssignmentAttempt &&
		existing.RouteGeneration == requested.RouteGeneration
}
