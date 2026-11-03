package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCoverageCatalogAdapterPreservesValuesAndRowJSON(t *testing.T) {
	zero := 0.0
	at := time.Unix(100, 123).UTC()
	facts := profilederivation.MeasurementFacts{
		Request: profilederivation.MeasurementRequestReference{Namespace: "ns", Name: "request", UID: "request-uid"},
		Condition: profilederivation.CurvePlanSpec{
			LogicalModel:      "model",
			NodeUID:           coverageFixturePhysicalUID,
			PlannedBatchSizes: []int{1},
		},
		Batches: []profilederivation.MeasurementBatchFacts{
			{Batch: 1, P95Milliseconds: &zero, IncrementalJoulesPerItem: &zero},
		},
	}
	for _, reused := range []bool{false, true} {
		profile := serving.Profile{
			ID:         "ns/request/request-uid",
			Reference:  evidencestore.ObjectIdentity{Store: "store", Key: "result"},
			Condition:  facts.Condition,
			Batches:    []serving.BatchProfile{{Batch: 1}},
			MeasuredAt: at,
			NodeName:   "node",
		}
		if reused {
			profile.Reuse = &serving.ProfileReuseCheck{
				SourceProfileID:       "source",
				ConfirmationProfileID: profile.ID,
				Outcome:               "Accepted",
				SourceNode: serving.NodeReference{
					Name: "node",
					UID:  "previous-physical-uid",
				},
				TargetNode:                  serving.NodeReference{Name: "node", UID: coverageFixturePhysicalUID},
				IdlePowerRelativeDifference: &zero,
				Batches:                     []serving.ProfileReuseBatchCheck{{Batch: 1, SourceMeanServiceNanos: -20}},
				Record:                      &evidencestore.ObjectIdentity{Store: "store", Key: "reuse"},
			}
		}
		snapshot := servingkube.ProjectCatalogSnapshot(servingkube.CatalogView{
			Profiles: []serving.Profile{
				profile,
			}, CurrentMeasurements: []profilederivation.MeasurementSelection{{ID: profile.ID}},
			MeasurementFacts: map[string]serving.MeasurementFacts{
				profile.ID: facts,
			}, SourceCheckedAt: at, Revision: "revision",
		}, "config", nil)
		source := coverageCatalogSourceAdapter{read: func(context.Context) (servingkube.CatalogRead, error) {
			return servingkube.CatalogRead{CatalogSnapshot: snapshot}, nil
		}}
		read, err := source.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(read.Reporting, queryCoverageCatalog(catalogView(snapshot))) ||
			!read.SourceCheckedAt.Equal(at) {
			t.Fatal("snapshot conversion changed queried catalog values")
		}
		converted := queryCoverageProfile(profile)
		if !reflect.DeepEqual(converted, read.Reporting.Profiles[0]) ||
			converted.Condition.NodeUID != coverageFixturePhysicalUID ||
			converted.MeasuredAt != at {
			t.Fatal("profile conversion changed identity or measured time")
		}
		_, rows := queryapi.AssessCatalogProfile(read.Reporting, converted)
		if len(rows) != 1 {
			t.Fatalf("rows=%d", len(rows))
		}
		want := queryapi.MeasurementResultRow(facts, facts.Batches[0], profile.ID, reused, true)
		want.RowKind, want.ID = "direct", profile.ID+"/direct/1"
		if reused {
			want.RowKind, want.ID = queryapi.ResultRowConfirmation, profile.ID+"/confirmation/1"
			want.SourceProfileID, want.Reuse = &profile.Reuse.SourceProfileID, profile.Reuse
		}
		actualJSON, err := json.Marshal(rows[0])
		if err != nil {
			t.Fatal(err)
		}
		wantJSON, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actualJSON, wantJSON) {
			t.Fatalf("row JSON changed: got %s want %s", actualJSON, wantJSON)
		}
	}
}

func TestCoverageCatalogAdapterPreservesOccupancyAvailabilityAndSignedFacts(t *testing.T) {
	at := time.Unix(100, 123).UTC()
	for _, selected := range [][]serving.DispatchOccupancy{
		nil, {},
		{{
			Node:  serving.NodeReference{Name: "node", UID: coverageFixturePhysicalUID},
			Model: "model", PowerMode: "fixed", ExecutionPath: "cpu", ProfileID: "profile", Batch: 1,
			Provenance: &serving.DispatchOccupancyProvenance{
				Source: "measured", Value: -9223372036854775807, Pairs: 0, UpdatedAt: at,
			},
		}},
	} {
		accepted := servingkube.CatalogSnapshot{
			DispatchOccupancy: selected,
			OccupancyEstimates: []serving.OccupancyEstimate{{
				Node: serving.NodeReference{Name: "node", UID: coverageFixturePhysicalUID}, Model: "model", Batch: 1,
				Statistics: &serving.OccupancyStatistics{
					Median: -42,
					Mean:   -20,
				}, Profile: serving.OccupancyProfile{ProfileID: "profile"},
			}},
		}
		read, err := (coverageCatalogSourceAdapter{read: func(context.Context) (servingkube.CatalogRead, error) {
			return servingkube.CatalogRead{
				CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: at}, Accepted: &accepted,
			}, nil
		}}).Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if (read.AcceptedOccupancy == nil) != (selected == nil) || len(read.AcceptedOccupancy) != len(selected) {
			t.Fatal("nil and confirmed empty selection were conflated")
		}
		if len(selected) > 0 &&
			(read.AcceptedOccupancy[0].Node.UID != coverageFixturePhysicalUID ||
				read.AcceptedOccupancy[0].Provenance.Value != -9223372036854775807 ||
				read.AcceptedOccupancy[0].Provenance.Window != nil) {
			t.Fatal("signed nanoseconds, null window or physical identity changed")
		}
		if read.ObservedOccupancy[0].Statistics.Median != -42 ||
			read.ObservedOccupancy[0].Node.UID != coverageFixturePhysicalUID {
			t.Fatal("observed signed facts changed")
		}
	}
	if queryCatalogSource(nil) != nil {
		t.Fatal("missing catalog source was wrapped")
	}
}

func TestW8CoverageDoesNotInferLegacyAdoption(t *testing.T) {
	base, cell, task := coverageCatalogAdapterFixture(t)
	selection := servingkube.OccupancySelection{Node: serving.NodeReference{Name: "node", UID: "node-uid"},
		Model: cell.Model.Name, PowerMode: cell.PowerMode, ExecutionPath: cell.ExecutionPath,
		ProfileID: task.Spec.Conditions.ProfileID, Batch: int(task.Spec.Batch)}
	selection.Evidence.Store, selection.Evidence.Key = "evidence", "accepted.json"
	read, err := (coverageCatalogSourceAdapter{read: func(context.Context) (servingkube.CatalogRead, error) {
		return servingkube.CatalogRead{CatalogSnapshot: servingkube.CatalogSnapshot{SourceCheckedAt: time.Now().UTC()},
			Accepted: &servingkube.CatalogSnapshot{Occupancy: []servingkube.OccupancySelection{selection}}}, nil
	}}).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	base.AcceptedOccupancy = read.AcceptedOccupancy
	view := queryapi.BuildMeasurementCoverage(base, "ns", queryapi.CoverageQuery{})
	if len(view.Cells) != 1 || len(view.Cells[0].Occupancy) != 1 {
		t.Fatal("occupancy tuple missing")
	}
	entry := view.Cells[0].Occupancy[0]
	if entry.Adopted != nil && *entry.Adopted {
		t.Fatal("legacy accepted.occupancy inferred scheduler adoption")
	}
}

func coverageCatalogAdapterFixture(
	t *testing.T,
) (*queryapi.CoverageBase, queryapi.MeasurementCoverageCell, api.DispatchOccupancyMeasurement) {
	t.Helper()
	task := api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{Namespace: "ns", Name: "request", UID: "request-uid"},
		Spec: api.BatchEnergyOptimizationSpec{
			State: api.MeasurementStateSpec{
				Candidate: api.ProfilingRunCandidateSpec{
					ModelSpec:     "model",
					DeviceClass:   "class",
					ExecutionPath: "cpu",
					PowerMode:     "fixed",
				},
			},
		},
	}
	id := queryapi.CoverageRequestID(task)
	condition := profilederivation.CurvePlanSpec{LogicalModel: "model", DeviceClass: "class", PowerMode: "fixed"}
	base := &queryapi.CoverageBase{
		Inventory: queryapi.InventoryView{
			Models: []queryapi.InventoryModelView{
				{Name: "model", Spec: api.ModelSpecSpec{ExecutionPaths: []api.ModelExecutionPath{{Name: "cpu"}}}},
			},
			Classes: []queryapi.InventoryClassView{
				{
					Name:       "class",
					PowerModes: []queryapi.InventoryPowerModeView{{Name: "fixed"}},
					Nodes:      []queryapi.InventoryNodeView{{Name: "node", UID: "node-uid"}},
				},
			},
		},
		Requests: api.BatchEnergyOptimizationList{
			Items: []api.BatchEnergyOptimization{task},
		}, Checked: map[string]time.Time{"catalog": time.Now()},
		Catalog: queryapi.CoverageCatalog{
			Profiles: []queryapi.CoverageProfile{{ID: id}},
			MeasurementFacts: map[string]profilederivation.MeasurementFacts{id: {
				Request: profilederivation.MeasurementRequestReference{
					Namespace: "ns",
					Name:      "request",
					UID:       "request-uid",
				}, Condition: condition}},
		},
	}
	cr := api.DispatchOccupancyMeasurement{
		ObjectMeta: meta.ObjectMeta{Name: "occupancy", UID: "occupancy-uid"},
		Spec:       api.DispatchOccupancyMeasurementSpec{Node: "node", BatchEnergyOptimization: task.Name, Batch: 1},
	}
	cr.Spec.Conditions.ProfileID = id
	base.Occupancies.Items = []api.DispatchOccupancyMeasurement{cr}
	base.OccupancyResults = map[string]queryapi.OccupancyEvidenceProjection{string(cr.UID): {
		DispatchOccupancyProgress: queryapi.OccupancyProgress(cr),
		Record: &queryapi.OccupancyRecordFacts{
			Node:      queryapi.MeasurementIdentity{Name: "node", UID: "node-uid"},
			ProfileID: id, Verdict: queryapi.MeasurementVerdictAccepted,
		},
	}}
	view := queryapi.BuildMeasurementCoverage(base, "ns", queryapi.CoverageQuery{})
	if len(view.Cells) != 1 {
		t.Fatal("current profile cell missing")
	}
	return base, view.Cells[0], cr
}

const coverageFixturePhysicalUID = "physical-uid"
