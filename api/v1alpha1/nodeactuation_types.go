package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// NodeActuationPhase is the NodeActuator-owned observed lifecycle phase.
// +kubebuilder:validation:Enum=Pending;AcquiringLease;ObservingPrevious;Applying;Verifying;Settling;Ready;Failed
type NodeActuationPhase string

const (
	NodeActuationPhasePending           NodeActuationPhase = "Pending"
	NodeActuationPhaseAcquiringLease    NodeActuationPhase = "AcquiringLease"
	NodeActuationPhaseObservingPrevious NodeActuationPhase = "ObservingPrevious"
	NodeActuationPhaseApplying          NodeActuationPhase = "Applying"
	NodeActuationPhaseVerifying         NodeActuationPhase = "Verifying"
	NodeActuationPhaseSettling          NodeActuationPhase = "Settling"
	NodeActuationPhaseReady             NodeActuationPhase = "Ready"
	NodeActuationPhaseFailed            NodeActuationPhase = "Failed"
)

// NodeActuationFailureStage identifies the operation that failed terminally.
// +kubebuilder:validation:Enum=Lease;Policy;ObservePrevious;Apply;Verify;Settle
type NodeActuationFailureStage string

const (
	NodeActuationFailureStageLease           NodeActuationFailureStage = "Lease"
	NodeActuationFailureStagePolicy          NodeActuationFailureStage = "Policy"
	NodeActuationFailureStageObservePrevious NodeActuationFailureStage = "ObservePrevious"
	NodeActuationFailureStageApply           NodeActuationFailureStage = "Apply"
	NodeActuationFailureStageVerify          NodeActuationFailureStage = "Verify"
	NodeActuationFailureStageSettle          NodeActuationFailureStage = "Settle"
)

// NodeActuationSpec is one immutable durable transition request.
// +kubebuilder:validation:XValidation:rule="self.node == oldSelf.node",message="node identity is immutable"
// +kubebuilder:validation:XValidation:rule="self.deviceClass == oldSelf.deviceClass",message="deviceClass is immutable"
// +kubebuilder:validation:XValidation:rule="self.powerMode == oldSelf.powerMode",message="powerMode is immutable"
// +kubebuilder:validation:XValidation:rule="self.actuator == oldSelf.actuator",message="actuator target is immutable"
// +kubebuilder:validation:XValidation:rule="self.settlingDurationSeconds == oldSelf.settlingDurationSeconds",message="settlingDurationSeconds is immutable"
// +kubebuilder:validation:XValidation:rule="self.maxActuationDurationSeconds == oldSelf.maxActuationDurationSeconds",message="maxActuationDurationSeconds is immutable"
// +kubebuilder:validation:XValidation:rule="self.deadline == oldSelf.deadline",message="deadline is immutable"
// +kubebuilder:validation:XValidation:rule="self.maxActuationDurationSeconds > self.settlingDurationSeconds",message="maxActuationDurationSeconds must exceed settlingDurationSeconds"
// +kubebuilder:validation:XValidation:rule="self.deadline > self.stageRequestedAt",message="deadline must follow stageRequestedAt"
// +kubebuilder:validation:XValidation:rule="self.stageRequestedAt == oldSelf.stageRequestedAt",message="stageRequestedAt is immutable"
type NodeActuationSpec struct {
	// Node is the exact cluster-scoped Kubernetes Node identity.
	Node ProfilingRunObjectReference `json:"node"`

	// DeviceClass is the frozen class used to select the actuation policy.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	DeviceClass string `json:"deviceClass"`

	// PowerMode is the stable selected mode name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PowerMode string `json:"powerMode"`

	// Actuator is the exact non-fixed typed target selected from DeviceClass.
	// +kubebuilder:validation:XValidation:rule="self.kind != 'Fixed'",message="NodeActuation requires a mutable actuator target"
	Actuator PowerModeActuator `json:"actuator"`

	// PolicyName identifies the selected configured actuation policy.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	PolicyName string `json:"policyName"`

	// SettlingDurationSeconds is the frozen post-verification settling period.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=86400
	SettlingDurationSeconds int32 `json:"settlingDurationSeconds"`

	// MaxActuationDurationSeconds is the frozen total handoff bound.
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:validation:Maximum=86400
	MaxActuationDurationSeconds int32 `json:"maxActuationDurationSeconds"`

	// Deadline is the absolute bound for reaching the settled target state.
	Deadline metav1.Time `json:"deadline"`

	// StageRequestedAt records when the transition was requested.
	StageRequestedAt metav1.Time `json:"stageRequestedAt"`
}

// NodeActuationObservation is one independently read typed host state.
type NodeActuationObservation struct {
	// PowerMode is populated when the observed target maps unambiguously to a
	// stable DeviceClass mode name.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	PowerMode string `json:"powerMode,omitempty"`

	Actuator PowerModeActuator `json:"actuator"`

	ObservedAt metav1.MicroTime `json:"observedAt"`
}

// NodeActuationFailure is a terminal, operator-visible actuation failure.
type NodeActuationFailure struct {
	Stage NodeActuationFailureStage `json:"stage"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Reason string `json:"reason"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message"`

	OccurredAt metav1.Time `json:"occurredAt"`
}

// NodeActuationTransitionCost records the duration of one completed state
// change in integer milliseconds.
type NodeActuationTransitionCost struct {
	// EndToEndMilliseconds measures from the durable pre-mutation apply fence
	// through terminal Ready.
	EndToEndMilliseconds int64 `json:"endToEndMilliseconds"`

	// ApplyAndReadBackMilliseconds measures from the durable pre-mutation fence
	// through independent target read-back and entry into settling. It is
	// controller-path cost, not a device-only measurement: it includes two
	// edge-to-control-plane round trips and two nvpmodel invocations in addition
	// to the device change.
	ApplyAndReadBackMilliseconds int64 `json:"applyAndReadBackMilliseconds"`

	// SettlingMilliseconds measures from the recorded settling start through
	// terminal Ready.
	SettlingMilliseconds int64 `json:"settlingMilliseconds"`
}

// NodeActuationStatus is written only by the responsible per-Node actuator.
// +kubebuilder:validation:XValidation:rule="!has(self.terminalFailure) || has(self.phase) && self.phase == 'Failed'",message="terminalFailure is valid only after failure"
// +kubebuilder:validation:XValidation:rule="!has(self.phase) || self.phase != 'Failed' || has(self.terminalFailure)",message="Failed phase requires terminalFailure"
// +kubebuilder:validation:XValidation:rule="!has(self.phase) || self.phase != 'Ready' || has(self.previousObservation) && has(self.targetObservation) && has(self.readyAt)",message="Ready phase requires previous and target observations plus readyAt"
type NodeActuationStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	Phase NodeActuationPhase `json:"phase,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=128
	Reason string `json:"reason,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`

	// Lease names the Node-UID-keyed lock held for this handoff.
	// +optional
	Lease *ProfilingRunObjectReference `json:"lease,omitempty"`

	// +optional
	PreviousObservation *NodeActuationObservation `json:"previousObservation,omitempty"`

	// +optional
	TargetObservation *NodeActuationObservation `json:"targetObservation,omitempty"`

	// +optional
	ApplyStartedAt *metav1.MicroTime `json:"applyStartedAt,omitempty"`

	// +optional
	VerifiedAt *metav1.MicroTime `json:"verifiedAt,omitempty"`

	// +optional
	SettlingStartedAt *metav1.MicroTime `json:"settlingStartedAt,omitempty"`

	// +optional
	ReadyAt *metav1.MicroTime `json:"readyAt,omitempty"`

	// TransitionCost records Node-clock duration only after a state change
	// reaches Ready.
	// +optional
	TransitionCost *NodeActuationTransitionCost `json:"transitionCost,omitempty"`

	// TerminalFailure records the single terminal failure outcome.
	// +optional
	TerminalFailure *NodeActuationFailure `json:"terminalFailure,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nact
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.node.name`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.powerMode`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NodeActuation is one durable transition toward a declared Node state.
type NodeActuation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeActuationSpec   `json:"spec"`
	Status NodeActuationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeActuationList contains Node transitions.
type NodeActuationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeActuation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeActuation{}, &NodeActuationList{})
}
