package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeviceClassSpec defines the desired state of DeviceClass
type DeviceClassSpec struct {
	// NodeSelector selects nodes that belong to this device class.
	// Discovery starts as explicit label matching; automatic node feature
	// discovery is intentionally outside this resource's first schema.
	// +kubebuilder:validation:MinProperties=1
	NodeSelector map[string]string `json:"nodeSelector"`

	// Architecture is the CPU architecture exposed by Kubernetes for this class.
	// +kubebuilder:validation:MinLength=1
	Architecture string `json:"architecture"`

	// MemoryBytes is the total unified or system memory available on this class.
	MemoryBytes resource.Quantity `json:"memory"`

	// Accelerator names the accelerator family exposed by this class, or "none".
	// +optional
	Accelerator string `json:"accelerator,omitempty"`

	// PowerModes lists the power modes supported by this class.
	// +kubebuilder:validation:MinItems=1
	PowerModes []PowerMode `json:"powerModes"`
}

// PowerMode describes one runtime-selectable power mode for a DeviceClass.
type PowerMode struct {
	// Name is the human-readable platform power mode name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// NvpID is the nvpmodel -m identifier for Jetson devices.
	// +optional
	// +kubebuilder:validation:Minimum=0
	NvpID *int `json:"nvpId,omitempty"`
}

// DeviceClassStatus defines the observed state of DeviceClass
type DeviceClassStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// DeviceClass is the Schema for the deviceclasses API
type DeviceClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeviceClassSpec   `json:"spec"`
	Status DeviceClassStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DeviceClassList contains a list of DeviceClass
type DeviceClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DeviceClass{}, &DeviceClassList{})
}
