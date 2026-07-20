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
	// +listType=map
	// +listMapKey=name
	PowerModes []PowerMode `json:"powerModes"`
}

// PowerMode binds a stable scheduler-facing name to one typed actuator target.
type PowerMode struct {
	// Name is the stable mode identity consumed by CHILL scheduling and profiling.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Actuator declares how a node agent applies and verifies this mode.
	Actuator PowerModeActuator `json:"actuator"`
}

// PowerModeActuatorKind identifies a supported typed power-mode actuator.
// +kubebuilder:validation:Enum=Fixed;JetsonNVPModel;NvidiaLockedGPUClock
type PowerModeActuatorKind string

const (
	PowerModeActuatorFixed                PowerModeActuatorKind = "Fixed"
	PowerModeActuatorJetsonNVPModel       PowerModeActuatorKind = "JetsonNVPModel"
	PowerModeActuatorNvidiaLockedGPUClock PowerModeActuatorKind = "NvidiaLockedGPUClock"
)

// PowerModeActuator is a discriminated union. Exactly the target matching Kind
// is present; Fixed has no target parameters.
// +kubebuilder:validation:XValidation:rule="self.kind == 'Fixed' ? !has(self.jetsonNVPModel) && !has(self.nvidiaLockedGPUClock) : true",message="Fixed actuator must not declare a target"
// +kubebuilder:validation:XValidation:rule="self.kind == 'JetsonNVPModel' ? has(self.jetsonNVPModel) && !has(self.nvidiaLockedGPUClock) : !has(self.jetsonNVPModel)",message="JetsonNVPModel actuator requires only jetsonNVPModel"
// +kubebuilder:validation:XValidation:rule="self.kind == 'NvidiaLockedGPUClock' ? has(self.nvidiaLockedGPUClock) && !has(self.jetsonNVPModel) : !has(self.nvidiaLockedGPUClock)",message="NvidiaLockedGPUClock actuator requires only nvidiaLockedGPUClock"
type PowerModeActuator struct {
	// Kind selects the typed actuator contract.
	Kind PowerModeActuatorKind `json:"kind"`

	// JetsonNVPModel is the target for the Jetson nvpmodel actuator.
	// +optional
	JetsonNVPModel *JetsonNVPModelTarget `json:"jetsonNVPModel,omitempty"`

	// NvidiaLockedGPUClock is the target for the NVIDIA locked GPU clock actuator.
	// +optional
	NvidiaLockedGPUClock *NvidiaLockedGPUClockTarget `json:"nvidiaLockedGPUClock,omitempty"`
}

// JetsonNVPModelTarget selects an nvpmodel configuration by numeric ID.
type JetsonNVPModelTarget struct {
	// ID is passed to the platform nvpmodel interface.
	// +kubebuilder:validation:Minimum=0
	ID int32 `json:"id"`
}

// NvidiaLockedGPUClockTarget selects an NVIDIA GPU clock range in MHz.
// +kubebuilder:validation:XValidation:rule="self.minMHz <= self.maxMHz",message="minMHz must not exceed maxMHz"
type NvidiaLockedGPUClockTarget struct {
	// MinMHz is the minimum requested locked GPU clock.
	// +kubebuilder:validation:Minimum=1
	MinMHz int32 `json:"minMHz"`

	// MaxMHz is the maximum requested locked GPU clock.
	// +kubebuilder:validation:Minimum=1
	MaxMHz int32 `json:"maxMHz"`

	// Mode selects the NVIDIA clock-locking algorithm when declared.
	// +optional
	// +kubebuilder:validation:Enum=0;1
	Mode *int32 `json:"mode,omitempty"`
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
