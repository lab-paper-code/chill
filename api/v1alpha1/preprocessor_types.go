package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PreprocessorSpec declares the raw sample contract and per-model-artifact preprocessing code.
type PreprocessorSpec struct {
	// Input declares every field of one raw sample.
	Input RawInputContract `json:"input"`

	// Output declares every field of one raw response.
	Output RawOutputContract `json:"output"`

	// MaxNewTokens bounds generation and is declared only by generative preprocessing.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxNewTokens *int32 `json:"maxNewTokens,omitempty"`

	// Artifacts binds one preprocessing code directory to each target model artifact.
	// +kubebuilder:validation:MinItems=1
	Artifacts []PreprocessorArtifact `json:"artifacts"`
}

// RawInputContract declares the fields of one raw sample. Every field is carried as BYTES.
type RawInputContract struct {
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Fields []RawField `json:"fields"`
}

// RawField declares one size-bounded raw field.
type RawField struct {
	// Name is one path element: dataset records store the field as <recordID>/<name>.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^([^/\\.][^/\\]*|\.[^/\\.][^/\\]*|\.\.[^/\\]+)$`
	Name string `json:"name"`

	// Format is the field content format.
	// +kubebuilder:validation:Enum=JPEG;UTF8
	Format string `json:"format"`

	// MaxBytes is the inclusive upper bound of the field size.
	// +kubebuilder:validation:Minimum=1
	MaxBytes int32 `json:"maxBytes"`
}

// RawOutputContract declares the fields of one raw response. Every field is carried as BYTES.
type RawOutputContract struct {
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Fields []RawOutputField `json:"fields"`
}

// RawOutputField declares one raw response field.
type RawOutputField struct {
	// Name is one path element: dataset records store the field as <recordID>/<name>.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^([^/\\.][^/\\]*|\.[^/\\.][^/\\]*|\.\.[^/\\]+)$`
	Name string `json:"name"`

	// Format is the field content format.
	// +kubebuilder:validation:Enum=JPEG;UTF8
	Format string `json:"format"`
}

// PreprocessorArtifact is the preprocessing code for one target model artifact.
type PreprocessorArtifact struct {
	Target PreprocessorTarget `json:"target"`

	Source PreprocessorArtifactSource `json:"source"`
}

// PreprocessorTarget names one ModelSpec artifact.
type PreprocessorTarget struct {
	// Model is the ModelSpec name.
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model"`

	// Artifact is a ModelArtifact name in that ModelSpec.
	// +kubebuilder:validation:MinLength=1
	Artifact string `json:"artifact"`
}

// PreprocessorArtifactSource identifies one exact code carrier and its in-container directory.
type PreprocessorArtifactSource struct {
	// Image is an immutable OCI image reference containing the code directory.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// Architecture is the Kubernetes architecture supported by the carrier.
	// +kubebuilder:validation:MinLength=1
	Architecture string `json:"architecture"`

	// Path is the clean absolute directory containing preprocessor.py.
	// +kubebuilder:validation:Pattern=`^/[A-Za-z0-9._/-]+$`
	Path string `json:"path"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster

// Preprocessor is the Schema for the preprocessors API.
type Preprocessor struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec PreprocessorSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// PreprocessorList contains a list of Preprocessor.
type PreprocessorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Preprocessor `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Preprocessor{}, &PreprocessorList{})
}
