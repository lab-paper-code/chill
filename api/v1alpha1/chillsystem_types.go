package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	// ChillSystemConditionReady reports whether the CHILL installation is usable.
	ChillSystemConditionReady = "Ready"
)

const (
	// ChillSystemReasonMaxLength is the status reason length allowed by the CRD schema.
	ChillSystemReasonMaxLength = 128
	// ChillSystemMessageMaxLength is the status message length allowed by the CRD schema.
	ChillSystemMessageMaxLength = 1024
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

	// Conditions follow the Kubernetes status condition convention.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=csys
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

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
