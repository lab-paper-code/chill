package queryapi

import (
	"encoding/json"
	"slices"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Result formats a measurement result view declares.
const (
	// ResultFormatSession is a stored measurement-session result.
	ResultFormatSession = "Session"
	// ResultFormatPrevious is a stored result of the per-batch Run method the
	// session replaced. Its trials are not read; the request must be measured again.
	ResultFormatPrevious = "PreviousFormat"
)

const resultFormatPreviousMessage = "This result was stored by the previous measurement method and cannot be read; delete the request and measure again."

// SessionResultView is a session result with its large arrays replaced by
// counts and the summaries a reader needs computed here.
type SessionResultView struct {
	SchemaVersion string                             `json:"schemaVersion"`
	Condition     profilederivation.SessionCondition `json:"condition"`
	Settings      profilederivation.SessionSettings  `json:"settings"`
	Power         SessionPowerView                   `json:"power"`
	Batches       []SessionBatchView                 `json:"batches"`
	Stop          *profilederivation.SessionStop     `json:"stop,omitempty"`
	MeterSource   json.RawMessage                    `json:"meterSource,omitempty"`
}

// SessionParameterView is a derived value with its unit. Value is present only
// when Status is Identified; Range may be present either way.
type SessionParameterView struct {
	Status profilederivation.ParameterStatus `json:"status"`
	Value  *float64                          `json:"value,omitempty"`
	Range  *[2]float64                       `json:"range,omitempty"`
	Reason string                            `json:"reason,omitempty"`
	Unit   string                            `json:"unit"`
}

type SessionPowerView struct {
	IdleWatts            SessionParameterView `json:"idleWatts"`
	AwakeStepWatts       SessionParameterView `json:"awakeStepWatts"`
	HoldSeconds          SessionParameterView `json:"holdSeconds"`
	MeterIntervalSeconds *float64             `json:"meterIntervalSeconds,omitempty"`
	SettledAtSeconds     *float64             `json:"settledAtSeconds,omitempty"`
	// PostLoad is the stored (seconds after load stopped, watts) series, not
	// reduced: one sample per meter interval over the post-load window.
	PostLoad            [][2]float64 `json:"postLoad"`
	PostLoadSampleCount int          `json:"postLoadSampleCount"`
}

type SessionBatchView struct {
	Batch               int                  `json:"batch"`
	ServiceRecordCount  int                  `json:"serviceRecordCount"`
	SaturationInputRate *float64             `json:"saturationInputRate,omitempty"`
	JoulesPerInput      SessionParameterView `json:"joulesPerInput"`
	PeakMemoryBytes     int64                `json:"peakMemoryBytes,omitempty"`
	MemoryKind          string               `json:"memoryKind"`
	DispatchOccupancy   SessionOccupancyView `json:"dispatchOccupancy"`
	PrepareCostKey      string               `json:"prepareCostKey"`
	RemoveCostKey       string               `json:"removeCostKey"`
	// Levels are the stored levels as measured, in the order they ran.
	Levels []profilederivation.SessionLevel `json:"levels"`
}

// SessionOccupancyView summarizes a batch's dispatch occupancy samples.
type SessionOccupancyView struct {
	Count       int      `json:"count"`
	MedianNanos *float64 `json:"medianNanos,omitempty"`
}

func BuildSessionResultView(result profilederivation.SessionResult) SessionResultView {
	view := SessionResultView{
		SchemaVersion: result.SchemaVersion, Condition: result.Condition, Settings: result.Settings,
		Stop: result.Stop, MeterSource: result.MeterSource,
		Power: SessionPowerView{
			IdleWatts:            parameterView(result.Power.IdleWatts, "W"),
			AwakeStepWatts:       parameterView(result.Power.AwakeStepWatts, "W"),
			HoldSeconds:          parameterView(result.Power.HoldSeconds, "s"),
			MeterIntervalSeconds: result.Power.MeterIntervalSeconds,
			SettledAtSeconds:     result.Power.SettledAtSeconds,
			PostLoad:             result.Power.PostLoad,
			PostLoadSampleCount:  len(result.Power.PostLoad),
		},
		Batches: make([]SessionBatchView, len(result.Batches)),
	}
	if view.Power.PostLoad == nil {
		view.Power.PostLoad = [][2]float64{}
	}
	for i, batch := range result.Batches {
		view.Batches[i] = SessionBatchView{
			Batch: batch.Batch, ServiceRecordCount: len(batch.ServiceNanos),
			SaturationInputRate: batch.SaturationInputRate,
			JoulesPerInput:      parameterView(batch.JoulesPerInput, "J"),
			PeakMemoryBytes:     batch.PeakMemoryBytes, MemoryKind: batch.MemoryKind,
			DispatchOccupancy: SessionOccupancyView{
				Count: len(batch.DispatchOccupancyNanos), MedianNanos: median(batch.DispatchOccupancyNanos),
			},
			PrepareCostKey: batch.PrepareCostKey, RemoveCostKey: batch.RemoveCostKey,
			Levels: batch.Levels,
		}
		if view.Batches[i].Levels == nil {
			view.Batches[i].Levels = []profilederivation.SessionLevel{}
		}
	}
	return view
}

func parameterView(parameter profilederivation.SessionParameter, unit string) SessionParameterView {
	view := SessionParameterView{Status: parameter.Status(), Reason: parameter.Reason(), Unit: unit}
	if value, ok := parameter.Value(); ok {
		view.Value = &value
	}
	if bounds, ok := parameter.Range(); ok {
		view.Range = &bounds
	}
	return view
}

// median is the empirical median, the mean of the middle pair for an even count.
func median(values []float64) *float64 {
	if len(values) < 2 {
		return nil
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	middle := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		middle = (sorted[len(sorted)/2-1] + middle) / 2
	}
	return &middle
}

// MeasurementReasonSentence is the message of the request's current
// MeasurementComplete condition: the session's error or stop sentence.
func MeasurementReasonSentence(task api.BatchEnergyOptimization) string {
	for _, condition := range task.Status.Conditions {
		if condition.Type == "MeasurementComplete" && condition.ObservedGeneration == task.Generation {
			return condition.Message
		}
	}
	return ""
}

// resultCampaign is the request identity and status every result view carries.
func resultCampaign(task *api.BatchEnergyOptimization) BatchEnergyOptimizationResultCampaign {
	return BatchEnergyOptimizationResultCampaign{
		Namespace: task.Namespace, Name: task.Name, UID: string(task.UID),
		ResourceVersion: task.ResourceVersion, Generation: task.Generation,
		Spec:  task.Spec,
		Phase: task.Status.Phase, Outcome: task.Status.Outcome,
		CompletedTrials:  task.Status.CompletedTrials,
		CompletedBatches: task.Status.CompletedBatches,
		Reason:           MeasurementReasonSentence(*task),
		Conditions:       append([]metav1.Condition(nil), task.Status.Conditions...),
	}
}

// previousFormat reports a published result the session reader does not
// decode: every result of a request without session settings.
func previousFormat(task *api.BatchEnergyOptimization) bool {
	return task.Status.Result != nil && task.Spec.Session == nil
}

// sessionFailureLabels name a session outcome in a failure summary.
var sessionFailureLabels = map[string]string{
	"Failed":                 "실패",
	"ChillServerRestart":     "서버 재시작으로 중단",
	"SessionSettingsMissing": "설정 없음",
	"MeterUndeclared":        "전력계 미선언",
	"SessionUnresolved":      "준비 정보 해석 실패",
}

// sessionRequestFailures is a failed session's one failure: its outcome and
// the MeasurementComplete sentence. A session owns no Run to inspect.
func sessionRequestFailures(task api.BatchEnergyOptimization, assessment MeasurementAssessment) []MeasurementFailure {
	if assessment.DisplayStatus != MeasurementProblem {
		return []MeasurementFailure{}
	}
	label, known := sessionFailureLabels[task.Status.Outcome]
	if !known {
		label = "실패"
	}
	return []MeasurementFailure{{
		RequestID: CoverageRequestID(task), Code: task.Status.Outcome, Label: label,
		Message: MeasurementReasonSentence(task), Relation: "unresolved",
	}}
}
