package v1alpha1

import (
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

// FaultInjectionPlan belongs to exactly one stage; times are relative to GO.
// +kubebuilder:validation:XValidation:rule="self.action != 'uncordon' || (has(self.priorInjectionID) && !has(self.selection))",message="uncordon requires priorInjectionID and no selection"
// +kubebuilder:validation:XValidation:rule="self.action == 'uncordon' || (has(self.selection) && !has(self.priorInjectionID))",message="pod actions require selection and no priorInjectionID"
type FaultInjectionPlan struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	ID string `json:"id"`
	// +kubebuilder:validation:Minimum=0
	OffsetSeconds float64 `json:"offsetSeconds"`
	// +kubebuilder:validation:Enum=delete-serving-pod;cordon-delete;uncordon
	Action string `json:"action"`
	// +optional
	Selection *FaultInjectionSelection `json:"selection,omitempty"`
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	PriorInjectionID string `json:"priorInjectionID,omitempty"`
}

type FaultInjectionSelection struct {
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	WorkloadUIDs []types.UID `json:"workloadUIDs"`
	// DifferentNodeFrom names a prior injection in this same stage.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	DifferentNodeFrom string `json:"differentNodeFrom,omitempty"`
	// OrderBy is the explicit deterministic tie-break order.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=3
	// +listType=atomic
	// +kubebuilder:validation:XValidation:rule="self.all(k, self.filter(v, v == k).size() == 1)",message="orderBy keys must be unique"
	OrderBy []FaultInjectionOrderKey `json:"orderBy"`
}

// +kubebuilder:validation:Enum=node;pod;podUID
type FaultInjectionOrderKey string

const (
	FaultInjectionDeleteServingPod = "delete-serving-pod"
	FaultInjectionCordonDelete     = "cordon-delete"
	FaultInjectionUncordon         = "uncordon"
)

func validateFaultInjection(plans []FaultInjectionPlan) error {
	prior := map[string]FaultInjectionPlan{}
	for _, plan := range plans {
		if len(validation.IsDNS1123Label(plan.ID)) != 0 || !finiteNonNegative(plan.OffsetSeconds) {
			return fmt.Errorf("invalid injection %q", plan.ID)
		}
		if _, exists := prior[plan.ID]; exists {
			return fmt.Errorf("duplicate injection %q", plan.ID)
		}
		if plan.Action != FaultInjectionDeleteServingPod && plan.Action != FaultInjectionCordonDelete && plan.Action != FaultInjectionUncordon {
			return fmt.Errorf("invalid injection action %q", plan.Action)
		}
		if plan.Action == FaultInjectionUncordon {
			p, exists := prior[plan.PriorInjectionID]
			if !exists || p.Action != FaultInjectionCordonDelete || p.OffsetSeconds >= plan.OffsetSeconds {
				return fmt.Errorf("uncordon %q requires an earlier cordon-delete priorInjectionID", plan.ID)
			}
			if plan.Selection != nil {
				return fmt.Errorf("uncordon uses its prior injection node, not a selection")
			}
		} else {
			if plan.PriorInjectionID != "" {
				return fmt.Errorf("priorInjectionID is only valid for uncordon")
			}
			selection := plan.Selection
			if selection == nil || len(selection.WorkloadUIDs) == 0 || len(selection.OrderBy) == 0 {
				return fmt.Errorf("injection %q requires workloadUIDs and orderBy", plan.ID)
			}
			uids := map[types.UID]bool{}
			for _, uid := range selection.WorkloadUIDs {
				if uid == "" || uids[uid] {
					return fmt.Errorf("invalid or duplicate injection workload UID")
				}
				uids[uid] = true
			}
			keys := map[FaultInjectionOrderKey]bool{}
			for _, key := range selection.OrderBy {
				if (key != "node" && key != "pod" && key != "podUID") || keys[key] {
					return fmt.Errorf("invalid or duplicate selection order key")
				}
				keys[key] = true
			}
			if selection.DifferentNodeFrom != "" {
				p, exists := prior[selection.DifferentNodeFrom]
				if !exists || p.Action == FaultInjectionUncordon || p.OffsetSeconds >= plan.OffsetSeconds {
					return fmt.Errorf("differentNodeFrom must reference an earlier target selection")
				}
			}
		}
		prior[plan.ID] = plan
	}
	return nil
}

// EvaluationStageSpec is the complete immutable builder output. Its controller
// EvaluationRun ownerReference is the sole parent identity, not a second field.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="EvaluationStage spec is immutable"
type EvaluationStageSpec struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	StageID string `json:"stageID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Row              string                    `json:"row"`
	LoadScenarioRef  EvaluationObjectReference `json:"loadScenarioRef"`
	Target           TargetIdentity            `json:"target"`
	Harness          EvaluationImage           `json:"harness"`
	LoadGenerator    Generator                 `json:"loadGenerator"`
	PowerMeasurement PowerMeasurement          `json:"powerMeasurement"`
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	SettingsPatch *apiextensionsv1.JSON `json:"settingsPatch,omitempty"`
	Judgment      Judgment              `json:"judgment"`
	// +kubebuilder:validation:MinLength=1
	EvidenceStore      string                      `json:"evidenceStore"`
	FrozenConfigMapRef EvaluationObjectReference   `json:"frozenConfigMapRef"`
	InputConfigMapRef  EvaluationObjectReference   `json:"inputConfigMapRef"`
	InputSnapshotRef   EvaluationEvidenceReference `json:"inputSnapshotRef"`
	Workspace          EvaluationObjectReference   `json:"workspace"`
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:XValidation:rule="self.all(i, self.filter(j, j.id == i.id).size() == 1)",message="injection IDs must be unique"
	// +kubebuilder:validation:XValidation:rule="self.all(i, i.action != 'uncordon' || self.exists(p, p.id == i.priorInjectionID && p.action == 'cordon-delete' && p.offsetSeconds < i.offsetSeconds))",message="uncordon requires an earlier cordon-delete in this stage"
	// +kubebuilder:validation:XValidation:rule="self.all(i, !has(i.selection) || !has(i.selection.differentNodeFrom) || self.exists(p, p.id == i.selection.differentNodeFrom && p.action != 'uncordon' && p.offsetSeconds < i.offsetSeconds))",message="differentNodeFrom requires an earlier target selection in this stage"
	FailureInjection []FaultInjectionPlan `json:"failureInjection,omitempty"`
}

// FaultInjectionRecord retains compact effect identities and evidence. Detailed
// method/path/body, candidate lists and response bodies belong in EvidenceRef.
type FaultInjectionRecord struct {
	ID          string      `json:"id"`
	ScheduledAt metav1.Time `json:"scheduledAt"`
	// +optional
	SelectedAt *metav1.Time `json:"selectedAt,omitempty"`
	// +optional
	RequestedAt *metav1.Time `json:"requestedAt,omitempty"`
	// +optional
	RespondedAt *metav1.Time `json:"respondedAt,omitempty"`
	// +optional
	EffectObservedAt *metav1.Time `json:"effectObservedAt,omitempty"`
	// +optional
	ActualOffsetSeconds *float64 `json:"actualOffsetSeconds,omitempty"`
	// +optional
	PodRef *EvaluationObjectReference `json:"podRef,omitempty"`
	// +optional
	NodeRef *EvaluationObjectReference `json:"nodeRef,omitempty"`
	// +optional
	Outcome string `json:"outcome,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// +optional
	EvidenceRef *EvaluationEvidenceReference `json:"evidenceRef,omitempty"`
}

type EvaluationProducerReference struct {
	WorkloadUID types.UID                 `json:"workloadUID"`
	Shard       int32                     `json:"shard"`
	JobRef      EvaluationObjectReference `json:"jobRef"`
	// +optional
	PodRef *EvaluationObjectReference `json:"podRef,omitempty"`
}

// EvaluationProvisionalD1 is the latest closed-minute request projection, not a verdict.
type EvaluationProvisionalD1 struct {
	// AsOf is the snapshot time; unfinished counts may change in later snapshots.
	AsOf string `json:"asOf"`
	// ClosedThrough bounds raw closed minutes and scheduled arrivals, not request completion.
	ClosedThrough    string                                   `json:"closedThrough"`
	Provisional      bool                                     `json:"provisional"`
	Workloads        map[string]EvaluationProvisionalRequests `json:"workloads"`
	GeneratorQuality map[string]EvaluationIssuanceFidelity    `json:"generatorQuality"`
}

type EvaluationProvisionalRequests struct {
	// Offered counts frozen arrivals scheduled at or before ClosedThrough.
	Offered                int64            `json:"offered"`
	DeadlineSuccess        int64            `json:"deadlineSuccess"`
	RequiredSuccessfulRank int64            `json:"requiredSuccessfulRank"`
	Outcomes               map[string]int64 `json:"outcomes"`
	// UnfinishedOrUnsent counts requests without a suitable completed HTTP final as of the snapshot,
	// including requests still in flight or without a client result. It is not a settled loss count.
	UnfinishedOrUnsent int64                           `json:"unfinishedOrUnsent"`
	SLO                EvaluationRequestSLO            `json:"slo"`
	Reachable          bool                            `json:"reachable"`
	Substages          []EvaluationProvisionalSubstage `json:"substages"`
}

type EvaluationRequestSLO struct {
	Percentile         float64 `json:"percentile"`
	TargetLatencyNanos int64   `json:"targetLatencyNanos"`
}

type EvaluationProvisionalSubstage struct {
	ID                     string                 `json:"id"`
	DurationSeconds        float64                `json:"durationSeconds"`
	Rate                   float64                `json:"rate"`
	Offered                int64                  `json:"offered"`
	DeadlineSuccess        int64                  `json:"deadlineSuccess"`
	RequiredSuccessfulRank int64                  `json:"requiredSuccessfulRank"`
	CompletedLatencyNS     EvaluationRequestStats `json:"completedLatencyNS"`
}

type EvaluationRequestStats struct {
	Samples int64 `json:"samples"`
	// +nullable
	Mean *float64 `json:"mean"`
	// +nullable
	P50 *float64 `json:"p50"`
	// +nullable
	P95 *float64 `json:"p95"`
	// +nullable
	P99 *float64 `json:"p99"`
	// +nullable
	Max *float64 `json:"max"`
}

type EvaluationGeneratorQualityPolicy struct {
	LatenessToleranceNS  int64   `json:"latenessToleranceNS"`
	MaxExceedingFraction float64 `json:"maxExceedingFraction"`
}

type EvaluationGeneratorQuality struct {
	Status string `json:"status"`
	// +nullable
	Policy      *EvaluationGeneratorQualityPolicy `json:"policy"`
	Exceeding   int64                             `json:"exceeding"`
	Denominator int64                             `json:"denominator"`
	// +nullable
	ExceedingFraction *float64 `json:"exceedingFraction"`
	ThresholdLabel    string   `json:"thresholdLabel"`
	// +nullable
	Reason *string `json:"reason"`
}

type EvaluationIssuanceFidelity struct {
	FailurePolicy         string `json:"failurePolicy"`
	Status                string `json:"status"`
	ThresholdNS           int64  `json:"thresholdNS"`
	Offered               int64  `json:"offered"`
	Issued                int64  `json:"issued"`
	Unissued              int64  `json:"unissued"`
	MissingClientRows     int64  `json:"missingClientRows"`
	ExtraClientRows       int64  `json:"extraClientRows"`
	DuplicateIndexes      int64  `json:"duplicateIndexes"`
	InvalidScheduleRows   int64  `json:"invalidScheduleRows"`
	MissingSendTimestamps int64  `json:"missingSendTimestamps"`
	Supplied              bool   `json:"supplied"`
	// +nullable
	IssuanceFraction *float64 `json:"issuanceFraction"`
	DurationNS       int64    `json:"durationNS"`
	// +nullable
	AchievedRequestsPerSecond *float64                   `json:"achievedRequestsPerSecond"`
	SendsAfterWindow          int64                      `json:"sendsAfterWindow"`
	SendLatenessNS            EvaluationRequestStats     `json:"sendLatenessNS"`
	ActualGapNS               EvaluationRequestStats     `json:"actualGapNS"`
	PlannedGapNS              EvaluationRequestStats     `json:"plannedGapNS"`
	GeneratorQuality          EvaluationGeneratorQuality `json:"generatorQuality"`
}

type EvaluationStageStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	ProvisionalD1 *EvaluationProvisionalD1 `json:"provisionalD1,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=3
	// +kubebuilder:validation:XValidation:rule="self.all(c, c.type in ['ExecutionComplete', 'MeasurementPassed', 'StageCleanupComplete'])",message="unsupported EvaluationStage condition"
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// +optional
	CurrentProcedure string `json:"currentProcedure,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=step
	// +kubebuilder:validation:XValidation:rule="self.all(p, p.step in ['factor', 'stage', 'judge', 'cleanup-jobs'])",message="child procedures must be stage-local"
	Procedures []EvaluationProcedure `json:"procedures,omitempty"`
	// +optional
	JobRef *EvaluationObjectReference `json:"jobRef,omitempty"`
	// +optional
	PodRef *EvaluationObjectReference `json:"podRef,omitempty"`
	// +optional
	ProducerRefs []EvaluationProducerReference `json:"producerRefs,omitempty"`
	// +optional
	GoRef *EvaluationEvidenceReference `json:"goRef,omitempty"`
	// GoAt is recorded once from validated GO evidence.
	// +optional
	GoAt *metav1.Time `json:"goAt,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=id
	InjectionRecords []FaultInjectionRecord `json:"injectionRecords,omitempty"`
	// +optional
	MeasurementVerdict MeasurementVerdict `json:"measurementVerdict,omitempty"`
	// +optional
	ResultRef *EvaluationEvidenceReference `json:"resultRef,omitempty"`
	// +optional
	Outputs map[string]EvaluationEvidenceReference `json:"outputs,omitempty"`
	// +optional
	ResidualStateRef *EvaluationEvidenceReference `json:"residualStateRef,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=evalstage
// +kubebuilder:printcolumn:name="Stage",type=string,JSONPath=`.spec.stageID`
// +kubebuilder:printcolumn:name="Execution",type=string,JSONPath=`.status.conditions[?(@.type=="ExecutionComplete")].status`
// +kubebuilder:printcolumn:name="Measurement",type=string,JSONPath=`.status.measurementVerdict`
// +kubebuilder:printcolumn:name="Cleanup",type=string,JSONPath=`.status.conditions[?(@.type=="StageCleanupComplete")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type EvaluationStage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              EvaluationStageSpec `json:"spec"`
	// +optional
	Status EvaluationStageStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type EvaluationStageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EvaluationStage `json:"items"`
}

func init() { SchemeBuilder.Register(&EvaluationStage{}, &EvaluationStageList{}) }
