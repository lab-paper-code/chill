package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ModelSpecSpec defines the desired state of ModelSpec
type ModelSpecSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Foo is an example field of ModelSpec. Edit modelspec_types.go to remove/update
	Foo string `json:"foo,omitempty"`
}

// ModelSpecStatus defines the observed state of ModelSpec
type ModelSpecStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// ModelSpec is the Schema for the modelspecs API
type ModelSpec struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelSpecSpec   `json:"spec,omitempty"`
	Status ModelSpecStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ModelSpecList contains a list of ModelSpec
type ModelSpecList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelSpec `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ModelSpec{}, &ModelSpecList{})
}
