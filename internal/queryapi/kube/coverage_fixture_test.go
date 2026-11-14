package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	coverageSizeNamespace = "measurement-campaign-production"
	coverageSizeNode      = "edge-node-01"
	coverageSizeNodeUID   = "node-physical-01"
)

type coverageCatalogFunc func(context.Context) (queryapi.CoverageCatalogRead, error)

func (f coverageCatalogFunc) Read(ctx context.Context) (queryapi.CoverageCatalogRead, error) {
	return f(ctx)
}

func coverageSizeBase() *queryapi.CoverageBase {
	base := &queryapi.CoverageBase{Checked: map[string]time.Time{}, Failures: map[string]error{}}
	for _, kind := range []string{"inventory", "compatibility", "catalog", "BEO", "Run"} {
		base.Checked[kind] = time.Now().UTC()
	}
	base.Inventory.Models = []queryapi.InventoryModelView{
		{Name: "model", Spec: api.ModelSpecSpec{ExecutionPaths: []api.ModelExecutionPath{{Name: "cpu"}}}},
	}
	base.Inventory.Classes = []queryapi.InventoryClassView{
		{
			Name:       "class",
			PowerModes: []queryapi.InventoryPowerModeView{{Name: "fixed"}},
			Nodes:      []queryapi.InventoryNodeView{{Name: coverageSizeNode, UID: coverageSizeNodeUID}},
		},
	}
	task := api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{
			Namespace:  coverageSizeNamespace,
			Name:       "request",
			UID:        "request-uid",
			Generation: 1,
		},
		Spec: api.BatchEnergyOptimizationSpec{
			State: api.MeasurementStateSpec{
				TargetNode: coverageSizeNode,
				Candidate: api.ProfilingRunCandidateSpec{
					ModelSpec:     "model",
					DeviceClass:   "class",
					ExecutionPath: "cpu",
					PowerMode:     "fixed",
				},
			},
			Session: &api.BatchEnergyOptimizationSessionSpec{},
		},
		Status: api.BatchEnergyOptimizationStatus{Phase: "Complete", ObservedGeneration: 1},
	}
	id := queryapi.CoverageRequestID(task)
	base.Requests.Items = []api.BatchEnergyOptimization{task}
	condition := profilederivation.CurvePlanSpec{
		LogicalModel:      "model",
		DeviceClass:       "class",
		NodeUID:           coverageSizeNodeUID,
		PowerMode:         "fixed",
		PlannedBatchSizes: []int{1},
	}
	base.Catalog.Profiles = []queryapi.CoverageProfile{
		{ID: id, Condition: condition, Batches: []int{1}, MeasuredAt: time.Unix(1, 0)},
	}
	base.Catalog.MeasurementFacts = map[string]profilederivation.MeasurementFacts{
		id: {
			Request: profilederivation.MeasurementRequestReference{
				Namespace: task.Namespace,
				Name:      task.Name,
				UID:       string(task.UID),
			},
			Condition: condition,
			Batches:   []profilederivation.MeasurementBatchFacts{{Batch: 1}},
		},
	}
	return base
}

func coverageOccupancyFixture(
	t *testing.T,
) (*queryapi.CoverageBase, queryapi.MeasurementCoverageCell, api.DispatchOccupancyMeasurement) {
	t.Helper()
	base := coverageSizeBase()
	initial := queryapi.BuildMeasurementCoverage(base, coverageSizeNamespace, queryapi.CoverageQuery{})
	var selected queryapi.MeasurementCoverageCell
	for _, cell := range initial.Cells {
		if cell.Occupancy == nil || len(cell.Occupancy) != 0 {
			t.Fatal("request-free coverage invented node/batch occupancy tuples")
		}
		if selected.ID == "" && len(cell.Variants) > 0 && len(cell.Variants[0].ProfileIDs) > 0 {
			selected = cell
		}
	}
	if selected.ID == "" {
		t.Fatal("current profile cell missing")
	}
	task := api.DispatchOccupancyMeasurement{
		ObjectMeta: meta.ObjectMeta{
			Name:              "occupancy",
			UID:               "occupancy-uid",
			CreationTimestamp: meta.NewTime(time.Unix(1, 0)),
		},
		Spec: api.DispatchOccupancyMeasurementSpec{
			Node:                    coverageSizeNode,
			BatchEnergyOptimization: strings.Split(selected.CurrentMeasurement.ID, "/")[1],
			Batch:                   int32(selected.Variants[0].BatchProgress.Measured[0]),
		},
	}
	task.Spec.Conditions.ProfileID = selected.Variants[0].ProfileIDs[0]
	base.OccupancyResults = map[string]queryapi.OccupancyEvidenceProjection{}
	return base, selected, task
}

func findCoverageOccupancy(
	t *testing.T,
	base *queryapi.CoverageBase,
	selected queryapi.MeasurementCoverageCell,
	task api.DispatchOccupancyMeasurement,
) queryapi.MeasurementOccupancy {
	t.Helper()
	got := queryapi.BuildMeasurementCoverage(base, coverageSizeNamespace, queryapi.CoverageQuery{})
	for _, cell := range got.Cells {
		if cell.ID != selected.ID {
			continue
		}
		for _, entry := range cell.Occupancy {
			if entry.ProfileID == task.Spec.Conditions.ProfileID && entry.Batch == int(task.Spec.Batch) {
				return entry
			}
		}
	}
	t.Fatal("occupancy tuple missing")
	return queryapi.MeasurementOccupancy{}
}

func acceptCoverageOccupancy(base *queryapi.CoverageBase, task api.DispatchOccupancyMeasurement) {
	progress := queryapi.OccupancyProgress(task)
	d := int64(-9223372036854775807)
	progress.D = &queryapi.OccupancyValue{PerRequestNanos: &d, Verdict: queryapi.MeasurementVerdictAccepted}
	progress.Evidence = &queryapi.OccupancyEvidence{Store: "evidence", Key: "accepted.json", Size: 123}
	base.OccupancyResults[string(task.UID)] = queryapi.OccupancyEvidenceProjection{
		DispatchOccupancyProgress: progress,
		Record: &queryapi.OccupancyRecordFacts{
			Node:      queryapi.MeasurementIdentity{Name: coverageSizeNode, UID: coverageSizeNodeUID},
			Verdict:   queryapi.MeasurementVerdictAccepted,
			ProfileID: task.Spec.Conditions.ProfileID,
		},
	}
}
