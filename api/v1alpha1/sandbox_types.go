package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "sandbox.fast.io", Version: "v1alpha1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)

// SandboxSpec defines the desired state of Sandbox.
type SandboxSpec struct {
	Image      string          `json:"image"`
	Command    []string        `json:"command,omitempty"`
	Args       []string        `json:"args,omitempty"`
	Envs       []corev1.EnvVar `json:"envs,omitempty"`
	WorkingDir string          `json:"workingDir,omitempty"`

	// ExpireTime specifies when this sandbox should expire and be garbage collected.
	// If not set, the sandbox will not expire automatically.
	ExpireTime *metav1.Time `json:"expireTime,omitempty"`

	// ExposedPorts specifies the ports that the sandbox application will listen on.
	// The controller ensures no port conflicts on the same Agent Pod during scheduling.
	ExposedPorts []int32 `json:"exposedPorts,omitempty"`

	// +kubebuilder:validation:Required
	// PoolRef specifies which SandboxPool this sandbox should be scheduled to.
	// This field is required.
	PoolRef string `json:"poolRef"`
}

// SandboxStatus defines the observed state of Sandbox.
type SandboxStatus struct {
	Phase       string             `json:"phase,omitempty"`
	AssignedPod string             `json:"assignedPod,omitempty"`
	NodeName    string             `json:"nodeName,omitempty"`
	SandboxID   string             `json:"sandboxID,omitempty"`
	Endpoints   []string           `json:"endpoints,omitempty"`
	Conditions  []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Sandbox is the Schema for the sandboxes API.
type Sandbox struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SandboxSpec   `json:"spec,omitempty"`
	Status SandboxStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SandboxList contains a list of Sandbox.
type SandboxList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Sandbox `json:"items"`
}

func (in *Sandbox) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(Sandbox)
	*out = *in
	return out
}

func (in *SandboxList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(SandboxList)
	*out = *in
	return out
}

func init() {
	SchemeBuilder.Register(&Sandbox{}, &SandboxList{})
}