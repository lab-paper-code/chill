package queryapi

import (
	"errors"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BatchEnergyOptimizationResultSnapshot joins one BatchEnergyOptimization control-plane record
// with the immutable terminal result it references. It is a read-time
// projection, not a second persisted copy of either source.
type BatchEnergyOptimizationResultSnapshot struct {
	ReadAt   time.Time
	Campaign edgev1alpha1.BatchEnergyOptimization
	// SchedulerUsed is nil when the accepted catalog could not be read.
	// It is a read-time selection fact, not part of the sealed result.
	SchedulerUsed *bool
	// Session is the decoded session result.
	Session *profilederivation.SessionResult
}

const BatchEnergyOptimizationResultAPIVersion = "console.chill.dacs.io/v1alpha1"

// BatchEnergyOptimizationResult exposes the request's progress and failure,
// with the session result once the request publishes its reference.
type BatchEnergyOptimizationResult struct {
	APIVersion string                                `json:"apiVersion"`
	Kind       string                                `json:"kind"`
	ReadAt     time.Time                             `json:"readAt"`
	Campaign   BatchEnergyOptimizationResultCampaign `json:"campaign"`
	Assessment MeasurementAssessment                 `json:"assessment"`
	ResultRows []ResultRow                           `json:"resultRows"`
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

func BuildBatchEnergyOptimizationResult(snapshot BatchEnergyOptimizationResultSnapshot) (BatchEnergyOptimizationResult, error) {
	if snapshot.Session != nil {
		return buildSessionResult(snapshot)
	}
	campaign := snapshot.Campaign.DeepCopy()
	view := BatchEnergyOptimizationResult{
		APIVersion: BatchEnergyOptimizationResultAPIVersion,
		Kind:       "BatchEnergyOptimizationResult",
		ReadAt:     snapshot.ReadAt.UTC(),
		Campaign:   resultCampaign(campaign),
		ResultRows: []ResultRow{},
		Assessment: AssessMeasurement(MeasurementAssessmentInput{
			Execution:     &MeasurementExecution{Phase: campaign.Status.Phase, Generation: campaign.Generation, ObservedGeneration: campaign.Status.ObservedGeneration},
			SchedulerUsed: snapshot.SchedulerUsed, Attempted: true, SourcesComplete: true,
		}),
	}
	if reference := campaign.Status.Result; reference != nil {
		view.Campaign.StoredResult = &evidencestore.ObjectIdentity{Store: reference.Store, Key: reference.Key, Size: reference.Size}
	}
	if previousFormat(campaign) {
		view.ResultFormat, view.ResultFormatMessage = ResultFormatPrevious, resultFormatPreviousMessage
	}
	view.Assessment.FailureSummary = MeasurementRequestFailures(*campaign, nil, view.Assessment)
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
