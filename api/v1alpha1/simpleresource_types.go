package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SimpleResourceSpec defines the desired state of SimpleResource
type SimpleResourceSpec struct {
	// Length of the random string to generate
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	Length int `json:"length"`

	// Prefix for the generated output file
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// Vars contains additional Terraform variables as key-value pairs
	// +optional
	Vars map[string]string `json:"vars,omitempty"`
}

// SimpleResourceStatus defines the observed state of SimpleResource
type SimpleResourceStatus struct {
	// Phase represents the current phase of the resource
	// Possible values: Pending, Applying, Ready, Error, Destroying
	// +optional
	Phase string `json:"phase,omitempty"`

	// Message provides additional information about the current phase
	// +optional
	Message string `json:"message,omitempty"`

	// Outputs contains the outputs from the Terraform apply
	// +optional
	Outputs map[string]string `json:"outputs,omitempty"`

	// LastAppliedHash tracks the hash of the last applied spec
	// +optional
	LastAppliedHash string `json:"lastAppliedHash,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=simple
// +kubebuilder:printcolumn:name="Length",type=integer,JSONPath=`.spec.length`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="RandomValue",type=string,JSONPath=`.status.randomValue`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SimpleResource is the Schema for the simpleresources API
type SimpleResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SimpleResourceSpec   `json:"spec,omitempty"`
	Status SimpleResourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SimpleResourceList contains a list of SimpleResource
type SimpleResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SimpleResource `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SimpleResource{}, &SimpleResourceList{})
}
