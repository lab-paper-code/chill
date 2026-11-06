package main

import (
	"context"
	"encoding/json"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	querykube "github.com/lab-paper-code/chill/internal/queryapi/kube"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

type dispatchOccupancyProjection struct {
	httpapi.MeasurementProjection
	queryapi.DispatchOccupancyProgress
}
type dispatchOccupancyProjectionList struct {
	Items []dispatchOccupancyProjection `json:"items"`
}

func (o *measurementService) projectDispatchOccupancy(
	ctx context.Context,
	cr api.DispatchOccupancyMeasurement,
) (dispatchOccupancyProjection, error) {
	projection, err := querykube.ProjectDispatchOccupancy(
		ctx,
		o.objects,
		o.opts.maxEvidenceBytes,
		cr,
		decodeOccupancyEvidence,
	)
	return dispatchOccupancyHTTPProjection(cr, projection), err
}

func dispatchOccupancyHTTPProjection(
	cr api.DispatchOccupancyMeasurement,
	projection queryapi.OccupancyEvidenceProjection,
) dispatchOccupancyProjection {
	envelope := httpapi.ProjectDispatchOccupancyMeasurement(cr)
	if cr.Spec.MinimumPairs > 0 {
		envelope.Conditions = cr.Spec
	}
	envelope.Assessment = projection.Assessment
	return dispatchOccupancyProjection{
		MeasurementProjection: envelope, DispatchOccupancyProgress: projection.DispatchOccupancyProgress,
	}
}

// decodeOccupancyEvidence adapts the retained serving wire format, including
// validation of fields that queries do not otherwise consume.
func decodeOccupancyEvidence(
	_ api.DispatchOccupancyMeasurement,
	raw []byte,
) (queryapi.DecodedOccupancyEvidence, error) {
	var header struct {
		SchemaVersion   string `json:"schemaVersion"`
		PerRequestNanos *int64 `json:"perRequestNanos"`
	}
	out := queryapi.DecodedOccupancyEvidence{}
	headerErr := json.Unmarshal(raw, &header)
	out.HasPerRequestNanos = header.PerRequestNanos != nil
	if headerErr == nil && header.SchemaVersion == servingkube.BurstOccupancySchema {
		out.Burst = true
		var record servingkube.BurstOccupancyRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return out, err
		}
		out.Record = queryapi.OccupancyEvidenceRecord{
			OccupancyRecordFacts: queryapi.OccupancyRecordFacts{
				Node: queryapi.MeasurementIdentity(record.Node), Model: record.Model, PowerMode: record.PowerMode,
				ProfileID: record.ProfileID, Verdict: record.Verdict, Estimate: queryOccupancyEstimate(record.Estimate),
			},
			SchemaVersion: record.SchemaVersion, Batch: record.Batch, Conditions: record.Conditions, Reasons: record.Reasons,
		}
		return out, nil
	}
	var record servingkube.DispatchOccupancyRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return out, err
	}
	out.Record = queryapi.OccupancyEvidenceRecord{
		OccupancyRecordFacts: queryapi.OccupancyRecordFacts{
			Node: queryapi.MeasurementIdentity(record.Node), Model: record.Model, PowerMode: record.PowerMode,
			ProfileID: record.ProfileID, Verdict: record.Verdict,
		},
		SchemaVersion: record.SchemaVersion, Batch: record.Batch, Conditions: record.Conditions,
		Window:          queryapi.OccupancyMeasurementWindow{Start: record.Window.Start, End: record.Window.End},
		PerRequestNanos: record.PerRequestNanos, Throughput: record.Throughput, IdleFraction: record.IdleFraction,
		Cycles: record.Cycles, IdleIdentified: record.IdleIdentified,
		PrecisionTargetMet: record.PrecisionTargetMet, Reasons: record.Reasons,
	}
	return out, nil
}

func queryOccupancyEstimate(value *serving.OccupancyEstimate) *queryapi.OccupancyEstimate {
	if value == nil {
		return nil
	}
	return &queryapi.OccupancyEstimate{
		Node: queryapi.MeasurementIdentity(value.Node), WorkloadUID: value.WorkloadUID,
		Model: value.Model, PowerMode: value.PowerMode, ExecutionPath: value.ExecutionPath,
		RuntimeImage: value.RuntimeImage, Batch: value.Batch, Profile: queryapi.OccupancyProfile(value.Profile),
		Pairs: value.Pairs, Batches: value.Batches, CompleteBatches: value.CompleteBatches,
		Requests: value.Requests, CompleteRequests: value.CompleteRequests,
		RawRows: value.RawRows, CompleteRawRows: value.CompleteRawRows,
		EnrichedCompleteRawRows: value.EnrichedCompleteRawRows,
		FinalRows:               value.FinalRows, CompleteFinalRows: value.CompleteFinalRows,
		Statistics: (*queryapi.OccupancyStatistics)(value.Statistics),
		Window:     queryapi.OccupancyMeasurementWindow(value.Window),
	}
}
