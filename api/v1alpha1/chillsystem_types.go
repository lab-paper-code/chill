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

// ChillSystemSpec defines one CHILL system instance managed by the operator.
type ChillSystemSpec struct {
	// ManagementNamespace contains the namespaced CHILL support resources for this system.
	// When omitted, the operator namespace is used.
	// +optional
	ManagementNamespace string `json:"managementNamespace,omitempty"`

	// NodeDiscovery configures the operator-managed node-discovery DaemonSet.
	// +optional
	NodeDiscovery ChillNodeDiscoverySpec `json:"nodeDiscovery,omitempty"`

	// DeviceDiscovery configures node-based DeviceClass discovery.
	// +optional
	DeviceDiscovery ChillDeviceDiscoverySpec `json:"deviceDiscovery,omitempty"`
}

// ChillNodeDiscoverySpec configures the node-discovery DaemonSet owned by a ChillSystem.
type ChillNodeDiscoverySpec struct {
	// Enabled controls whether the operator should run node-discovery.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// DaemonSetName overrides the generated node-discovery DaemonSet name.
	// +optional
	DaemonSetName string `json:"daemonSetName,omitempty"`

	// ConfigMapName overrides the generated node-discovery config ConfigMap name.
	// +optional
	ConfigMapName string `json:"configMapName,omitempty"`

	// ConfigMapKey is the data key containing node-discovery config.
	// +optional
	ConfigMapKey string `json:"configMapKey,omitempty"`
}

// ChillDeviceDiscoverySpec configures DeviceClass discovery from Kubernetes Nodes.
type ChillDeviceDiscoverySpec struct {
	// Enabled controls whether the operator should derive DeviceClasses from Nodes.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// LabelKey is the Node label key used to bind Nodes to DeviceClasses.
	// +optional
	LabelKey string `json:"labelKey,omitempty"`

	// OverwriteManualLabels allows discovery to replace existing non-CHILL device-class labels.
	// When omitted, the operator default is used.
	// +optional
	OverwriteManualLabels *bool `json:"overwriteManualLabels,omitempty"`

	// NodeLabelSelector limits which Nodes participate in discovery.
	// +optional
	NodeLabelSelector string `json:"nodeLabelSelector,omitempty"`

	// RequireCatalogMatch controls whether Nodes must match the catalog before a DeviceClass is created.
	// When omitted, the operator uses the safe default of true.
	// +optional
	RequireCatalogMatch *bool `json:"requireCatalogMatch,omitempty"`

	// FallbackPowerModes are used for inferred DeviceClasses when catalog matching is not required
	// and a Node does not match a catalog entry.
	// +optional
	FallbackPowerModes []PowerMode `json:"fallbackPowerModes,omitempty"`

	// Catalog references the ConfigMap containing the DeviceClass catalog.
	// +optional
	Catalog ChillConfigMapKeyRef `json:"catalog,omitempty"`
}

// ChillConfigMapKeyRef points at a ConfigMap data key.
type ChillConfigMapKeyRef struct {
	// Namespace is the ConfigMap namespace. When omitted, managementNamespace is used.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Name is the ConfigMap name.
	// +optional
	Name string `json:"name,omitempty"`

	// Key is the ConfigMap data key.
	// +optional
	Key string `json:"key,omitempty"`
}

// ChillSystemStatus defines the observed state of one CHILL system instance.
type ChillSystemStatus struct {
	// ObservedGeneration is the latest spec generation observed by the operator.
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

	// OperatorState summarizes the operator Deployment state.
	// +optional
	// +kubebuilder:validation:Enum=Ready;Progressing;Disabled;Degraded;Unknown
	OperatorState ComponentState `json:"operatorState,omitempty"`

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
// +kubebuilder:resource:scope=Cluster,shortName=csys
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Operator",type=string,JSONPath=`.status.operatorState`,priority=1
// +kubebuilder:printcolumn:name="NodeDiscovery",type=string,JSONPath=`.status.nodeDiscoveryState`,priority=1
// +kubebuilder:printcolumn:name="Classes",type=integer,JSONPath=`.status.deviceClassCount`,priority=1
// +kubebuilder:printcolumn:name="Nodes",type=integer,JSONPath=`.status.observedNodeCount`,priority=1
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`

// ChillSystem is the cluster-scoped root resource for a CHILL installation.
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
