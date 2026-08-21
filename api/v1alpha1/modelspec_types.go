package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ModelSpecSpec defines model artifacts and their execution-path requirements.
type ModelSpecSpec struct {
	// Artifacts contains executable representations of this logical model.
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

// ModelArtifact identifies one executable representation.
type ModelArtifact struct {
	// Name is unique within one ModelSpec and is referenced by execution paths.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Format names the artifact representation, such as onnx.
	// +kubebuilder:validation:MinLength=1
	Format string `json:"format"`

	// Source identifies the immutable OCI artifact carrier used to deliver this
	// representation to a measurement worker.
	Source ModelArtifactSource `json:"source"`

	// Input declares this artifact's input tensor contract without its batch axis.
	Input *ModelArtifactInputContract `json:"input"`

	// Derivation binds a derived executable to its source artifact and conversion settings.
	// It is absent for canonical source artifacts.
	// +optional
	Derivation *ModelArtifactDerivation `json:"derivation,omitempty"`
}

// ModelArtifactSource identifies one exact artifact carrier and its in-container path.
type ModelArtifactSource struct {
	// Image is an immutable OCI image reference containing the artifact.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// Architecture is the Kubernetes architecture supported by the carrier.
	// +kubebuilder:validation:MinLength=1
	Architecture string `json:"architecture"`

	// Path is the clean absolute path of the artifact in the carrier image.
	// +kubebuilder:validation:Pattern=`^/[A-Za-z0-9._/-]+$`
	Path string `json:"path"`
}

// ModelArtifactDerivation identifies how one derived artifact was produced.
type ModelArtifactDerivation struct {
	// SourceArtifact references the source ModelArtifact name in this ModelSpec.
	// Reference resolution is semantic validation, not OpenAPI structure.
	// +kubebuilder:validation:MinLength=1
	SourceArtifact string `json:"sourceArtifact"`

	// TargetPlatform is the converter target, such as rk3588.
	// +kubebuilder:validation:MinLength=1
	TargetPlatform string `json:"targetPlatform"`

	// DeclaredBatchSizes enumerates every batch size value this artifact can execute.
	// A runtime that consumes batch as a call argument declares no derivation
	// and therefore no declared batch sizes.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Minimum=1
	DeclaredBatchSizes []int32 `json:"declaredBatchSizes"`

	// Conversion binds the converter and recipe used to produce this artifact.
	Conversion ModelArtifactConversion `json:"conversion"`
}

type ModelArtifactInputContract struct {
	// InputSampleShape is the ordered input tensor dimensions without its batch
	// axis. The selected measurement batch supplies that axis.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Minimum=1
	InputSampleShape []int32 `json:"inputSampleShape"`
}

// ModelArtifactConversion identifies the executable converter and conversion recipe.
type ModelArtifactConversion struct {
	// Tool is the converter family, such as rknn-toolkit2.
	// +kubebuilder:validation:MinLength=1
	Tool string `json:"tool"`

	// Version is the converter's reported version.
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`
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
