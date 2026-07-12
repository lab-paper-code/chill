package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ModelSpecSpec defines the stable model artifacts and their execution-path
// requirements.
type ModelSpecSpec struct {
	// Artifacts contains immutable executable representations of this logical model.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Artifacts []ModelArtifact `json:"artifacts"`

	// ExecutionPaths binds artifacts to required runtime families and backends.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	ExecutionPaths []ModelExecutionPath `json:"executionPaths"`
}

// ModelArtifact identifies one immutable executable representation.
type ModelArtifact struct {
	// Name is unique within one ModelSpec and is referenced by execution paths.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Format names the artifact representation, such as onnx.
	// +kubebuilder:validation:MinLength=1
	Format string `json:"format"`

	// Digest identifies the exact artifact bytes. The first schema accepts SHA-256.
	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	Digest string `json:"digest"`
}

// ModelExecutionPath describes one stable artifact/runtime requirement.
type ModelExecutionPath struct {
	// Name is unique within one ModelSpec.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Artifact references a ModelArtifact name in this ModelSpec.
	// Reference resolution is semantic validation, not OpenAPI structure.
	// +kubebuilder:validation:MinLength=1
	Artifact string `json:"artifact"`

	// Runtime contains the software path required to consume the artifact.
	Runtime ModelRuntimeRequirement `json:"runtime"`
}

// ModelRuntimeRequirement identifies the required runtime family and backend.
// It does not claim that any image or Node currently supplies them.
type ModelRuntimeRequirement struct {
	// Family names the required runtime family, such as onnxruntime.
	// +kubebuilder:validation:MinLength=1
	Family string `json:"family"`

	// Backend names the required backend within the runtime-family context.
	// +kubebuilder:validation:MinLength=1
	Backend string `json:"backend"`
}

// ModelSpecStatus defines the observed state of ModelSpec
type ModelSpecStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// ModelSpec is the Schema for the modelspecs API
type ModelSpec struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelSpecSpec   `json:"spec"`
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
