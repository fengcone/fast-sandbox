package v1alpha1

import (
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

// SandboxClaimSpec defines the desired state of SandboxClaim.
type SandboxClaimSpec struct {
	Image      string            `json:"image"`
	CPU        string            `json:"cpu,omitempty"`
	Memory     string            `json:"memory,omitempty"`
	TTLSeconds *int32            `json:"ttlSeconds,omitempty"`
	Command    []string          `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Port       int32             `json:"port,omitempty"`
}

// SandboxClaimStatus defines the observed state of SandboxClaim.
type SandboxClaimStatus struct {
	Phase            string             `json:"phase,omitempty"`
	AssignedAgentPod string             `json:"assignedAgentPod,omitempty"`
	NodeName         string             `json:"nodeName,omitempty"`
	SandboxID        string             `json:"sandboxID,omitempty"`
	Address          string             `json:"address,omitempty"`
	Conditions       []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// SandboxClaim is the Schema for the sandboxclaims API.
type SandboxClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SandboxClaimSpec   `json:"spec,omitempty"`
	Status SandboxClaimStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SandboxClaimList contains a list of SandboxClaim.
type SandboxClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SandboxClaim `json:"items"`
}

func (in *SandboxClaim) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(SandboxClaim)
	*out = *in
	return out
}

func (in *SandboxClaimList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(SandboxClaimList)
	*out = *in
	return out
}

func init() {
	SchemeBuilder.Register(&SandboxClaim{}, &SandboxClaimList{})
}
