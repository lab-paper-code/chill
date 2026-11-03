package main

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

const measurementVerdictObserved = "Observed"

type dispatchOccupancyProjection struct {
	httpapi.MeasurementProjection
	queryapi.DispatchOccupancyProgress
	record *servingkube.DispatchOccupancyRecord
	burst  *servingkube.BurstOccupancyRecord
}
type dispatchOccupancyProjectionList struct {
	Items []dispatchOccupancyProjection `json:"items"`
}

func finiteOccupancyValue(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

func occupancyProgress(cr api.DispatchOccupancyMeasurement) queryapi.DispatchOccupancyProgress {
	out := queryapi.DispatchOccupancyProgress{
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
		out.Evidence = &queryapi.OccupancyEvidence{Store: ref.Store, Key: ref.Key, Size: ref.Size}
	}
	if cr.Status.PerRequestNanos != nil && (cr.Status.Result == nil ||
		!strings.HasPrefix(cr.Status.Result.Key, servingkube.BurstOccupancyPrefix)) {
		out.D = &queryapi.OccupancyValue{PerRequestNanos: cr.Status.PerRequestNanos}
		if value, err := strconv.ParseFloat(cr.Status.Throughput, 64); err == nil {
			out.D.Throughput = finiteOccupancyValue(value)
		}
	}
	return out
}

func projectBurstOccupancy(
	cr api.DispatchOccupancyMeasurement, projection dispatchOccupancyProjection,
	input queryapi.MeasurementAssessmentInput, raw []byte,
) (dispatchOccupancyProjection, error) {
	projection.D = nil
	var record servingkube.BurstOccupancyRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		input.Problems = append(input.Problems, queryapi.MeasurementReason{
			Code: "OccupancyEvidenceInvalid", Message: "Stored burst evidence cannot be decoded",
			SourceIDs: []string{cr.Status.Result.Key},
		})
	} else {
		input.Available = true
		matches := record.SchemaVersion == servingkube.BurstOccupancySchema &&
			record.Node.Name == cr.Spec.Node && record.Node.UID != "" &&
			cr.Status.Result.Key == servingkube.BurstOccupancyPrefix+record.Node.UID+"/"+string(cr.UID)+".json" &&
			reflect.DeepEqual(record.Conditions, cr.Spec) && record.Batch == int(cr.Spec.Batch) &&
			(cr.Spec.Conditions.ProfileID == "" || record.ProfileID == cr.Spec.Conditions.ProfileID) &&
			(record.Verdict == measurementVerdictObserved || record.Verdict == "Insufficient")
		if estimate := record.Estimate; estimate != nil {
			matches = matches && estimate.Node == record.Node && estimate.Model == record.Model &&
				estimate.PowerMode == record.PowerMode && estimate.Profile.ProfileID == record.ProfileID &&
				estimate.Batch == record.Batch
		}
		if matches {
			input.Observed = record.Verdict == measurementVerdictObserved
			projection.burst = &record
			projection.SchemaVersion, projection.Verdict = record.SchemaVersion, record.Verdict
			projection.ProfileID, projection.Estimate = record.ProfileID, record.Estimate
			if record.Estimate != nil {
				window := record.Estimate.Window
				projection.MeasurementWindow = &queryapi.OccupancyMeasurementWindow{Start: window.Start, End: window.End}
				input.Facts = append(input.Facts, fmt.Sprintf(
					"Observed signed ns/request estimate: %d adjacent pairs; minimumPairs: %d",
					record.Estimate.Pairs, record.Conditions.MinimumPairs))
			}
			for _, reason := range record.Reasons {
				input.Reasons = append(input.Reasons, queryapi.MeasurementReason{
					Code: reason, Message: reason, SourceIDs: []string{cr.Status.Result.Key},
				})
			}
		} else {
			input.Problems = append(input.Problems, queryapi.MeasurementReason{
				Code: "OccupancyEvidenceMismatch", Message: "Stored burst evidence does not match this request",
				SourceIDs: []string{cr.Status.Result.Key},
			})
		}
	}
	var err error
	projection.Assessment, err = json.Marshal(queryapi.AssessMeasurement(input))
	return projection, err
}
