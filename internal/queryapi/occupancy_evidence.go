package queryapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
)

const (
	MeasurementVerdictAccepted = "Accepted"
	MeasurementVerdictObserved = "Observed"
	DispatchOccupancyPrefix    = "serving/dispatch-occupancy/"
	DispatchOccupancySchema    = "chill.dacs.io/dispatch-occupancy-measurement.v1alpha1"
	BurstOccupancyPrefix       = "serving/dispatch-occupancy-observations/"
	BurstOccupancySchema       = "chill.dacs.io/dispatch-occupancy-measurement.v1alpha2"
)

// OccupancyEvidenceProjection retains interpreted progress and private application facts.
type OccupancyEvidenceProjection struct {
	DispatchOccupancyProgress
	Assessment json.RawMessage       `json:"assessment"`
	Record     *OccupancyRecordFacts `json:"-"`
	Burst      *OccupancyRecordFacts `json:"-"`
}

// OccupancyRecordFacts contains only identities and observations consulted by queries.
type OccupancyRecordFacts struct {
	Node      MeasurementIdentity `json:"node"`
	Model     string              `json:"model"`
	PowerMode string              `json:"powerMode"`
	ProfileID string              `json:"profileID"`
	Verdict   string              `json:"verdict"`
	Estimate  *OccupancyEstimate  `json:"-"`
}

// OccupancyEvidenceRecord contains only decoded fields consulted by queries.
type OccupancyEvidenceRecord struct {
	OccupancyRecordFacts
	SchemaVersion      string                               `json:"schemaVersion"`
	Batch              int                                  `json:"batch"`
	Conditions         api.DispatchOccupancyMeasurementSpec `json:"conditions"`
	Window             OccupancyMeasurementWindow           `json:"window"`
	PerRequestNanos    int64                                `json:"perRequestNanos"`
	Throughput         float64                              `json:"throughput"`
	IdleFraction       float64                              `json:"idleFraction"`
	Cycles             int                                  `json:"cycles"`
	IdleIdentified     bool                                 `json:"idleIdentified"`
	PrecisionTargetMet *bool                                `json:"precisionTargetMet,omitempty"`
	Reasons            []string                             `json:"reasons,omitempty"`
}

// DecodedOccupancyEvidence retains the original wire decoder's branch and scalar presence.
type DecodedOccupancyEvidence struct {
	Burst              bool
	HasPerRequestNanos bool
	Record             OccupancyEvidenceRecord
}

// OccupancyEvidenceDecoder preserves source wire validation without coupling queries to serving.
type OccupancyEvidenceDecoder func(api.DispatchOccupancyMeasurement, []byte) (DecodedOccupancyEvidence, error)

func finiteOccupancyValue(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

func OccupancyProgress(cr api.DispatchOccupancyMeasurement) DispatchOccupancyProgress {
	out := DispatchOccupancyProgress{
		Name: cr.Name, UID: string(cr.UID), BatchEnergyOptimization: cr.Spec.BatchEnergyOptimization,
		Workload: cr.Spec.Workload, Batch: int(cr.Spec.Batch), ProfileID: cr.Spec.Conditions.ProfileID,
		Reason: cr.Status.Reason, CreatedAt: cr.CreationTimestamp.Time,
		MinimumPairs: int(cr.Spec.MinimumPairs),
	}
	if cr.Status.Phase != "" {
		phase := string(cr.Status.Phase)
		out.Phase = &phase
	}
	if ref := cr.Status.Result; ref != nil {
		out.Evidence = &OccupancyEvidence{Store: ref.Store, Key: ref.Key, Size: ref.Size}
	}
	if cr.Status.PerRequestNanos != nil && (cr.Status.Result == nil ||
		!strings.HasPrefix(cr.Status.Result.Key, BurstOccupancyPrefix)) {
		out.D = &OccupancyValue{PerRequestNanos: cr.Status.PerRequestNanos}
		if value, err := strconv.ParseFloat(cr.Status.Throughput, 64); err == nil {
			out.D.Throughput = finiteOccupancyValue(value)
		}
	}
	return out
}

func projectBurstOccupancy(
	cr api.DispatchOccupancyMeasurement, projection OccupancyEvidenceProjection,
	input MeasurementAssessmentInput, record OccupancyEvidenceRecord, decodeErr error,
) (OccupancyEvidenceProjection, error) {
	projection.D = nil
	if decodeErr != nil {
		input.Problems = append(input.Problems, MeasurementReason{
			Code: "OccupancyEvidenceInvalid", Message: "Stored burst evidence cannot be decoded",
			SourceIDs: []string{cr.Status.Result.Key},
		})
	} else {
		input.Available = true
		if record.SchemaVersion == BurstOccupancySchema {
			input.Observed = record.Verdict == MeasurementVerdictObserved
			projection.Burst = &record.OccupancyRecordFacts
			projection.Burst.Estimate = record.Estimate
			projection.SchemaVersion, projection.Verdict = record.SchemaVersion, record.Verdict
			projection.ProfileID, projection.Estimate = record.ProfileID, record.Estimate
			if record.Estimate != nil {
				window := record.Estimate.Window
				projection.MeasurementWindow = &OccupancyMeasurementWindow{Start: window.Start, End: window.End}
				input.Facts = append(input.Facts, fmt.Sprintf(
					"Observed signed ns/request estimate: %d adjacent pairs; minimumPairs: %d",
					record.Estimate.Pairs, record.Conditions.MinimumPairs))
			}
			for _, reason := range record.Reasons {
				input.Reasons = append(input.Reasons, MeasurementReason{
					Code: reason, Message: reason, SourceIDs: []string{cr.Status.Result.Key},
				})
			}
		} else {
			input.Problems = append(input.Problems, MeasurementReason{
				Code: "OccupancyEvidenceInvalid", Message: "Unsupported stored burst evidence schema",
				SourceIDs: []string{cr.Status.Result.Key},
			})
		}
	}
	var err error
	projection.Assessment, err = json.Marshal(AssessMeasurement(input))
	return projection, err
}

func InterpretDispatchOccupancy(
	cr api.DispatchOccupancyMeasurement,
	decoded DecodedOccupancyEvidence,
	decodeErr, readErr error,
) (OccupancyEvidenceProjection, error) {
	projection := OccupancyEvidenceProjection{DispatchOccupancyProgress: OccupancyProgress(cr)}
	input := MeasurementAssessmentInput{
		Execution:        &MeasurementExecution{Phase: string(cr.Status.Phase), Generation: cr.Generation},
		ExecutionCurrent: true, Attempted: true,
		Facts: []string{"DispatchOccupancyMeasurement has immutable spec and no observedGeneration status field"},
	}
	if cr.Status.Reason != "" {
		input.Reasons = append(input.Reasons, MeasurementReason{
			Code: string(cr.Status.Phase), Message: cr.Status.Reason, SourceIDs: []string{string(cr.UID)},
		})
	}
	if cr.Status.Phase == api.MeasurementPhaseInsufficient {
		input.Problems = append(input.Problems, MeasurementReason{
			Code: "OccupancyEvidenceInsufficient", Message: cr.Status.Reason, SourceIDs: []string{string(cr.UID)},
		})
	}
	if cr.Status.PerRequestNanos != nil && *cr.Status.PerRequestNanos < 0 {
		input.Problems = append(input.Problems, MeasurementReason{
			Code: "OccupancyNegativeD", Message: "Signed negative d requires review; planner consumes max(d,0)",
			SourceIDs: []string{string(cr.UID)},
		})
	}
	if ref := cr.Status.Result; ref != nil && ref.Key != "" {
		err := readErr
		if err != nil {
			code, message := "OccupancyEvidenceUnavailable", "분배 점유 근거를 읽지 못했습니다. 저장소 연결과 읽기 권한을 확인하세요."
			if errors.Is(err, evidencestore.ErrNotFound) {
				code, message = "OccupancyEvidenceMissing", "분배 점유 근거 파일이 없습니다. 원래 결과 위치와 보존 상태를 확인하세요."
			}
			input.Problems = append(input.Problems, MeasurementReason{
				Code: code, Message: message, SourceIDs: []string{ref.Key},
			})
			projection.Assessment, err = json.Marshal(AssessMeasurement(input))
			return projection, err
		}
		if decoded.Burst {
			return projectBurstOccupancy(cr, projection, input, decoded.Record, decodeErr)
		}
		record := decoded.Record
		if decodeErr != nil {
			input.Problems = append(input.Problems, MeasurementReason{
				Code: "OccupancyEvidenceInvalid", Message: "분배 점유 근거 파일을 해석하지 못했습니다. 보존된 원본을 확인하세요.", SourceIDs: []string{ref.Key},
			})
			projection.Assessment, err = json.Marshal(AssessMeasurement(input))
			return projection, err
		}
		if !decoded.HasPerRequestNanos {
			projection.D = nil
		}
		input.Available = true
		input.Facts = append(input.Facts, "Retained occupancy: "+ref.Store+"/"+ref.Key)
		matches := decoded.HasPerRequestNanos && record.SchemaVersion == DispatchOccupancySchema
		input.Accepted = matches && record.Verdict == MeasurementVerdictAccepted
		if matches {
			projection.Record = &record.OccupancyRecordFacts
			projection.SchemaVersion, projection.Verdict = record.SchemaVersion, record.Verdict
			projection.ProfileID = record.ProfileID
			projection.D = &OccupancyValue{
				PerRequestNanos: &record.PerRequestNanos, Throughput: finiteOccupancyValue(record.Throughput),
				IdleFraction: finiteOccupancyValue(record.IdleFraction), Verdict: record.Verdict,
			}
			if !record.Window.Start.IsZero() && !record.Window.End.IsZero() {
				projection.MeasurementWindow = &OccupancyMeasurementWindow{
					Start: record.Window.Start, End: record.Window.End,
				}
			}
		}
		if !matches {
			input.Problems = append(input.Problems, MeasurementReason{
				Code: "OccupancyEvidenceInvalid", Message: "Stored occupancy lacks a supported schema or scalar",
				SourceIDs: []string{ref.Key},
			})
		}
		if record.PerRequestNanos < 0 || math.IsNaN(record.Throughput) || math.IsInf(record.Throughput, 0) {
			input.Problems = append(input.Problems, MeasurementReason{
				Code:      "OccupancyValueProblem",
				Message:   "Signed negative d or nonfinite throughput requires review; stored values and verdict are unchanged",
				SourceIDs: []string{ref.Key},
			})
		}
		for _, reason := range record.Reasons {
			input.Reasons = append(input.Reasons, MeasurementReason{
				Code: reason, Message: reason, SourceIDs: []string{ref.Key},
			})
		}
		if decoded.HasPerRequestNanos {
			input.Facts = append(input.Facts,
				fmt.Sprintf("Stored signed d: %d ns/request; planner consumes max(d,0)", record.PerRequestNanos),
				fmt.Sprintf("Stored physical cycles: %d; idle identified: %t", record.Cycles, record.IdleIdentified),
			)
		}
		if record.PrecisionTargetMet != nil {
			input.Facts = append(input.Facts,
				fmt.Sprintf("Stored reporting precision target met: %t", *record.PrecisionTargetMet),
			)
		}
	}
	if invalidOccupancyThroughput(cr.Status.Throughput) {
		input.Problems = append(input.Problems, MeasurementReason{
			Code: "OccupancyThroughputInvalid", Message: "Recorded throughput is not finite",
			SourceIDs: []string{string(cr.UID)},
		})
	}
	var err error
	projection.Assessment, err = json.Marshal(AssessMeasurement(input))
	return projection, err
}

func invalidOccupancyThroughput(throughput string) bool {
	value, err := strconv.ParseFloat(throughput, 64)
	return throughput != "" && (err != nil || math.IsNaN(value) || math.IsInf(value, 0))
}
