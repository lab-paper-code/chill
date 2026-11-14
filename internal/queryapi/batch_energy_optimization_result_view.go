package queryapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BatchEnergyOptimizationResultSnapshot joins one BatchEnergyOptimization control-plane record
// with the immutable terminal result it references. It is a read-time
// projection, not a second persisted copy of either source.
type BatchEnergyOptimizationResultSnapshot struct {
	ReadAt                 time.Time
	Campaign               edgev1alpha1.BatchEnergyOptimization
	Result                 batchenergyoptimization.ResultRecord
	Statistics             *BatchEnergyOptimizationStatistics
	StatisticsReason       *string
	MeasurementFacts       *profilederivation.MeasurementFacts
	MeasurementFactsReason *string
	FailedRuns             []edgev1alpha1.ProfilingRun
	// SchedulerUsed is nil when the accepted catalog could not be read.
	// It is a read-time selection fact, not part of the sealed result.
	SchedulerUsed *bool
	// Session is the decoded session result; the fields above stay empty.
	Session *profilederivation.SessionResult
}

const BatchEnergyOptimizationResultAPIVersion = "console.chill.dacs.io/v1alpha1"

// BatchEnergyOptimizationResult exposes actual campaign progress and failure,
// with the sealed result only after the campaign publishes its reference.
type BatchEnergyOptimizationResult struct {
	APIVersion             string                                `json:"apiVersion"`
	Kind                   string                                `json:"kind"`
	ReadAt                 time.Time                             `json:"readAt"`
	Campaign               BatchEnergyOptimizationResultCampaign `json:"campaign"`
	Result                 *BatchEnergyOptimizationResultRecord  `json:"result"`
	Statistics             *BatchEnergyOptimizationStatistics    `json:"statistics"`
	StatisticsReason       *string                               `json:"statisticsReason"`
	MeasurementFacts       *profilederivation.MeasurementFacts   `json:"measurementFacts"`
	MeasurementFactsReason *string                               `json:"measurementFactsReason"`
	Assessment             MeasurementAssessment                 `json:"assessment"`
	ResultRows             []ResultRow                           `json:"resultRows"`
	// ResultFormat is ResultFormatSession or ResultFormatPrevious once a
	// result is published, absent before.
	ResultFormat        string             `json:"resultFormat,omitempty"`
	ResultFormatMessage string             `json:"resultFormatMessage,omitempty"`
	Session             *SessionResultView `json:"session,omitempty"`
}

type BatchEnergyOptimizationResultCampaign struct {
	Namespace       string                                   `json:"namespace"`
	Name            string                                   `json:"name"`
	UID             string                                   `json:"uid"`
	ResourceVersion string                                   `json:"resourceVersion"`
	Generation      int64                                    `json:"generation"`
	Spec            edgev1alpha1.BatchEnergyOptimizationSpec `json:"spec"`
	Phase           string                                   `json:"phase,omitempty"`
	Outcome         string                                   `json:"outcome,omitempty"`
	CompletedTrials int32                                    `json:"completedTrials"`
	// CompletedBatches counts the batch sizes a session measured.
	CompletedBatches int32 `json:"completedBatches"`
	// Reason is the current MeasurementComplete condition's message.
	Reason       string                        `json:"reason,omitempty"`
	Conditions   []metav1.Condition            `json:"conditions,omitempty"`
	StoredResult *evidencestore.ObjectIdentity `json:"storedResult"`
}

// BatchEnergyOptimizationResultRecord republishes the stored record as it was sealed.
// Detail stays the measurement method's own JSON: the Query API does not decide
// what a curve looks like, so a method added later needs no change here.
type BatchEnergyOptimizationResultRecord struct {
	SchemaVersion string `json:"schemaVersion"`
	batchenergyoptimization.ResultRecordSpec
}

// BatchEnergyOptimizationStatistics is computed only while reading retained evidence.
type BatchEnergyOptimizationStatistics struct {
	Batches          []BatchEnergyOptimizationBatchStatistics `json:"batches"`
	MeasurementFacts *profilederivation.MeasurementFacts      `json:"-"`
}

type BatchEnergyOptimizationBatchStatistics struct {
	Batch                        int                               `json:"batch"`
	IncrementalWorkJoulesPerItem float64                           `json:"incrementalWorkJoulesPerItem"`
	ActiveTrialCount             int                               `json:"activeTrialCount"`
	IdleTrialCount               int                               `json:"idleTrialCount"`
	Confidence                   profilederivation.PointConfidence `json:"confidence"`
	LatencySampleCount           int                               `json:"latencySampleCount"`
	LatencyP95Milliseconds       float64                           `json:"latencyP95Milliseconds"`
	Sources                      []evidencestore.ObjectIdentity    `json:"sources"`
}

func BuildBatchEnergyOptimizationResult(snapshot BatchEnergyOptimizationResultSnapshot) (BatchEnergyOptimizationResult, error) {
	if snapshot.Session != nil {
		return buildSessionResult(snapshot)
	}
	if snapshot.Campaign.Status.Result == nil && snapshot.Result.SchemaVersion() != "" {
		return BatchEnergyOptimizationResult{}, errors.New("batch-energy-optimization snapshot has no stored result reference")
	}
	var storedResult *evidencestore.ObjectIdentity
	var result *BatchEnergyOptimizationResultRecord
	campaign := snapshot.Campaign.DeepCopy()
	statistics, reason := snapshot.Statistics, snapshot.StatisticsReason
	facts, factsReason := snapshot.MeasurementFacts, snapshot.MeasurementFactsReason
	if facts == nil && statistics != nil {
		facts = statistics.MeasurementFacts
	}
	if facts == nil && factsReason == nil {
		factsReason = reason
		if factsReason == nil {
			missing := "StatisticsNotRecorded"
			factsReason = &missing
		}
	}
	if campaign.Status.Result == nil {
		missing := "NoPublishedResult"
		if campaign.Status.Phase == string(edgev1alpha1.MeasurementPhaseFailed) {
			missing = "RequestFailed"
		}
		statistics, reason = nil, &missing
		facts, factsReason = nil, &missing
	}
	if campaign.Status.Result != nil {
		schemaVersion := snapshot.Result.SchemaVersion()
		if schemaVersion == "" {
			return BatchEnergyOptimizationResult{}, errors.New("batch-energy-optimization snapshot carries an uninitialized result record")
		}
		storedResult = &evidencestore.ObjectIdentity{
			Store: campaign.Status.Result.Store, Key: campaign.Status.Result.Key, Size: campaign.Status.Result.Size,
		}
		result = &BatchEnergyOptimizationResultRecord{
			SchemaVersion: schemaVersion, ResultRecordSpec: snapshot.Result.Spec(),
		}
	}
	accepted := result != nil && campaign.Status.Phase == string(edgev1alpha1.MeasurementPhaseComplete)
	// Discovery can seal an expected stop without completing any active batch.
	// Only retained measurement facts establish a usable discovery prefix.
	unmeasuredDiscovery := result != nil && result.Outcome == batchenergyoptimization.BatchDiscoveryComplete &&
		(facts == nil || len(facts.Batches) == 0)
	var assessmentReasons []MeasurementReason
	if unmeasuredDiscovery {
		accepted = false
		if factsReason != nil {
			assessmentReasons = append(assessmentReasons, MeasurementReason{
				Code: *factsReason, Message: "No completed active batch measurement facts are available",
				SourceIDs: []string{storedResult.Store + "/" + storedResult.Key},
			})
		}
	}
	reused := campaign.Annotations["chill.dacs.io/profile-reuse"] != ""
	rows := []ResultRow{}
	var children []MeasurementAssessment
	if facts != nil {
		used := snapshot.SchedulerUsed != nil && *snapshot.SchedulerUsed
		rows = MeasurementResultRows(*facts, campaign.Namespace+"/"+campaign.Name+"/"+string(campaign.UID), reused, used)
		for i := range rows {
			rows[i].Assessment.Scope.SchedulerUsed = snapshot.SchedulerUsed
			if accepted {
				children = append(children, rows[i].Assessment)
			}
		}
	}
	if reused {
		var reference struct {
			SourceProfileID           string  `json:"sourceProfileID"`
			MaximumRelativeDifference float64 `json:"maximumRelativeDifference"`
		}
		if err := json.Unmarshal([]byte(campaign.Annotations["chill.dacs.io/profile-reuse"]), &reference); err == nil && reference.SourceProfileID != "" {
			confirmations := len(rows)
			for i := range confirmations {
				rows[i].SourceProfileID = &reference.SourceProfileID
				rows[i].Reuse = reference
				rows = append(rows, ResultRow{
					ID:      fmt.Sprintf("%s/source-reference/%d", reference.SourceProfileID, rows[i].Batch),
					RowKind: ResultRowSourceReference, ProfileID: reference.SourceProfileID,
					SourceProfileID: &reference.SourceProfileID, Batch: rows[i].Batch, Reuse: reference,
					Condition:  map[string]string{"sourceProfileId": reference.SourceProfileID},
					Assessment: AssessMeasurement(MeasurementAssessmentInput{Reused: true}),
					Sources:    []string{reference.SourceProfileID}, MissingReasons: []string{"SourceProfileFactsNotLoaded"},
				})
			}
		}
	}
	view := BatchEnergyOptimizationResult{
		APIVersion: BatchEnergyOptimizationResultAPIVersion,
		Kind:       "BatchEnergyOptimizationResult",
		ReadAt:     snapshot.ReadAt.UTC(),
		Campaign:   resultCampaign(campaign),
		Result: result, Statistics: statistics, StatisticsReason: reason,
		MeasurementFacts: facts, MeasurementFactsReason: factsReason,
		ResultRows: rows,
		Assessment: AssessMeasurement(MeasurementAssessmentInput{
			Execution: &MeasurementExecution{Phase: campaign.Status.Phase, Generation: campaign.Generation, ObservedGeneration: campaign.Status.ObservedGeneration},
			Available: result != nil, Accepted: accepted,
			Reused: reused, SchedulerUsed: snapshot.SchedulerUsed,
			Attempted: true, SourcesComplete: true, Children: children, Reasons: assessmentReasons,
		}),
	}
	view.Campaign.StoredResult = storedResult
	if previousFormat(campaign) {
		view.ResultFormat, view.ResultFormatMessage = ResultFormatPrevious, resultFormatPreviousMessage
	}
	view.Assessment.FailureSummary = MeasurementRequestFailures(*campaign, snapshot.FailedRuns, view.Assessment)
	return view, nil
}

func buildSessionResult(snapshot BatchEnergyOptimizationResultSnapshot) (BatchEnergyOptimizationResult, error) {
	campaign := snapshot.Campaign.DeepCopy()
	reference := campaign.Status.Result
	if reference == nil {
		return BatchEnergyOptimizationResult{}, errors.New("batch-energy-optimization snapshot has no stored result reference")
	}
	session := BuildSessionResultView(*snapshot.Session)
	view := BatchEnergyOptimizationResult{
		APIVersion: BatchEnergyOptimizationResultAPIVersion, Kind: "BatchEnergyOptimizationResult",
		ReadAt: snapshot.ReadAt.UTC(), Campaign: resultCampaign(campaign),
		ResultRows: []ResultRow{}, ResultFormat: ResultFormatSession, Session: &session,
		Assessment: AssessMeasurement(MeasurementAssessmentInput{
			Execution: &MeasurementExecution{Phase: campaign.Status.Phase, Generation: campaign.Generation, ObservedGeneration: campaign.Status.ObservedGeneration},
			Available: true, Accepted: campaign.Status.Phase == string(edgev1alpha1.MeasurementPhaseComplete),
			SchedulerUsed: snapshot.SchedulerUsed, Attempted: true, SourcesComplete: true,
		}),
	}
	view.Campaign.StoredResult = &evidencestore.ObjectIdentity{Store: reference.Store, Key: reference.Key, Size: reference.Size}
	view.Assessment.FailureSummary = MeasurementRequestFailures(*campaign, nil, view.Assessment)
	return view, nil
}
