package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestW8BurstEvidenceDecode(t *testing.T) {
	for _, verdict := range []string{measurementVerdictObserved, "Insufficient"} {
		t.Run(verdict, func(t *testing.T) {
			cr := api.DispatchOccupancyMeasurement{ObjectMeta: meta.ObjectMeta{Name: "burst", UID: "burst-uid"},
				Spec: api.DispatchOccupancyMeasurementSpec{Node: "node", Batch: 2, MinimumPairs: 4}}
			cr.Spec.Conditions.ProfileID = "profile"
			cr.Status.Phase = api.MeasurementPhaseComplete
			if verdict == "Insufficient" {
				cr.Status.Phase = api.MeasurementPhaseInsufficient
			}
			cr.Status.Result = &api.ProfilingRunEvidenceReference{
				Store: "evidence", Key: servingkube.BurstOccupancyPrefix + "node-uid/burst-uid.json",
			}
			record := servingkube.BurstOccupancyRecord{SchemaVersion: servingkube.BurstOccupancySchema,
				Node: serving.NodeReference{Name: "node", UID: "node-uid"}, ProfileID: "profile", Batch: 2,
				Conditions: cr.Spec, Verdict: verdict, Estimate: &serving.OccupancyEstimate{
					Node: serving.NodeReference{Name: "node", UID: "node-uid"}, Batch: 2,
					Profile: serving.OccupancyProfile{ProfileID: "profile"}, Pairs: 3,
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

func TestW8CoverageDoesNotInferLegacyAdoption(t *testing.T) {
	base, cell, task := coverageOccupancyFixture(t)
	acceptCoverageOccupancy(base, task)
	base.occupancies.Items = append(base.occupancies.Items, task)
	selection := servingkube.OccupancySelection{Node: base.occupancyResults[string(task.UID)].record.Node,
		Model: cell.Model.Name, PowerMode: cell.PowerMode, ExecutionPath: cell.ExecutionPath,
		ProfileID: task.Spec.Conditions.ProfileID, Batch: int(task.Spec.Batch)}
	selection.Evidence.Store, selection.Evidence.Key = "evidence", "accepted.json"
	next, err := readCoverageAdoptionFixture(t, base, func(context.Context) (servingkube.CatalogRead, error) {
		return servingkube.CatalogRead{CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: time.Now().UTC()},
			Accepted: &servingkube.CatalogSnapshot{Occupancy: []servingkube.OccupancySelection{selection}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := findCoverageOccupancy(t, next, cell, task)
	if entry.Adopted != nil && *entry.Adopted {
		t.Fatal("legacy accepted.occupancy inferred scheduler adoption")
	}
}

func TestW8CoverageWithoutDOM(t *testing.T) {
	base, cell, task := coverageOccupancyFixture(t)
	at := time.Now().UTC()
	estimate := serving.OccupancyEstimate{
		Node:  serving.NodeReference{Name: coverageSizeNode, UID: coverageSizeNodeUID},
		Model: cell.Model.Name, PowerMode: cell.PowerMode, ExecutionPath: cell.ExecutionPath,
		Batch: int(task.Spec.Batch), Profile: serving.OccupancyProfile{ProfileID: task.Spec.Conditions.ProfileID},
		Pairs: 9, Statistics: &serving.OccupancyStatistics{Median: -42, Mean: -20},
		Window: serving.OccupancyWindow{Start: at.Add(-time.Minute), End: at},
	}
	base.acceptedOccupancy = []serving.DispatchOccupancy{{
		Node: estimate.Node, Model: estimate.Model, PowerMode: estimate.PowerMode,
		ExecutionPath: estimate.ExecutionPath, ProfileID: estimate.Profile.ProfileID, Batch: estimate.Batch,
		Provenance: &serving.DispatchOccupancyProvenance{Source: "default", Value: 2000},
	}}
	for _, tc := range []struct {
		name    string
		change  func(*serving.OccupancyEstimate)
		present bool
	}{
		{name: "signed observation and default coexist", present: true},
		{name: "runtime image display only", present: true, change: func(e *serving.OccupancyEstimate) {
			e.RuntimeImage = "different"
		}},
		{name: "wrong physical node", change: func(e *serving.OccupancyEstimate) { e.Node.UID = "replaced" }},
		{name: "wrong model", change: func(e *serving.OccupancyEstimate) { e.Model = occupancyOtherIdentity }},
		{name: "wrong mode", change: func(e *serving.OccupancyEstimate) { e.PowerMode = occupancyOtherIdentity }},
		{name: "wrong execution path", change: func(e *serving.OccupancyEstimate) {
			e.ExecutionPath = occupancyOtherIdentity
		}},
		{name: "wrong profile", change: func(e *serving.OccupancyEstimate) { e.Profile.ProfileID = "old" }},
		{name: "wrong batch", change: func(e *serving.OccupancyEstimate) { e.Batch++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := estimate
			if tc.change != nil {
				tc.change(&observed)
			}
			base.observedOccupancy = []serving.OccupancyEstimate{observed}
			entry := findCoverageOccupancy(t, base, cell, task)
			if entry.Request != nil || entry.D != nil || entry.SchedulerD == nil ||
				entry.SchedulerD.Value != 2000 || entry.Adopted == nil || *entry.Adopted {
				t.Fatalf("no-DOM tuple invented a request, scalar or adoption: %+v", entry)
			}
			if (entry.ObservedD != nil) != tc.present {
				t.Fatalf("observed identity mismatch: %+v", entry)
			}
			if entry.ObservedD != nil && (entry.ObservedD.Statistics.Median != -42 || entry.ObservedD.Pairs != 9) {
				t.Fatalf("signed observation changed: %+v", entry.ObservedD)
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
	result, err := service.projectDispatchOccupancy(context.Background(), cr)
	if err != nil {
		t.Fatal(err)
	}
	if result.D != nil || result.record != nil {
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
	if verdict == measurementVerdictObserved && assessment.Status != queryapi.MeasurementComplete {
		t.Fatalf("factual Observed record was not Complete: %+v", assessment)
	}
}
