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

	// Derivation binds a derived executable to its source artifact and conversion identity.
	// It is absent for canonical source artifacts.
	// +optional
	Derivation *ModelArtifactDerivation `json:"derivation,omitempty"`
}

// ModelArtifactDerivation identifies how one immutable derived artifact was produced.
// The derived artifact's own Digest remains the identity of its output bytes.
type ModelArtifactDerivation struct {
	// SourceArtifact references the source ModelArtifact name in this ModelSpec.
	// Reference resolution is semantic validation, not OpenAPI structure.
	// +kubebuilder:validation:MinLength=1
	SourceArtifact string `json:"sourceArtifact"`

	// TargetPlatform is the converter target, such as rk3588.
	// +kubebuilder:validation:MinLength=1
	TargetPlatform string `json:"targetPlatform"`

	// FixedBatch is the only batch size encoded by this artifact.
	// +kubebuilder:validation:Minimum=1
	FixedBatch int32 `json:"fixedBatch"`

	// Conversion binds the converter and recipe used to produce this artifact.
	Conversion ModelArtifactConversion `json:"conversion"`
}

// ModelArtifactConversion identifies the executable converter and conversion recipe.
type ModelArtifactConversion struct {
	// Tool is the converter family, such as rknn-toolkit2.
	// +kubebuilder:validation:MinLength=1
	Tool string `json:"tool"`

	// Version is the converter's reported version.
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// ToolDigest identifies the exact converter distribution bytes.
	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	ToolDigest string `json:"toolDigest"`

	// RecipeDigest identifies the exact conversion recipe bytes.
	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	RecipeDigest string `json:"recipeDigest"`
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
