package v1alpha1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	EvaluationConditionPoolAcquired              = "PoolAcquired"
	EvaluationConditionExecutionComplete         = "ExecutionComplete"
	EvaluationConditionMeasurementPassed         = "MeasurementPassed"
	EvaluationConditionCleanupComplete           = "CleanupComplete"
	EvaluationConditionPublished                 = "Published"
	EvaluationConditionStageCleanupComplete      = "StageCleanupComplete"
	EvaluationReasonNotStarted                   = "NotStarted"
	EvaluationReasonPoolBusy                     = "PoolBusy"
	EvaluationReasonProcedureFailed              = "ProcedureFailed"
	EvaluationReasonRemoteEffectUnknown          = "RemoteEffectUnknown"
	EvaluationReasonSLOFailed                    = "SLOFailed"
	EvaluationReasonGeneratorInvalid             = "GeneratorInvalid"
	EvaluationReasonPowerIncomplete              = "PowerIncomplete"
	EvaluationReasonInjectionInvalid             = "InjectionInvalid"
	EvaluationReasonReturnTimeout                = "ReturnTimeout"
	EvaluationReasonResetWaitTimeout             = "ResetWaitTimeout"
	EvaluationReasonResidualState                = "ResidualState"
	EvaluationReasonOwnedJobNotFound             = "OwnedJobNotFound"
	EvaluationReasonObservationUnavailable       = "ObservationUnavailable"
	EvaluationReasonEvidenceStoreFailed          = "EvidenceStoreFailed"
	EvaluationReasonPublicationFailed            = "PublicationFailed"
	EvaluationReasonPublicationConflict          = "PublicationConflict"
	EvaluationReasonMissingInput                 = "MissingInput"
	EvaluationReasonFrozenInputInvalid           = "FrozenInputInvalid"
	EvaluationReasonTargetUnavailable            = "TargetUnavailable"
	EvaluationReasonIdentityMismatch             = "IdentityMismatch"
	EvaluationReasonEvidenceNotVisible           = "EvidenceNotVisible"
	EvaluationReasonCollectorFailed              = "CollectorFailed"
	EvaluationReasonRestoreFailed                = "RestoreFailed"
	EvaluationReasonExecutionLost                = "ExecutionLost"
	EvaluationReasonDrainTimeout                 = "DrainTimeout"
	EvaluationReasonEvidenceUnreadable           = "EvidenceUnreadable"
	EvaluationReasonEvidenceCapacityInsufficient = "EvidenceCapacityInsufficient"
	EvaluationReasonJudgmentFailed               = "JudgmentFailed"
	EvaluationReasonParentRequired               = "ParentRequired"
	EvaluationStepPrepare                        = "prepare"
	EvaluationStepFactor                         = "factor"
	EvaluationStepStage                          = "stage"
	EvaluationStepJudge                          = "judge"
	EvaluationStepCleanupJobs                    = "cleanup-jobs"
	EvaluationStepFinish                         = "finish"
	EvaluationStepPublish                        = "publish"
	EvaluationLabelRunUID                        = "edge.dacs.io/evaluation-run-uid"
	EvaluationLabelStage                         = "edge.dacs.io/evaluation-stage"
	EvaluationLabelStep                          = "edge.dacs.io/evaluation-step"
	EvaluationReleaseAnnotation                  = "edge.dacs.io/evaluation-release"
	EvaluationRunFinalizer                       = "edge.dacs.io/evaluation-run"
	// EvaluationConfigName is the namespace ConfigMap a Run freezes when it takes the pool.
	EvaluationConfigName     = "chill-evaluation-config"
	EvaluationStageFinalizer = "edge.dacs.io/evaluation-stage"
)

// +kubebuilder:validation:Enum=PASS;FAIL;INVALID;NOT_EVALUATED
type MeasurementVerdict string

const (
	MeasurementPASS         MeasurementVerdict = "PASS"
	MeasurementFAIL         MeasurementVerdict = "FAIL"
	MeasurementINVALID      MeasurementVerdict = "INVALID"
	MeasurementNotEvaluated MeasurementVerdict = "NOT_EVALUATED"
)

// AggregateMeasurement preserves invalidity before incompleteness before failure.
// No results (including stages not started) is not a passing measurement.
func AggregateMeasurement(verdicts ...MeasurementVerdict) (metav1.ConditionStatus, MeasurementVerdict) {
	result := MeasurementPASS
	if len(verdicts) == 0 {
		result = MeasurementNotEvaluated
	}
	for _, verdict := range verdicts {
		switch verdict {
		case MeasurementINVALID:
			return metav1.ConditionFalse, MeasurementINVALID
		case MeasurementPASS:
		case MeasurementFAIL:
			if result == MeasurementPASS {
				result = MeasurementFAIL
			}
		default:
			result = MeasurementNotEvaluated
		}
	}
	switch result {
	case MeasurementPASS:
		return metav1.ConditionTrue, result
	case MeasurementNotEvaluated:
		return metav1.ConditionUnknown, result
	default:
		return metav1.ConditionFalse, result
	}
}

type EvaluationImage struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:Pattern=`@sha256:`
	Image string `json:"image"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	SourceSHA string `json:"sourceSHA"`
}

type EvaluationResourceReference struct {
	// +kubebuilder:validation:MinLength=1
	APIVersion string `json:"apiVersion"`
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`
	// Namespace is the release namespace; cross-namespace references are unsupported.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

type EvaluationTargetComponent struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name     string                      `json:"name"`
	Resource EvaluationResourceReference `json:"resource"`
}

type TargetIdentity struct {
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	Components []EvaluationTargetComponent `json:"components"`
}

// Generator defines scientific load-generator identity and release conditions.
// +kubebuilder:validation:XValidation:rule="!has(self.resources.requests) || !('cpu' in self.resources.requests)",message="generator CPU requests are not supported"
// +kubebuilder:validation:XValidation:rule="!has(self.resources.limits) || !('cpu' in self.resources.limits)",message="generator CPU limits are not supported"
type Generator struct {
	EvaluationImage `json:",inline"`
	Resources       corev1.ResourceRequirements `json:"resources"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	NodeName string `json:"nodeName"`
	// Shards are keyed by Workload name, resolved against LoadScenario at prepare.
	// +kubebuilder:validation:MinProperties=1
	// +kubebuilder:validation:MaxProperties=128
	// +kubebuilder:validation:XValidation:rule="self.all(k, self[k] > 0)",message="shards must be positive"
	Shards  map[string]int32 `json:"shards"`
	Barrier GeneratorBarrier `json:"barrier"`
}

type GeneratorBarrier struct {
	// +kubebuilder:validation:Minimum=0
	MaxStartSkewNanos int64 `json:"maxStartSkewNanos"`
	// +kubebuilder:validation:Minimum=1
	MinimumReadySamples int32 `json:"minimumReadySamples"`
}

type PowerMeasurement struct {
	// +listType=map
	// +listMapKey=id
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	Meters []EvaluationMeter `json:"meters"`
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	Nodes []EvaluationObjectReference `json:"nodes"`
	// +kubebuilder:validation:MinLength=1
	Series   string        `json:"series"`
	Sampling PowerSampling `json:"sampling"`
}

type EvaluationMeter struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ID string `json:"id"`
	// +kubebuilder:validation:MinLength=1
	SourceIdentity string `json:"sourceIdentity"`
}

type PowerSampling struct {
	// +kubebuilder:validation:Minimum=1
	IntervalNanos int64 `json:"intervalNanos"`
}

type Judgment struct {
	GeneratorQuality GeneratorQuality `json:"generatorQuality"`
	EnergyCoverage   EnergyCoverage   `json:"energyCoverage"`
}

type GeneratorQuality struct {
	// +kubebuilder:validation:Minimum=0
	LatenessToleranceNanos int64 `json:"latenessToleranceNanos"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	MaxExceedingFraction float64 `json:"maxExceedingFraction"`
}

type EnergyCoverage struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	MaxGapFraction float64 `json:"maxGapFraction"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:ExclusiveMinimum=true
	MaxSingleGapSeconds float64 `json:"maxSingleGapSeconds"`
}

// StageTemplate assembles one child. A stage settingsPatch replaces the Run patch.
type StageTemplate struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	ID string `json:"id"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Row             string                    `json:"row"`
	LoadScenarioRef EvaluationObjectReference `json:"loadScenarioRef"`
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	SettingsPatch *apiextensionsv1.JSON `json:"settingsPatch,omitempty"`
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:XValidation:rule="self.all(i, self.filter(j, j.id == i.id).size() == 1)",message="injection IDs must be unique"
	// +kubebuilder:validation:XValidation:rule="self.all(i, i.action != 'uncordon' || self.exists(p, p.id == i.priorInjectionID && p.action == 'cordon-delete' && p.offsetSeconds < i.offsetSeconds))",message="uncordon requires an earlier cordon-delete in this stage"
	// +kubebuilder:validation:XValidation:rule="self.all(i, !has(i.selection) || !has(i.selection.differentNodeFrom) || self.exists(p, p.id == i.selection.differentNodeFrom && p.action != 'uncordon' && p.offsetSeconds < i.offsetSeconds))",message="differentNodeFrom requires an earlier target selection in this stage"
	FailureInjection []FaultInjectionPlan `json:"failureInjection,omitempty"`
}

type EvaluationComparison struct {
	Left  EvaluationComparisonReference `json:"left"`
	Right EvaluationComparisonReference `json:"right"`
	// +kubebuilder:validation:MinLength=1
	Reason string `json:"reason"`
}

// +kubebuilder:validation:XValidation:rule="has(self.stageID) != has(self.historical)",message="exactly one comparison reference is required"
type EvaluationComparisonReference struct {
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	StageID string `json:"stageID,omitempty"`
	// +optional
	Historical *EvaluationHistoricalReference `json:"historical,omitempty"`
}

type EvaluationHistoricalReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_-][A-Za-z0-9_.-]*$`
	PublicationID string `json:"publicationID"`
	// +kubebuilder:validation:MinLength=1
	StageID string `json:"stageID"`
}

// EvaluationRunSpec fixes the question, assembly and comparison, not operations.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="EvaluationRun spec is immutable"
// +kubebuilder:validation:XValidation:rule="!has(self.comparisons) || self.comparisons.all(c, (!has(c.left.stageID) || self.stages.exists(s, s.id == c.left.stageID)) && (!has(c.right.stageID) || self.stages.exists(s, s.id == c.right.stageID)))",message="comparison stage must exist"
// +kubebuilder:validation:XValidation:rule="self.stages.all(s, self.stages.filter(t, t.id == s.id).size() == 1)",message="stage IDs must be unique"
type EvaluationRunSpec struct {
	// +kubebuilder:validation:MinLength=1
	Question         string           `json:"question"`
	Target           TargetIdentity   `json:"target"`
	Harness          EvaluationImage  `json:"harness"`
	LoadGenerator    Generator        `json:"loadGenerator"`
	PowerMeasurement PowerMeasurement `json:"powerMeasurement"`
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	SettingsPatch *apiextensionsv1.JSON `json:"settingsPatch,omitempty"`
	Judgment      Judgment              `json:"judgment"`
	// +kubebuilder:validation:MinLength=1
	EvidenceStore string `json:"evidenceStore"`
	// Stages is ordered; do not use map-list equality for immutable execution order.
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	Stages []StageTemplate `json:"stages"`
	// +optional
	// +kubebuilder:validation:MaxItems=128
	Comparisons []EvaluationComparison `json:"comparisons,omitempty"`
}

// Validate is called once before any execution effects, not on every reconcile.
func (s *EvaluationRunSpec) Validate() error {
	if s.Question == "" || s.EvidenceStore == "" || len(s.Stages) == 0 {
		return fmt.Errorf("question, evidenceStore and stages are required")
	}
	if err := validateEvaluationContext(s.Target, s.Harness, s.LoadGenerator, s.PowerMeasurement, s.Judgment); err != nil {
		return err
	}
	if err := ValidateEvaluationSettingsPatch(s.SettingsPatch); err != nil {
		return err
	}
	prior := map[string]bool{}
	for _, stage := range s.Stages {
		if len(validation.IsDNS1123Label(stage.ID)) != 0 || prior[stage.ID] {
			return fmt.Errorf("invalid or duplicate stage ID %q", stage.ID)
		}
		if err := ValidateEvaluationSettingsPatch(stage.SettingsPatch); err != nil {
			return fmt.Errorf("stage %q: %w", stage.ID, err)
		}
		if stage.Row == "" || !validEvaluationObject(stage.LoadScenarioRef) {
			return fmt.Errorf("row and loadScenarioRef required for %q", stage.ID)
		}
		if err := validateFaultInjection(stage.FailureInjection); err != nil {
			return fmt.Errorf("stage %q: %w", stage.ID, err)
		}
		prior[stage.ID] = true
	}
	return validateEvaluationComparisons(s.Comparisons, prior)
}

// ValidateEvaluationSettingsPatch is deliberately shape-only: the public
// scheduler settings boundary owns field names, types and value validation.
func ValidateEvaluationSettingsPatch(patch *apiextensionsv1.JSON) error {
	if patch == nil {
		return nil
	}
	raw := bytes.TrimSpace(patch.Raw)
	if len(raw) == 0 || len(raw) > 64*1024 || raw[0] != '{' || !json.Valid(raw) {
		return fmt.Errorf("settingsPatch must be a JSON object of at most 65536 bytes")
	}
	return nil
}

var evaluationPublicationID = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)

func validateEvaluationComparisons(comparisons []EvaluationComparison, stages map[string]bool) error {
	for _, comparison := range comparisons {
		if comparison.Reason == "" {
			return fmt.Errorf("comparison reason is required")
		}
		for _, ref := range []EvaluationComparisonReference{comparison.Left, comparison.Right} {
			if (ref.StageID == "") == (ref.Historical == nil) {
				return fmt.Errorf("exactly one comparison reference required")
			}
			if ref.StageID != "" && !stages[ref.StageID] {
				return fmt.Errorf("comparison references missing stage %q", ref.StageID)
			}
			if h := ref.Historical; h != nil && (!evaluationPublicationID.MatchString(h.PublicationID) || h.StageID == "") {
				return fmt.Errorf("invalid historical comparison")
			}
		}
	}
	return nil
}

func validEvaluationObject(ref EvaluationObjectReference) bool {
	return ref.Name != "" && ref.UID != ""
}
func validateEvaluationContext(target TargetIdentity, harness EvaluationImage, generator Generator, power PowerMeasurement, judgment Judgment) error {
	if len(target.Components) == 0 {
		return fmt.Errorf("target components required")
	}
	images := []EvaluationImage{harness, generator.EvaluationImage}
	names := map[string]bool{}
	for _, c := range target.Components {
		if c.Name == "" || names[c.Name] || c.Resource.APIVersion == "" || c.Resource.Kind == "" || c.Resource.Namespace == "" || c.Resource.Name == "" {
			return fmt.Errorf("invalid or duplicate target component %q", c.Name)
		}
		names[c.Name] = true
	}
	for _, image := range images {
		if !strings.Contains(image.Image, "@sha256:") || image.SourceSHA == "" {
			return fmt.Errorf("images require @sha256: and sourceSHA")
		}
	}
	if err := validateEvaluationGenerator(generator); err != nil {
		return err
	}
	if err := validateEvaluationPower(power); err != nil {
		return err
	}
	q, e := judgment.GeneratorQuality, judgment.EnergyCoverage
	if q.LatenessToleranceNanos < 0 || !finiteNonNegative(q.MaxExceedingFraction) || q.MaxExceedingFraction > 1 || !finiteNonNegative(e.MaxGapFraction) || e.MaxGapFraction > 1 || !finitePositive(e.MaxSingleGapSeconds) {
		return fmt.Errorf("invalid judgment numeric ranges")
	}
	return nil
}

func validateEvaluationGenerator(generator Generator) error {
	if len(validation.IsDNS1123Subdomain(generator.NodeName)) != 0 {
		return fmt.Errorf("valid generator nodeName required")
	}
	if len(generator.Shards) == 0 || generator.Barrier.MaxStartSkewNanos < 0 || generator.Barrier.MinimumReadySamples < 1 {
		return fmt.Errorf("invalid generator shards or barrier")
	}
	for name, n := range generator.Shards {
		if name == "" || n < 1 {
			return fmt.Errorf("invalid generator shards")
		}
	}
	for _, resources := range []corev1.ResourceList{generator.Resources.Requests, generator.Resources.Limits} {
		if _, present := resources[corev1.ResourceCPU]; present {
			return fmt.Errorf("generator CPU resources are not supported")
		}
		for _, quantity := range resources {
			if quantity.Sign() < 0 {
				return fmt.Errorf("negative generator resources")
			}
		}
	}
	return nil
}

// EvaluationWorkloadCoverage is the one rule binding a stage to its LoadScenario: the
// generator shards and the evaluation config's operational workloads name exactly the
// scenario's workloads. The controller enforces it when it freezes inputs; preflight
// reports the same rule.
func EvaluationWorkloadCoverage(scenario LoadScenarioSpec, shards map[string]int32, config map[string]string) error {
	var operational struct {
		Workloads map[string]json.RawMessage `json:"workloads"`
	}
	if err := json.Unmarshal([]byte(config["operational"]), &operational); err != nil {
		return fmt.Errorf("%s: operational evaluation config: %v", EvaluationReasonFrozenInputInvalid, err)
	}
	names := make(map[string]bool, len(scenario.Workloads))
	for _, workload := range scenario.Workloads {
		names[workload.Name] = true
	}
	if !namesExactly(names, shards) {
		return fmt.Errorf("%s: declared generator shard coverage: shards must name exactly the LoadScenario workloads", EvaluationReasonFrozenInputInvalid)
	}
	if !namesExactly(names, operational.Workloads) {
		return fmt.Errorf("%s: operational workload coverage: operational workloads must name exactly the LoadScenario workloads", EvaluationReasonFrozenInputInvalid)
	}
	return nil
}

func namesExactly[V any](names map[string]bool, keyed map[string]V) bool {
	if len(keyed) != len(names) {
		return false
	}
	for name := range keyed {
		if !names[name] {
			return false
		}
	}
	return true
}

func validateEvaluationPower(power PowerMeasurement) error {
	if len(power.Meters) == 0 || len(power.Nodes) == 0 || power.Series == "" || power.Sampling.IntervalNanos < 1 {
		return fmt.Errorf("invalid power measurement")
	}
	names := map[string]bool{}
	for _, meter := range power.Meters {
		if meter.ID == "" || meter.SourceIdentity == "" || names[meter.ID] {
			return fmt.Errorf("invalid or duplicate meter")
		}
		names[meter.ID] = true
	}
	names = map[string]bool{}
	for _, node := range power.Nodes {
		if !validEvaluationObject(node) || names[node.Name] {
			return fmt.Errorf("invalid or duplicate power node")
		}
		names[node.Name] = true
	}
	return nil
}

// EvaluationFrozenConfig retains non-secret operational ConfigMap data verbatim.
// Harness image and source identity belong only to spec.
type EvaluationFrozenConfig struct {
	EvaluationObjectReference `json:",inline"`
	ResourceVersion           string            `json:"resourceVersion"`
	Values                    map[string]string `json:"values"`
}

type EvaluationReleaseRecord struct {
	OriginalAnnotation string                      `json:"originalAnnotation"`
	ProcessedAt        metav1.Time                 `json:"processedAt"`
	EvidenceRef        EvaluationEvidenceReference `json:"evidenceRef"`
}

type EvaluationChildReference struct {
	StageID                   string `json:"stageID"`
	EvaluationObjectReference `json:",inline"`
}

type EvaluationProcedure struct {
	// +kubebuilder:validation:Enum=prepare;factor;stage;judge;cleanup-jobs;finish;publish
	Step string `json:"step"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// +optional
	JobRef *EvaluationObjectReference `json:"jobRef,omitempty"`
	// +optional
	PodRef *EvaluationObjectReference `json:"podRef,omitempty"`
	// +optional
	EvidenceRef *EvaluationEvidenceReference `json:"evidenceRef,omitempty"`
	// +optional
	MeasurementVerdict MeasurementVerdict `json:"measurementVerdict,omitempty"`
}

type EvaluationRunStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=5
	// +optional
	// +kubebuilder:validation:XValidation:rule="self.all(c, c.type in ['PoolAcquired', 'ExecutionComplete', 'MeasurementPassed', 'CleanupComplete', 'Published'])",message="unsupported EvaluationRun condition"
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// +optional
	CurrentStage string `json:"currentStage,omitempty"`
	// +optional
	CurrentProcedure string `json:"currentProcedure,omitempty"`
	// +optional
	FrozenConfig *EvaluationFrozenConfig `json:"frozenConfig,omitempty"`
	// +optional
	Workspace *EvaluationObjectReference `json:"workspace,omitempty"`
	// +optional
	InputSnapshotRef *EvaluationEvidenceReference `json:"inputSnapshotRef,omitempty"`
	// +optional
	InputConfigMapRef *EvaluationObjectReference `json:"inputConfigMapRef,omitempty"`
	// +optional
	FrozenConfigMapRef *EvaluationObjectReference `json:"frozenConfigMapRef,omitempty"`
	// +listType=map
	// +listMapKey=stageID
	// +optional
	ChildRefs []EvaluationChildReference `json:"childRefs,omitempty"`
	// Procedures contains only prepare/finish/publish, never child procedures.
	// +listType=map
	// +listMapKey=step
	// +optional
	// +kubebuilder:validation:XValidation:rule="self.all(p, p.step in ['prepare', 'finish', 'publish'])",message="parent procedures must be global"
	Procedures []EvaluationProcedure `json:"procedures,omitempty"`
	// +optional
	MeasurementVerdict MeasurementVerdict `json:"measurementVerdict,omitempty"`
	// +optional
	FinalEvidenceRef *EvaluationEvidenceReference `json:"finalEvidenceRef,omitempty"`
	// +optional
	OccupyingRunUID types.UID `json:"occupyingRunUID,omitempty"`
	// +optional
	ResidualStateRef *EvaluationEvidenceReference `json:"residualStateRef,omitempty"`
	// +optional
	ReleaseRecords []EvaluationReleaseRecord `json:"releaseRecords,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=evalrun
// +kubebuilder:printcolumn:name="Pool",type=string,JSONPath=`.status.conditions[?(@.type=="PoolAcquired")].status`
// +kubebuilder:printcolumn:name="Execution",type=string,JSONPath=`.status.conditions[?(@.type=="ExecutionComplete")].status`
// +kubebuilder:printcolumn:name="Measurement",type=string,JSONPath=`.status.measurementVerdict`
// +kubebuilder:printcolumn:name="Cleanup",type=string,JSONPath=`.status.conditions[?(@.type=="CleanupComplete")].status`
// +kubebuilder:printcolumn:name="Published",type=string,JSONPath=`.status.conditions[?(@.type=="Published")].status`
// +kubebuilder:printcolumn:name="Stage",type=string,JSONPath=`.status.currentStage`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type EvaluationRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              EvaluationRunSpec `json:"spec"`
	// +optional
	Status EvaluationRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type EvaluationRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EvaluationRun `json:"items"`
}

func init() { SchemeBuilder.Register(&EvaluationRun{}, &EvaluationRunList{}) }
