package queryapi

import (
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// EvidenceReadError describes an evidence dependency failure, never a transport
// failure between ChillServer and this service.
type EvidenceReadError struct {
	Code   string `json:"code"`
	Status int    `json:"status"`
}

type EvidencePassportState struct {
	APIVersion  string                       `json:"apiVersion"`
	Kind        string                       `json:"kind"`
	ReadAt      time.Time                    `json:"readAt"`
	Code        string                       `json:"code,omitempty"`
	Reason      string                       `json:"reason,omitempty"`
	Run         EvidencePassportRun          `json:"run"`
	Assessment  MeasurementAssessment        `json:"assessment"`
	Evidence    *EvidencePassportLineage     `json:"evidence"`
	Measurement *EvidencePassportMeasurement `json:"measurement"`
}

func BuildEvidencePassportState(snapshot EvidencePassportSnapshot, code, reason string) EvidencePassportState {
	run := snapshot.Run
	return EvidencePassportState{
		APIVersion: EvidencePassportAPIVersion, Kind: "EvidencePassport", ReadAt: snapshot.ReadAt.UTC(),
		Code: code, Reason: reason,
		Run: EvidencePassportRun{Namespace: run.Namespace, Name: run.Name, UID: string(run.UID),
			ResourceVersion: run.ResourceVersion, Generation: run.Generation, Spec: run.Spec,
			Conditions: run.Status.Conditions, Job: run.Status.Job, ActualExecution: run.Status.ObservedExecution},
		Assessment: AssessProfilingRun(run),
	}
}

func EvidencePassportPending(snapshot EvidencePassportSnapshot) bool {
	return snapshot.Run.Name != "" && snapshot.Run.Status.Evidence == nil &&
		AssessProfilingRun(snapshot.Run).DisplayStatus == MeasurementRunning
}

type BatchEnergyOptimizationResultError struct {
	BatchEnergyOptimizationResult
	Code          string            `json:"code"`
	Reason        string            `json:"reason,omitempty"`
	EvidenceError EvidenceReadError `json:"evidenceError"`
}

func BuildBatchEnergyOptimizationResultError(
	snapshot BatchEnergyOptimizationResultSnapshot,
	status int,
	code, reason string,
) BatchEnergyOptimizationResultError {
	task := snapshot.Campaign
	var stored *evidencestore.ObjectIdentity
	if ref := task.Status.Result; ref != nil {
		stored = &evidencestore.ObjectIdentity{Store: ref.Store, Key: ref.Key, Size: ref.Size}
	}
	assessment := AssessMeasurement(MeasurementAssessmentInput{
		Execution: &MeasurementExecution{Phase: task.Status.Phase, Generation: task.Generation,
			ObservedGeneration: task.Status.ObservedGeneration}, Attempted: true, SourcesComplete: false,
	})
	assessment.FailureSummary = MeasurementRequestFailures(task, nil, assessment)
	view := BatchEnergyOptimizationResult{
		APIVersion: BatchEnergyOptimizationResultAPIVersion, Kind: "BatchEnergyOptimizationResult", ReadAt: snapshot.ReadAt.UTC(),
		Campaign:   resultCampaign(&task),
		Assessment: assessment, ResultRows: []ResultRow{},
	}
	view.Campaign.StoredResult = stored
	return BatchEnergyOptimizationResultError{
		BatchEnergyOptimizationResult: view,
		Code:                          code, Reason: reason, EvidenceError: EvidenceReadError{Code: code, Status: status},
	}
}
