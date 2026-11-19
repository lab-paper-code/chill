package kube

import (
	"context"
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
