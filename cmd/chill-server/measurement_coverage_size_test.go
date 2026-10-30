package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

const (
	coverageSizeNamespace   = "measurement-campaign-production"
	coverageSizeClass       = "edge-cpu"
	coverageSizeNode        = "edge-node-01"
	coverageSizeNodeUID     = "node-physical-01"
	coverageSizePath        = "onnx-cpu"
	coverageSizePowerSource = "node-wall"
)

func TestMeasurementCoverageCampaignFitsResponseBudget(t *testing.T) {
	base := coverageSizeBase()
	got := buildMeasurementCoverage(base, coverageSizeNamespace, httpapi.MeasurementCoverageQuery{})
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("coverage bytes=%d profiles=%d requests=%d runs=%d",
		len(raw), len(got.Entities.Profiles), len(got.Entities.Requests), len(got.Entities.Runs))
	if len(raw) >= (8<<20)/2 {
		t.Errorf("coverage uses %d bytes; must remain below half the 8MiB response limit", len(raw))
	}
	if len(got.Entities.Runs) != 1200 || len(got.Entities.Requests) != 90 || len(got.Entities.Profiles) != 80 {
		t.Fatalf("entities truncated: runs=%d requests=%d profiles=%d",
			len(got.Entities.Runs), len(got.Entities.Requests), len(got.Entities.Profiles))
	}
	coverageSizeAssertWire(t, raw)
	rows, history := 0, 0
	seen := map[string]bool{}
	for _, cell := range got.Cells {
		history += len(cell.History)
		if len(cell.Assessment.Checklist.Checks) != 0 {
			t.Errorf("cell %s repeats checklist checks", cell.ID)
		}
		for _, variant := range cell.Variants {
			rows += len(variant.ResultRows)
			coverageSizeAssertVariant(t, base, variant, seen)
		}
	}
	if rows != 240 || len(seen) != 240 || history != 90 {
		t.Fatalf("projection truncated: rows=%d unique=%d history=%d", rows, len(seen), history)
	}
}

func coverageSizeBase() *measurementCoverageBase {
	base := &measurementCoverageBase{
		checked: map[string]time.Time{}, failures: map[string]error{},
		catalog: httpapi.ProfilesView{},
	}
	base.catalog.MeasurementFacts = map[string]serving.MeasurementFacts{}
	for _, kind := range []string{"inventory", "compatibility", "catalog", "BEO", "Run"} {
		base.checked[kind] = time.Now().UTC()
	}
	base.inventory.Classes = []operatorconsole.InventoryClassView{{
		Name: coverageSizeClass, UID: "class-2df925e8-7142-45cd-8427-9f89c1155a80",
		PowerModes: []operatorconsole.InventoryPowerModeView{{Name: "performance"}, {Name: "powersave"}},
		Nodes:      []operatorconsole.InventoryNodeView{{Name: coverageSizeNode, UID: coverageSizeNodeUID}},
	}}
	for _, model := range []string{"resnet50", "mobilenet-v3"} {
		base.inventory.Models = append(base.inventory.Models, operatorconsole.InventoryModelView{
			Name: model, UID: model + "-spec-uid",
			Spec: api.ModelSpecSpec{ExecutionPaths: []api.ModelExecutionPath{{Name: coverageSizePath}}},
		})
		base.compatibility.Rows = append(base.compatibility.Rows, operatorconsole.ModelCompatibilityView{
			Selection: operatorconsole.ModelCompatibilitySelection{
				ModelSpec: model, DeviceClass: coverageSizeClass, ExecutionPath: coverageSizePath, Node: coverageSizeNode,
			},
			Verdict: operatorconsole.MeasurementSupportCompatible,
			Inputs: operatorconsole.ModelCompatibilityInputs{
				Node: operatorconsole.ModelCompatibilityObjectInput{Name: coverageSizeNode, UID: coverageSizeNodeUID},
			},
		})
	}
	for index := range 90 {
		task := coverageSizeRequest(index)
		base.requests.Items = append(base.requests.Items, task)
		if index < 80 {
			coverageSizeAddProfile(base, task, index)
		}
	}
	for index := range 1200 {
		task := base.requests.Items[index%90]
		base.runs.Items = append(base.runs.Items, api.ProfilingRun{
			ObjectMeta: meta.ObjectMeta{
				Namespace: task.Namespace, Name: fmt.Sprintf("%s-trial-%04d", task.Name, index),
				UID: types.UID(fmt.Sprintf("b3b3258c-4f21-4f94-9c5f-%012d", index)), Generation: 1,
				OwnerReferences: []meta.OwnerReference{{
					APIVersion: api.GroupVersion.String(), Kind: "BatchEnergyOptimization",
					Name: task.Name, UID: task.UID, Controller: ptr.To(true),
				}},
			},
			Spec: api.ProfilingRunSpec{MeasurementStateSpec: task.Spec.State},
		})
	}
	return base
}

func coverageSizeRequest(index int) api.BatchEnergyOptimization {
	return api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{
			Namespace: coverageSizeNamespace, Name: fmt.Sprintf("energy-resnet-cpu-campaign-20261030-%03d", index),
			UID: types.UID(fmt.Sprintf("e82961a0-316d-4fe3-9cb1-%012d", index)), Generation: 1,
		},
		Spec: api.BatchEnergyOptimizationSpec{
			State: api.MeasurementStateSpec{
				Candidate: api.ProfilingRunCandidateSpec{
					ModelSpec:   []string{"resnet50", "mobilenet-v3"}[(index/2)%2],
					DeviceClass: coverageSizeClass, ExecutionPath: coverageSizePath,
					PowerMode: []string{"performance", "powersave"}[(index/4)%2],
				}, TargetNode: coverageSizeNode,
			}, BatchSizes: []int32{1, 4, 8},
		},
		Status: api.BatchEnergyOptimizationStatus{Phase: "Complete", ObservedGeneration: 1},
	}
}

func coverageSizeAddProfile(base *measurementCoverageBase, task api.BatchEnergyOptimization, index int) {
	condition := coverageSizeCondition(task, index/2)
	id := coverageRequestID(task)
	facts := serving.MeasurementFacts{
		Request: profilederivation.MeasurementRequestReference{
			Namespace: task.Namespace, Name: task.Name, UID: string(task.UID),
		},
		ResultReference: profilederivation.RawEvidenceIdentity{
			Store: "campaign-evidence", Key: id + "/results/batch-energy-result.json", Size: 32768,
		},
		Condition:   condition,
		PointPolicy: profilederivation.PointPolicy{Version: "point-policy.v2", MinimumTrials: 3, ConfidenceLevel: .95},
	}
	profile := serving.Profile{ID: id, Condition: condition, NodeName: coverageSizeNode}
	for _, batch := range []int{1, 4, 8} {
		facts.Batches = append(facts.Batches, coverageSizeBatch(task, condition, batch))
		profile.Batches = append(profile.Batches, serving.BatchProfile{Batch: batch})
	}
	base.catalog.Profiles = append(base.catalog.Profiles, profile)
	base.catalog.MeasurementFacts[id] = facts
}

func coverageSizeCondition(task api.BatchEnergyOptimization, group int) profilederivation.CurvePlanSpec {
	active := profilederivation.CurveProtocolContract{
		WorkloadMode: "active",
		Dataset: &profilederivation.CurveDatasetContract{
			Name: "imagenet-validation", UID: "dataset-uid",
		},
		WarmupIterations: 10, SettlingDurationSeconds: 5, DurationSeconds: 30, DurationToleranceSeconds: 2,
		MetricIntervalMilliseconds: 100, MetricTimeoutMilliseconds: 1000, MetricDurationSeconds: 30,
		MetricObserverImage:   "registry.example.org/chill/power-observer:20261030",
		WorkloadObserverImage: "registry.example.org/chill/workload-observer:20261030",
		WorkloadSchemaVersion: "workload.v1",
	}
	idle := active
	idle.WorkloadMode, idle.Dataset = "idle", nil
	return profilederivation.CurvePlanSpec{
		Name: fmt.Sprintf("frozen-condition-%02d", group), LogicalModel: task.Spec.State.Candidate.ModelSpec,
		DeviceClass: coverageSizeClass, NodeUID: coverageSizeNodeUID, RuntimeFamily: "onnxruntime", Backend: "cpu",
		RuntimeImage: "registry.example.org/chill/onnxruntime:1.20.1", PowerMode: task.Spec.State.Candidate.PowerMode,
		CPU: profilederivation.CurveCPUContract{
			Policy: "fixed", PolicyVersion: "cpu.v1", Request: fmt.Sprint(group + 1), Limit: fmt.Sprint(group + 1),
			ORTIntraOpThreads: 1, ORTInterOpThreads: 1,
		},
		MetricSource: profilederivation.CurveMetricSourceContract{
			SourceKind: coverageSizePowerSource, NodeName: coverageSizeNode, Endpoint: "http://edge-meter:9100/metrics",
			PowerMetric: "node_wall_power_watts",
		},
		ActiveProtocol: active, IdleProtocol: idle, PlannedBatchSizes: []int{1, 4, 8},
		Artifact: profilederivation.CurveArtifactContract{
			ExecutionPath: coverageSizePath, Name: "model.onnx", Files: []string{"model.onnx"},
			Image: "registry.example.org/chill/model-artifacts:20261030",
		},
	}
}

func coverageSizeBatch(
	task api.BatchEnergyOptimization, condition profilederivation.CurvePlanSpec, batch int,
) profilederivation.MeasurementBatchFacts {
	facts := profilederivation.MeasurementBatchFacts{
		Batch: batch, P95Milliseconds: ptr.To(0.0), LatencySampleCount: 300,
		IdleWatts: ptr.To(0.0), ActiveWatts: ptr.To(12.0), TotalJoulesPerItem: ptr.To(0.0),
		IncrementalJoulesPerItem: ptr.To(-1.0),
		Confidence:               &profilederivation.PointConfidence{Level: .95, HalfWidth: 2},
		ActiveTrialCount:         3, IdleTrialCount: 3, CompletedItems: batch * 300, CompletedCalls: 300,
		PowerSource: profilederivation.MeasurementPowerSource{
			Kind: coverageSizePowerSource, NodeName: coverageSizeNode, Endpoint: condition.MetricSource.Endpoint,
			Metric: "node_wall_power_watts",
		},
	}
	for _, protocol := range []profilederivation.CurveProtocolContract{condition.ActiveProtocol, condition.IdleProtocol} {
		for trial := range 3 {
			trialID := fmt.Sprintf("%s-batch-%03d-%s-trial-%03d", task.Name, batch, protocol.WorkloadMode, trial)
			facts.Quality = append(facts.Quality, profilederivation.MeasurementTrialQuality{
				TrialID: trialID, WorkloadMode: protocol.WorkloadMode, Protocol: protocol,
				WindowSeconds: 30, SuccessfulSamples: 300, MaximumSampleGapSeconds: .1, PolicyPassed: ptr.To(true),
			})
			facts.Sources = append(facts.Sources, profilederivation.RawEvidenceIdentity{
				Store: "campaign-evidence",
				Key: fmt.Sprintf("%s/profilingruns/%s/%s/records/power-window-workload-result.json",
					coverageRequestID(task), trialID, task.UID), Size: 16384,
			})
		}
	}
	return facts
}

func coverageSizeAssertWire(t *testing.T, raw []byte) {
	t.Helper()
	var wire struct {
		Entities map[string][]map[string]json.RawMessage `json:"entities"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"runs", "requests", "profiles"} {
		want := []string{"cellIds", "label", "status", "uid"}
		if kind != "runs" {
			want = []string{"cellIds", "id", "label", "status", "uid"}
		}
		for _, entity := range wire.Entities[kind] {
			keys := make([]string, 0, len(entity))
			for key := range entity {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			if !reflect.DeepEqual(keys, want) {
				t.Fatalf("%s entity keys=%v, want %v", kind, keys, want)
			}
		}
	}
}

type coverageSizeCheckKey struct {
	code, result, message string
}

func coverageSizeAssertVariant(
	t *testing.T, base *measurementCoverageBase, variant operatorconsole.MeasurementCoverageVariant, seen map[string]bool,
) {
	t.Helper()
	expected := map[coverageSizeCheckKey][]string{}
	for _, row := range variant.ResultRows {
		if seen[row.ID] {
			t.Fatalf("duplicate row %s", row.ID)
		}
		seen[row.ID] = true
		facts := base.catalog.MeasurementFacts[row.ProfileID]
		if !reflect.DeepEqual(variant.Condition, facts.Condition) ||
			!reflect.DeepEqual(row.Condition, facts.Condition) {
			t.Fatalf("row %s frozen conditions changed", row.ID)
		}
		for _, batch := range facts.Batches {
			if batch.Batch != row.Batch {
				continue
			}
			original := operatorconsole.AssessMeasurementBatch(batch, facts.PointPolicy.MinimumTrials, false, true)
			coverageSizeAssertRow(t, row, batch, original)
			for _, check := range original.Checklist.Checks {
				key := coverageSizeCheckKey{check.Code, check.Result, check.Message}
				expected[key] = append(expected[key], row.ID)
			}
		}
	}
	if len(variant.Assessment.Checklist.Checks) != len(expected) {
		t.Fatalf("variant checks=%d want %d unique checks", len(variant.Assessment.Checklist.Checks), len(expected))
	}
	for _, check := range variant.Assessment.Checklist.Checks {
		key := coverageSizeCheckKey{check.Code, check.Result, check.Message}
		want := expected[key]
		actual := append([]string(nil), check.SourceIDs...)
		slices.Sort(want)
		slices.Sort(actual)
		if len(want) == 0 || !reflect.DeepEqual(actual, want) {
			t.Fatalf("check %s contributors=%v want exact ResultRow IDs %v", check.Code, actual, want)
		}
		delete(expected, key)
	}
	if len(expected) != 0 {
		t.Fatalf("variant omitted %d checks", len(expected))
	}
}

func coverageSizeAssertRow(
	t *testing.T, row operatorconsole.ResultRow, batch profilederivation.MeasurementBatchFacts,
	original operatorconsole.MeasurementAssessment,
) {
	t.Helper()
	wantAssessment := original
	wantAssessment.Checklist.Checks = []operatorconsole.MeasurementCheck{}
	if !reflect.DeepEqual(row.Assessment, wantAssessment) {
		t.Fatalf("row %s assessment summary changed: got=%+v want=%+v", row.ID, row.Assessment, wantAssessment)
	}
	if row.Assessment.Status != operatorconsole.MeasurementCriteriaMet ||
		row.P95Milliseconds == nil || *row.P95Milliseconds != 0 ||
		row.TotalJoulesPerItem == nil || *row.TotalJoulesPerItem != 0 ||
		row.IdleWatts == nil || *row.IdleWatts != 0 ||
		row.Confidence == nil || row.Confidence.Lower != -3 || row.Confidence.Upper != 1 {
		t.Fatalf("row %s lost criteria, zero, or signed CI: %+v", row.ID, row)
	}
	wantSources := make([]string, 0, len(batch.Sources))
	for _, source := range batch.Sources {
		wantSources = append(wantSources, source.Store+"/"+source.Key)
	}
	if !reflect.DeepEqual(row.Sources, wantSources) {
		t.Fatalf("row %s raw sources changed: got=%v want=%v", row.ID, row.Sources, wantSources)
	}
}

func TestMeasurementCoverageChecklistKeepsRequestedBatchProvenance(t *testing.T) {
	response := buildMeasurementCoverage(
		coverageSizeBase(), coverageSizeNamespace, httpapi.MeasurementCoverageQuery{Batches: []int{4}},
	)
	rows := 0
	for _, cell := range response.Cells {
		for _, variant := range cell.Variants {
			rows += len(variant.ResultRows)
			selected := map[string]bool{}
			for _, row := range variant.ResultRows {
				if row.Batch == 4 {
					selected[row.ID] = true
				}
			}
			for _, check := range variant.Assessment.Checklist.Checks {
				if len(check.SourceIDs) != len(selected) {
					t.Fatalf("check %s does not retain every selected result row", check.Code)
				}
				for _, id := range check.SourceIDs {
					if !selected[id] {
						t.Fatalf("check %s references a batch outside the requested goal: %s", check.Code, id)
					}
				}
			}
		}
	}
	if rows != 240 {
		t.Fatalf("goal projection truncated result rows: %d", rows)
	}
}
