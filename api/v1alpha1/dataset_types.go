package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DatasetSpec declares a record carrier and its record fields.
// Records live in <path>/profiling/<recordID>/<field> and <path>/evaluation/<recordID>/<field>.
type DatasetSpec struct {
	Source DatasetSource `json:"source"`

	// Fields declares every record field.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Fields []RawField `json:"fields"`
}

// DatasetSource identifies one exact multi-architecture record carrier and its in-container directory.
type DatasetSource struct {
	// Image is an immutable multi-architecture OCI image index reference.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// Path is the clean absolute directory containing the profiling and evaluation subsets.
	// +kubebuilder:validation:Pattern=`^/[A-Za-z0-9._/-]+$`
	Path string `json:"path"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster

// Dataset is the Schema for the datasets API.
type Dataset struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec DatasetSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// DatasetList contains a list of Dataset.
type DatasetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Dataset `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Dataset{}, &DatasetList{})
}
