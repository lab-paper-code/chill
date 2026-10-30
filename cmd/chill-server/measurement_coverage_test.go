package main

import (
	"context"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestCoverageNeverUnionsDifferentFrozenVariants(t *testing.T) {
	now := time.Now().UTC()
	base := &measurementCoverageBase{checked: map[string]time.Time{}, failures: map[string]error{}, catalog: httpapi.ProfilesView{}}
	for _, kind := range []string{"inventory", "compatibility", "catalog", "BEO", "Run"} {
		base.checked[kind] = now
	}
	base.inventory = operatorconsole.InventoryView{Models: []operatorconsole.InventoryModelView{{Name: "model", UID: "model-uid", Spec: api.ModelSpecSpec{ExecutionPaths: []api.ModelExecutionPath{{Name: "cpu"}}}}}, Classes: []operatorconsole.InventoryClassView{{Name: "class", UID: "class-uid", PowerModes: []operatorconsole.InventoryPowerModeView{{Name: "fixed"}}, Nodes: []operatorconsole.InventoryNodeView{{Name: "node", UID: "node-uid"}}}}}
	base.compatibility.Rows = []operatorconsole.ModelCompatibilityView{{Selection: operatorconsole.ModelCompatibilitySelection{ModelSpec: "model", DeviceClass: "class", ExecutionPath: "cpu", Node: "node"}, Verdict: "Compatible", Inputs: operatorconsole.ModelCompatibilityInputs{Node: operatorconsole.ModelCompatibilityObjectInput{Name: "node", UID: "node-uid"}}}}
	base.catalog.MeasurementFacts = map[string]serving.MeasurementFacts{}
	zero := 0.0
	for index := range 2 {
		name := []string{"first", "second"}[index]
		uid := types.UID(name + "-uid")
		task := api.BatchEnergyOptimization{ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: name, UID: uid, Generation: 1}, Spec: api.BatchEnergyOptimizationSpec{State: api.MeasurementStateSpec{Candidate: api.ProfilingRunCandidateSpec{ModelSpec: "model", DeviceClass: "class", ExecutionPath: "cpu", PowerMode: "fixed"}, TargetNode: "node"}, BatchSizes: []int32{1, 2}}, Status: api.BatchEnergyOptimizationStatus{Phase: "Complete", ObservedGeneration: 1}}
		id := coverageRequestID(task)
		batch := index + 1
		condition := profilederivation.CurvePlanSpec{LogicalModel: "model", DeviceClass: "class", NodeUID: "node-uid", PowerMode: "fixed", CPU: profilederivation.CurveCPUContract{Request: []string{"1", "2"}[index]}, PlannedBatchSizes: []int{1, 2}}
		facts := serving.MeasurementFacts{Request: profilederivation.MeasurementRequestReference{Namespace: "allowed", Name: name, UID: string(uid)}, Condition: condition, Batches: []profilederivation.MeasurementBatchFacts{{Batch: batch, IncrementalJoulesPerItem: &zero, P95Milliseconds: &zero}}}
		base.requests.Items = append(base.requests.Items, task)
		base.catalog.Profiles = append(base.catalog.Profiles, serving.Profile{ID: id, Condition: condition, Batches: []serving.BatchProfile{{Batch: batch}}})
		base.catalog.MeasurementFacts[id] = facts
	}
	got := buildMeasurementCoverage(base, "allowed", httpapi.MeasurementCoverageQuery{Batches: []int{1, 2}})
	if len(got.Cells) != 1 {
		t.Fatalf("cells %d", len(got.Cells))
	}
	cell := got.Cells[0]
	if cell.VariantCount != 2 || cell.BatchProgress != nil || cell.Representative != nil {
		t.Fatalf("multiple conditions exposed union: %+v", cell)
	}
	if cell.Assessment.Status != "partial" || *got.Summary.CompleteCellCount != 0 {
		t.Fatalf("fake complete across CPU conditions: %+v", cell.Assessment)
	}
	for _, variant := range cell.Variants {
		if len(variant.BatchProgress.Measured) != 1 || len(variant.BatchProgress.Missing) != 1 {
			t.Fatalf("variant batch union %+v", variant.BatchProgress)
		}
	}
	// A cost-source failure is intentionally absent from the source set and cannot
	// affect coverage completeness; a real Run source failure does.
	base.failures["Run"] = assertCoverageError{}
	partial := buildMeasurementCoverage(base, "allowed", httpapi.MeasurementCoverageQuery{})
	if partial.Completeness != "partial" || partial.Summary.CountsByDisplayStatus["unmeasured"] != nil {
		t.Fatal("incomplete run source invented unmeasured count")
	}
}

type assertCoverageError struct{}

func (assertCoverageError) Error() string { return "source unavailable" }

func TestCoverageFreshNamespaceSharedByFiveCallers(t *testing.T) {
	base := &measurementCoverageBase{checked: map[string]time.Time{}, failures: map[string]error{}}
	for _, kind := range []string{"inventory", "compatibility", "catalog", "BEO", "Run"} {
		base.checked[kind] = time.Now().UTC()
	}
	service := &measurementService{opts: options{servingNamespace: "allowed", maxRequestBytes: 8 << 20}, coverageCache: measurementCoverageCache{namespaces: map[string]*measurementCoverageEntry{"allowed": {base: base}}}}
	// Nil Kubernetes/Console dependencies make any redundant source I/O fail.
	results := make(chan error, 5)
	for range 5 {
		go func() {
			_, err := service.coverage(context.Background(), "allowed", httpapi.MeasurementCoverageQuery{Batches: []int{1, 2}})
			results <- err
		}()
	}
	for range 5 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if len(base.checked) != 5 || len(base.requests.Items) != 0 {
		t.Fatal("query projection mutated base snapshot")
	}
}
