package queryapi

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
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
	got := BuildMeasurementCoverage(base, coverageSizeNamespace, CoverageQuery{})
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("coverage bytes=%d requests=%d runs=%d", len(raw), len(got.Entities.Requests), len(got.Entities.Runs))
	if len(raw) >= (8<<20)/2 {
		t.Errorf("coverage uses %d bytes; must remain below half the 8MiB response limit", len(raw))
	}
	if len(got.Entities.Runs) != 1200 || len(got.Entities.Requests) != 90 {
		t.Fatalf("entities truncated: runs=%d requests=%d", len(got.Entities.Runs), len(got.Entities.Requests))
	}
	coverageSizeAssertWire(t, raw)
	history := 0
	for _, cell := range got.Cells {
		history += len(cell.History)
		if len(cell.Assessment.Checklist.Checks) != 0 {
			t.Errorf("cell %s repeats checklist checks", cell.ID)
		}
	}
	if history != 90 || len(base.Catalog.MeasurementFacts) != 80 {
		t.Fatalf("retained history changed: history=%d", history)
	}
}

func coverageSizeBase() *CoverageBase {
	base := &CoverageBase{
		Checked: map[string]time.Time{}, Failures: map[string]error{},
		Catalog: CoverageCatalog{},
	}
	base.Catalog.MeasurementFacts = map[string]profilederivation.MeasurementFacts{}
	for _, kind := range []string{"inventory", "compatibility", "catalog", "BEO", "Run"} {
		base.Checked[kind] = time.Now().UTC()
	}
	base.Inventory.Classes = []InventoryClassView{{
		Name: coverageSizeClass, UID: "class-2df925e8-7142-45cd-8427-9f89c1155a80",
		PowerModes: []InventoryPowerModeView{{Name: "performance"}, {Name: "powersave"}},
		Nodes:      []InventoryNodeView{{Name: coverageSizeNode, UID: coverageSizeNodeUID}},
	}}
	for _, model := range []string{"resnet50", "mobilenet-v3"} {
		base.Inventory.Models = append(base.Inventory.Models, InventoryModelView{
			Name: model, UID: model + "-spec-uid",
			Spec: api.ModelSpecSpec{ExecutionPaths: []api.ModelExecutionPath{{Name: coverageSizePath}}},
		})
		base.Compatibility.Rows = append(base.Compatibility.Rows, ModelCompatibilityView{
			Selection: ModelCompatibilitySelection{
				ModelSpec: model, DeviceClass: coverageSizeClass, ExecutionPath: coverageSizePath, Node: coverageSizeNode,
			},
			Verdict: MeasurementSupportCompatible,
			Inputs: ModelCompatibilityInputs{
				Node: ModelCompatibilityObjectInput{Name: coverageSizeNode, UID: coverageSizeNodeUID},
			},
		})
	}
	for index := range 90 {
		task := coverageSizeRequest(index)
		base.Requests.Items = append(base.Requests.Items, task)
		if index < 80 {
			coverageSizeAddProfile(base, task, index)
		}
	}
	for index := range 1200 {
		task := base.Requests.Items[index%90]
		base.Runs.Items = append(base.Runs.Items, api.ProfilingRun{
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
			},
			Session: &api.BatchEnergyOptimizationSessionSpec{},
		},
		Status: api.BatchEnergyOptimizationStatus{Phase: "Complete", ObservedGeneration: 1},
	}
}

func coverageSizeAddProfile(base *CoverageBase, task api.BatchEnergyOptimization, index int) {
	condition := coverageSizeCondition(task, index/2)
	id := CoverageRequestID(task)
	facts := profilederivation.MeasurementFacts{
		Request: profilederivation.MeasurementRequestReference{
			Namespace: task.Namespace, Name: task.Name, UID: string(task.UID),
		},
		ResultReference: profilederivation.RawEvidenceIdentity{
			Store: "campaign-evidence", Key: id + "/results/batch-energy-result.json", Size: 32768,
		},
		Condition:   condition,
		PointPolicy: profilederivation.PointPolicy{Version: "point-policy.v2", MinimumTrials: 3, ConfidenceLevel: .95},
	}
	profile := CoverageProfile{
		ID: id, Condition: condition, NodeName: coverageSizeNode, MeasuredAt: time.Unix(int64(index+1), 0),
	}
	for _, batch := range []int{1, 4, 8} {
		facts.Batches = append(facts.Batches, coverageSizeBatch(task, condition, batch))
		profile.Batches = append(profile.Batches, batch)
	}
	base.Catalog.Profiles = append(base.Catalog.Profiles, profile)
	base.Catalog.MeasurementFacts[id] = facts
}

func coverageSizeCondition(task api.BatchEnergyOptimization, group int) profilederivation.CurvePlanSpec {
	active := profilederivation.CurveProtocolContract{
		WorkloadMode: "empirical-cadence-fixed-batch",
		Dataset: &profilederivation.CurveDatasetContract{
			Name: "imagenet-validation", UID: "dataset-uid",
		},
		WarmupIterations: 10 + group, SettlingDurationSeconds: 5, DurationSeconds: 30, DurationToleranceSeconds: 2,
		MetricIntervalMilliseconds: 100, MetricTimeoutMilliseconds: 1000, MetricDurationSeconds: 30,
		MetricObserverImage:   "registry.example.org/chill/power-observer:20261030",
		WorkloadObserverImage: "registry.example.org/chill/workload-observer:20261030",
		WorkloadSchemaVersion: "workload.v1",
	}
	idle := active
	idle.WorkloadMode, idle.Dataset = "idle-model-loaded", nil
	return profilederivation.CurvePlanSpec{
		Name: fmt.Sprintf("frozen-condition-%02d", group), LogicalModel: task.Spec.State.Candidate.ModelSpec,
		DeviceClass: coverageSizeClass, NodeUID: coverageSizeNodeUID, RuntimeFamily: "onnxruntime", Backend: "cpu",
		RuntimeImage: "registry.example.org/chill/onnxruntime:1.20.1", PowerMode: task.Spec.State.Candidate.PowerMode,
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
					CoverageRequestID(task), trialID, task.UID), Size: 16384,
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
