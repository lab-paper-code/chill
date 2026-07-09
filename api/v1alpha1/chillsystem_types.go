package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ChillSystemPhase summarizes the observed CHILL installation state.
type ChillSystemPhase string

const (
	// ChillSystemPhaseReady means all enabled CHILL components are ready.
	ChillSystemPhaseReady ChillSystemPhase = "Ready"
	// ChillSystemPhaseProgressing means CHILL is reconciling toward readiness.
	ChillSystemPhaseProgressing ChillSystemPhase = "Progressing"
	// ChillSystemPhaseDegraded means at least one required CHILL component is unhealthy.
	ChillSystemPhaseDegraded ChillSystemPhase = "Degraded"
)

const (
	// ChillSystemConditionReady reports whether the CHILL installation is usable.
	ChillSystemConditionReady = "Ready"
	// ChillSystemConditionProgressing reports whether CHILL is still reconciling.
	ChillSystemConditionProgressing = "Progressing"
	// ChillSystemConditionDegraded reports whether CHILL needs operator attention.
	ChillSystemConditionDegraded = "Degraded"
)

const (
	// ChillSystemReasonMaxLength is the status reason length allowed by the CRD schema.
	ChillSystemReasonMaxLength = 128
	// ChillSystemMessageMaxLength is the status message length allowed by the CRD schema.
	ChillSystemMessageMaxLength = 1024
)

// ComponentState summarizes one CHILL component.
type ComponentState string

const (
	// ComponentStateReady means the component is enabled and ready.
	ComponentStateReady ComponentState = "Ready"
	// ComponentStateProgressing means the component is enabled but not fully ready yet.
	ComponentStateProgressing ComponentState = "Progressing"
	// ComponentStateDisabled means the component is intentionally disabled.
	ComponentStateDisabled ComponentState = "Disabled"
	// ComponentStateDegraded means the component is expected but unhealthy or missing.
	ComponentStateDegraded ComponentState = "Degraded"
	// ComponentStateUnknown means the component state could not be observed.
	ComponentStateUnknown ComponentState = "Unknown"
)

// ChillSystemSpec defines the desired state of the CHILL management surface.
type ChillSystemSpec struct{}

// ChillSystemStatus defines the observed state of CHILL in one management namespace.
type ChillSystemStatus struct {
	// ObservedGeneration is the latest spec generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase summarizes the current CHILL installation state.
	// +optional
	// +kubebuilder:validation:Enum=Ready;Progressing;Degraded
	Phase ChillSystemPhase `json:"phase,omitempty"`

	// Ready mirrors the Ready condition for kubectl printer columns.
	// +optional
	// +kubebuilder:validation:Enum=True;False;Unknown
	Ready metav1.ConditionStatus `json:"ready,omitempty"`

	// Message is a concise human-readable status summary.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`

	// ControllerState summarizes the controller manager Deployment state.
	// +optional
	// +kubebuilder:validation:Enum=Ready;Progressing;Disabled;Degraded;Unknown
	ControllerState ComponentState `json:"controllerState,omitempty"`

	// NodeDiscoveryState summarizes the node-discovery DaemonSet state.
	// +optional
	// +kubebuilder:validation:Enum=Ready;Progressing;Disabled;Degraded;Unknown
	NodeDiscoveryState ComponentState `json:"nodeDiscoveryState,omitempty"`

	// DeviceClassCount is the number of DeviceClass objects observed by CHILL.
	// +optional
	// +kubebuilder:validation:Minimum=0
	DeviceClassCount *int32 `json:"deviceClassCount,omitempty"`

	// ObservedNodeCount is the number of Kubernetes Nodes observed by CHILL.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ObservedNodeCount *int32 `json:"observedNodeCount,omitempty"`

	// Components reports detailed per-component status.
	// +optional
	// +listType=map
	// +listMapKey=name
	Components []ChillComponentStatus `json:"components,omitempty"`

	// Conditions follow the Kubernetes status condition convention.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ChillComponentStatus reports one CHILL runtime or control-plane component.
type ChillComponentStatus struct {
	// Name is the stable component identifier.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Kind is the Kubernetes workload kind backing this component.
	// +optional
	Kind string `json:"kind,omitempty"`

	// Namespace is the namespace containing this component.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// State summarizes the observed component state.
	// +kubebuilder:validation:Enum=Ready;Progressing;Disabled;Degraded;Unknown
	State ComponentState `json:"state"`

	// Reason is a short machine-readable reason for State.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	Reason string `json:"reason,omitempty"`

	// Message is a concise human-readable explanation for State.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`

	// Desired is the desired number of component replicas or scheduled pods.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Desired int32 `json:"desired,omitempty"`

	// Ready is the number of ready component replicas or scheduled pods.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Ready int32 `json:"ready,omitempty"`

	// Available is the number of available component replicas.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Available int32 `json:"available,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=csys
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Controller",type=string,JSONPath=`.status.controllerState`,priority=1
// +kubebuilder:printcolumn:name="NodeDiscovery",type=string,JSONPath=`.status.nodeDiscoveryState`,priority=1
// +kubebuilder:printcolumn:name="Classes",type=integer,JSONPath=`.status.deviceClassCount`,priority=1
// +kubebuilder:printcolumn:name="Nodes",type=integer,JSONPath=`.status.observedNodeCount`,priority=1
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`

// ChillSystem is the namespace-local status surface for a CHILL installation.
type ChillSystem struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ChillSystemSpec   `json:"spec,omitempty"`
	Status ChillSystemStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ChillSystemList contains a list of ChillSystem.
type ChillSystemList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ChillSystem `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ChillSystem{}, &ChillSystemList{})
}
