package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// NodeActuationDesiredStage is the Profiler-owned requested lifecycle stage.
// +kubebuilder:validation:Enum=Apply;Restore
type NodeActuationDesiredStage string

const (
	NodeActuationDesiredStageApply   NodeActuationDesiredStage = "Apply"
	NodeActuationDesiredStageRestore NodeActuationDesiredStage = "Restore"
)

// NodeActuationPhase is the NodeActuator-owned observed lifecycle phase.
// +kubebuilder:validation:Enum=Pending;AcquiringLease;CheckingSafety;ObservingPrevious;Applying;Verifying;Settling;Ready;Restoring;VerifyingRestore;Restored;Failed
type NodeActuationPhase string

const (
	NodeActuationPhasePending           NodeActuationPhase = "Pending"
	NodeActuationPhaseAcquiringLease    NodeActuationPhase = "AcquiringLease"
	NodeActuationPhaseCheckingSafety    NodeActuationPhase = "CheckingSafety"
	NodeActuationPhaseObservingPrevious NodeActuationPhase = "ObservingPrevious"
	NodeActuationPhaseApplying          NodeActuationPhase = "Applying"
	NodeActuationPhaseVerifying         NodeActuationPhase = "Verifying"
	NodeActuationPhaseSettling          NodeActuationPhase = "Settling"
	NodeActuationPhaseReady             NodeActuationPhase = "Ready"
	NodeActuationPhaseRestoring         NodeActuationPhase = "Restoring"
	NodeActuationPhaseVerifyingRestore  NodeActuationPhase = "VerifyingRestore"
	NodeActuationPhaseRestored          NodeActuationPhase = "Restored"
	NodeActuationPhaseFailed            NodeActuationPhase = "Failed"
)

// NodeActuationFailureStage identifies the operation that failed terminally.
// +kubebuilder:validation:Enum=Lease;Policy;Safety;ObservePrevious;Apply;Verify;Settle;Hold;Restore
type NodeActuationFailureStage string

const (
	NodeActuationFailureStageLease           NodeActuationFailureStage = "Lease"
	NodeActuationFailureStagePolicy          NodeActuationFailureStage = "Policy"
	NodeActuationFailureStageSafety          NodeActuationFailureStage = "Safety"
	NodeActuationFailureStageObservePrevious NodeActuationFailureStage = "ObservePrevious"
	NodeActuationFailureStageApply           NodeActuationFailureStage = "Apply"
	NodeActuationFailureStageVerify          NodeActuationFailureStage = "Verify"
	NodeActuationFailureStageSettle          NodeActuationFailureStage = "Settle"
	NodeActuationFailureStageHold            NodeActuationFailureStage = "Hold"
	NodeActuationFailureStageRestore         NodeActuationFailureStage = "Restore"
)

// NodeActuationSpec is the durable Profiler-to-NodeActuator handoff. Identity,
// target, policy, timing, and deadline are immutable; only desiredStage and its
// request timestamp may advance once from Apply to Restore.
// +kubebuilder:validation:XValidation:rule="self.run == oldSelf.run",message="run identity is immutable"
// +kubebuilder:validation:XValidation:rule="self.node == oldSelf.node",message="node identity is immutable"
// +kubebuilder:validation:XValidation:rule="self.deviceClass == oldSelf.deviceClass",message="deviceClass is immutable"
// +kubebuilder:validation:XValidation:rule="self.powerMode == oldSelf.powerMode",message="powerMode is immutable"
// +kubebuilder:validation:XValidation:rule="self.actuator == oldSelf.actuator",message="actuator target is immutable"
// +kubebuilder:validation:XValidation:rule="self.settlingDurationSeconds == oldSelf.settlingDurationSeconds",message="settlingDurationSeconds is immutable"
// +kubebuilder:validation:XValidation:rule="self.maxActuationDurationSeconds == oldSelf.maxActuationDurationSeconds",message="maxActuationDurationSeconds is immutable"
// +kubebuilder:validation:XValidation:rule="self.deadline == oldSelf.deadline",message="deadline is immutable"
// +kubebuilder:validation:XValidation:rule="self.maxActuationDurationSeconds > self.settlingDurationSeconds",message="maxActuationDurationSeconds must exceed settlingDurationSeconds"
// +kubebuilder:validation:XValidation:rule="self.deadline > self.stageRequestedAt",message="deadline must follow stageRequestedAt"
// +kubebuilder:validation:XValidation:rule="self.desiredStage == oldSelf.desiredStage || oldSelf.desiredStage == 'Apply' && self.desiredStage == 'Restore'",message="desiredStage may only advance from Apply to Restore"
// +kubebuilder:validation:XValidation:rule="self.desiredStage == oldSelf.desiredStage ? self.stageRequestedAt == oldSelf.stageRequestedAt : self.stageRequestedAt > oldSelf.stageRequestedAt",message="stageRequestedAt changes only when desiredStage advances"
type NodeActuationSpec struct {
	// Run is the exact owning ProfilingRun identity in this namespace.
	Run ProfilingRunObjectReference `json:"run"`

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

	// Deadline is the absolute fail-safe restoration deadline.
	Deadline metav1.Time `json:"deadline"`

	// DesiredStage is written only by the Profiler.
	DesiredStage NodeActuationDesiredStage `json:"desiredStage"`

	// StageRequestedAt records when the current desired stage was requested.
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

	ObservedAt metav1.Time `json:"observedAt"`
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

// NodeActuationSafetyVerdict is the fail-closed workload coexistence result.
// +kubebuilder:validation:Enum=Allowed;Blocked
type NodeActuationSafetyVerdict string

const (
	NodeActuationSafetyAllowed NodeActuationSafetyVerdict = "Allowed"
	NodeActuationSafetyBlocked NodeActuationSafetyVerdict = "Blocked"
)

type NodeActuationPodReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UID string `json:"uid"`
}

// NodeActuationSafetyObservation records the workload set result used by the
// actuator. Raw Pod objects remain authoritative in Kubernetes.
// +kubebuilder:validation:XValidation:rule="self.verdict == 'Allowed' ? !has(self.blockingPods) || size(self.blockingPods) == 0 : has(self.blockingPods) && size(self.blockingPods) > 0",message="Blocked safety requires blockingPods and Allowed safety forbids them"
type NodeActuationSafetyObservation struct {
	Verdict NodeActuationSafetyVerdict `json:"verdict"`

	ObservedAt metav1.Time `json:"observedAt"`

	// +optional
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=uid
	BlockingPods []NodeActuationPodReference `json:"blockingPods,omitempty"`
}

// NodeActuationStatus is written only by the responsible per-Node actuator.
// +kubebuilder:validation:XValidation:rule="!has(self.terminalFailure) || has(self.phase) && self.phase in ['Restoring', 'VerifyingRestore', 'Failed']",message="terminalFailure is valid only while restoring or after failure"
// +kubebuilder:validation:XValidation:rule="!has(self.phase) || self.phase != 'Failed' || has(self.terminalFailure)",message="Failed phase requires terminalFailure"
// +kubebuilder:validation:XValidation:rule="!has(self.phase) || self.phase != 'Ready' || has(self.previousObservation) && has(self.targetObservation) && has(self.readyAt)",message="Ready phase requires previous and target observations plus readyAt"
// +kubebuilder:validation:XValidation:rule="!has(self.phase) || self.phase != 'Restoring' || has(self.previousObservation) && has(self.restoreStartedAt)",message="Restoring phase requires previous observation and restoreStartedAt"
// +kubebuilder:validation:XValidation:rule="!has(self.phase) || self.phase != 'VerifyingRestore' || has(self.previousObservation) && has(self.restoreStartedAt) && has(self.restoreAppliedAt)",message="VerifyingRestore phase requires previous observation and restore timestamps"
// +kubebuilder:validation:XValidation:rule="!has(self.phase) || self.phase != 'Restored' || has(self.previousObservation) && has(self.restoredObservation) && has(self.restoredAt)",message="Restored phase requires previous and restored observations plus restoredAt"
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

	// Safety is the latest fail-closed workload coexistence observation.
	// +optional
	Safety *NodeActuationSafetyObservation `json:"safety,omitempty"`

	// Lease names the Node-UID-keyed lock held for this handoff.
	// +optional
	Lease *ProfilingRunObjectReference `json:"lease,omitempty"`

	// +optional
	PreviousObservation *NodeActuationObservation `json:"previousObservation,omitempty"`

	// +optional
	TargetObservation *NodeActuationObservation `json:"targetObservation,omitempty"`

	// +optional
	RestoredObservation *NodeActuationObservation `json:"restoredObservation,omitempty"`

	// +optional
	ApplyStartedAt *metav1.Time `json:"applyStartedAt,omitempty"`

	// +optional
	VerifiedAt *metav1.Time `json:"verifiedAt,omitempty"`

	// +optional
	SettlingStartedAt *metav1.Time `json:"settlingStartedAt,omitempty"`

	// +optional
	ReadyAt *metav1.Time `json:"readyAt,omitempty"`

	// +optional
	RestoreStartedAt *metav1.Time `json:"restoreStartedAt,omitempty"`

	// RestoreAppliedAt separates the idempotent restore command from its
	// independent read-back after restart.
	// +optional
	RestoreAppliedAt *metav1.Time `json:"restoreAppliedAt,omitempty"`

	// +optional
	RestoredAt *metav1.Time `json:"restoredAt,omitempty"`

	// TerminalFailure makes the measurement path terminal. It may be retained
	// while fail-safe restoration continues; a restore failure replaces it and
	// keeps the Node locked for operator attention.
	// +optional
	TerminalFailure *NodeActuationFailure `json:"terminalFailure,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nact
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.node.name`
// +kubebuilder:printcolumn:name="Desired",type=string,JSONPath=`.spec.desiredStage`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NodeActuation is one durable Profiler-to-NodeActuator handoff for one Run.
type NodeActuation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeActuationSpec   `json:"spec"`
	Status NodeActuationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeActuationList contains NodeActuation handoffs.
type NodeActuationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeActuation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeActuation{}, &NodeActuationList{})
}
