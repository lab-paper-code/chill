package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	querykube "github.com/lab-paper-code/chill/internal/queryapi/kube"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestW8BurstEvidenceDecode(t *testing.T) {
	for _, verdict := range []string{queryapi.MeasurementVerdictObserved, "Insufficient"} {
		t.Run(verdict, func(t *testing.T) {
			cr := api.DispatchOccupancyMeasurement{ObjectMeta: meta.ObjectMeta{Name: "burst", UID: "burst-uid"},
				Spec: api.DispatchOccupancyMeasurementSpec{Node: "node", Batch: 2, MinimumPairs: 4}}
			cr.Spec.Conditions.ProfileID = occupancyW8FixtureProfile
			cr.Status.Phase = api.MeasurementPhaseComplete
			if verdict == "Insufficient" {
				cr.Status.Phase = api.MeasurementPhaseInsufficient
			}
			cr.Status.Result = &api.ProfilingRunEvidenceReference{
				Store: "evidence", Key: servingkube.BurstOccupancyPrefix + "node-uid/burst-uid.json",
			}
			record := servingkube.BurstOccupancyRecord{SchemaVersion: servingkube.BurstOccupancySchema,
				Node: serving.NodeReference{
					Name: "node",
					UID:  "node-uid",
				}, ProfileID: occupancyW8FixtureProfile, Batch: 2,
				Conditions: cr.Spec, Verdict: verdict, Estimate: &serving.OccupancyEstimate{
					Node: serving.NodeReference{Name: "node", UID: "node-uid"}, Batch: 2,
					Profile: serving.OccupancyProfile{ProfileID: occupancyW8FixtureProfile}, Pairs: 3,
					Statistics: &serving.OccupancyStatistics{Median: -20, Mean: -10},
					Window:     serving.OccupancyWindow{Start: time.Unix(100, 0).UTC(), End: time.Unix(110, 0).UTC()},
				}}
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			service := &measurementService{
				objects: &operationProjectionStore{payload: raw}, opts: options{maxEvidenceBytes: 1 << 20},
			}
			projection, err := service.projectDispatchOccupancy(context.Background(), cr)
			if err != nil {
				t.Fatal(err)
			}
			assertBurstOccupancyWire(t, projection, cr, verdict)
			cr.Status.Result.Key = servingkube.DispatchOccupancyPrefix + "node-uid/burst-uid.json"
			mismatch, err := service.projectDispatchOccupancy(context.Background(), cr)
			if err != nil || !strings.Contains(string(mismatch.Assessment), "OccupancyEvidenceMismatch") {
				t.Fatalf("wrong path accepted: %+v (%v)", mismatch, err)
			}
			cr.Status.Result.Key = servingkube.BurstOccupancyPrefix + "node-uid/burst-uid.json"
			record.Estimate.Statistics = nil
			raw, err = json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			service.objects = &operationProjectionStore{payload: raw}
			emptyStatistics, err := service.projectDispatchOccupancy(context.Background(), cr)
			if err != nil || emptyStatistics.Estimate == nil || emptyStatistics.Estimate.Statistics != nil ||
				emptyStatistics.D != nil || emptyStatistics.Estimate.Pairs != 3 {
				t.Fatalf("null statistics invented a scalar or lost pair count: %+v (%v)", emptyStatistics, err)
			}
		})
	}
}

func TestW8LegacyMissingScalarIsNull(t *testing.T) {
	zero := int64(0)
	cr := api.DispatchOccupancyMeasurement{ObjectMeta: meta.ObjectMeta{Name: "legacy", UID: "legacy-uid"},
		Spec: api.DispatchOccupancyMeasurementSpec{Node: "node", Batch: 1}}
	cr.Status.PerRequestNanos = &zero
	cr.Status.Result = &api.ProfilingRunEvidenceReference{
		Store: "evidence", Key: servingkube.DispatchOccupancyPrefix + "node-uid/legacy-uid.json",
	}
	record := servingkube.DispatchOccupancyRecord{SchemaVersion: servingkube.DispatchOccupancySchema,
		Node: serving.NodeReference{Name: "node", UID: "node-uid"}, Batch: 1, Conditions: cr.Spec, Verdict: "Accepted"}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "perRequestNanos")
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	service := &measurementService{
		objects: &operationProjectionStore{payload: raw}, opts: options{maxEvidenceBytes: 1 << 20},
	}
	projection, err := querykube.ProjectDispatchOccupancy(
		context.Background(), service.objects, service.opts.maxEvidenceBytes, cr, decodeOccupancyEvidence,
	)
	if err != nil {
		t.Fatal(err)
	}
	result := dispatchOccupancyHTTPProjection(cr, projection)
	if result.D != nil || projection.Record != nil {
		t.Fatalf("missing scalar became stored zero: %+v", result)
	}
}

func assertBurstOccupancyWire(
	t *testing.T, projection dispatchOccupancyProjection, cr api.DispatchOccupancyMeasurement, verdict string,
) {
	t.Helper()
	wire, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["d"]) != "null" || string(fields["verdict"]) != `"`+verdict+`"` ||
		string(fields["minimumPairs"]) != "4" ||
		string(fields["schemaVersion"]) != `"`+servingkube.BurstOccupancySchema+`"` {
		t.Fatalf("burst was decoded as legacy scalar or lost verdict: %s", wire)
	}
	var conditions api.DispatchOccupancyMeasurementSpec
	if err := json.Unmarshal(fields["conditions"], &conditions); err != nil ||
		conditions.MinimumPairs != cr.Spec.MinimumPairs || conditions.Conditions.ProfileID != cr.Spec.Conditions.ProfileID {
		t.Fatalf("burst protocol conditions lost: %s (%v)", wire, err)
	}
	var estimate serving.OccupancyEstimate
	if err := json.Unmarshal(fields["estimate"], &estimate); err != nil ||
		estimate.Statistics == nil || estimate.Statistics.Median != -20 || estimate.Pairs != 3 {
		t.Fatalf("signed estimate lost: %s (%v)", wire, err)
	}
	for _, field := range []string{"saturation", "idleFraction"} {
		if string(fields[field]) != "null" {
			t.Fatalf("%s invented: %s", field, wire)
		}
	}
	var assessment queryapi.MeasurementAssessment
	if err := json.Unmarshal(projection.Assessment, &assessment); err != nil {
		t.Fatal(err)
	}
	if !assessment.Evidence.Available || assessment.Evidence.Accepted ||
		strings.Contains(string(projection.Assessment), "OccupancyEvidenceMismatch") {
		t.Fatalf("burst became adopted or mismatched: %s", projection.Assessment)
	}
	if verdict == queryapi.MeasurementVerdictObserved && assessment.Status != queryapi.MeasurementComplete {
		t.Fatalf("factual Observed record was not Complete: %+v", assessment)
	}
}

func TestOccupancyEstimateQueryConversionPreservesJSON(t *testing.T) {
	const raw = `{"node":{"name":"node","uid":"physical"},"workloadUID":"workload",
		"model":"model","powerMode":"mode","executionPath":"cpu","runtimeImage":"image","batch":4,
		"profile":{"profileID":"profile","reference":{"store":"evidence","key":"profile","size":42},
		"evidence":[{"store":"evidence","key":"raw","size":99}],"meanNanos":123.5},
		"pairs":9,"batches":10,"completeBatches":8,"requests":40,"completeRequests":32,
		"rawRows":80,"completeRawRows":64,"enrichedCompleteRawRows":60,"finalRows":40,"completeFinalRows":32,
		"statistics":{"median":-2.5,"p25":-4,"p75":1,"iqr":5,"p10":-8,"p90":3,"mean":-1,"negativeFraction":0.75},
		"window":{"start":"2026-11-04T00:00:00.000000001Z","end":"2026-11-04T00:01:00.000000002Z"}}`
	var estimate serving.OccupancyEstimate
	if err := json.Unmarshal([]byte(raw), &estimate); err != nil {
		t.Fatal(err)
	}
	for _, value := range []*serving.OccupancyEstimate{&estimate, nil, {}} {
		want, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(queryOccupancyEstimate(value))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("occupancy conversion changed JSON:\ngot %s\nwant %s", got, want)
		}
	}
}

const occupancyW8FixtureProfile = "profile"
