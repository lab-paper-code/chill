package operatorconsole

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildBatchEnergyOptimizationPreflightProjectsAResolvedPlan(t *testing.T) {
	t.Parallel()
	plan := batchEnergyOptimizationPreflightPlan(t)
	canonical, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	readAt := time.Date(2026, 8, 19, 6, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	campaign := edgev1alpha1.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "campaign"},
		Spec: edgev1alpha1.BatchEnergyOptimizationSpec{
			State:      edgev1alpha1.MeasurementStateSpec{TargetNode: "orangepi"},
			BatchSizes: []int32{1, 2, 4},
		},
	}

	view, err := BuildBatchEnergyOptimizationPreflight(BatchEnergyOptimizationPreflightSnapshot{
		ReadAt: readAt, Campaign: campaign, Plan: plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.APIVersion != BatchEnergyOptimizationPreflightAPIVersion ||
		view.Kind != "BatchEnergyOptimizationPreflight" || !view.ReadAt.Equal(readAt.UTC()) ||
		view.ReadAt.Location() != time.UTC || !view.Resolves || view.Reason != "" {
		t.Fatalf("envelope = %#v", view)
	}
	if view.Campaign.Namespace != campaign.Namespace || view.Campaign.Name != campaign.Name ||
		view.Campaign.Spec.State.TargetNode != campaign.Spec.State.TargetNode {
		t.Fatalf("campaign = %#v", view.Campaign)
	}
	if !bytes.Equal(view.Plan, canonical) {
		t.Fatalf("plan = %s, want byte-identical %s", view.Plan, canonical)
	}

	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["reason"]; exists {
		t.Fatalf("resolved encoding carries a reason: %s", encoded)
	}
}

func TestBuildBatchEnergyOptimizationPreflightProjectsAnUnresolvedIntent(t *testing.T) {
	t.Parallel()
	const reason = " referenced DeviceClass does not exist "

	view, err := BuildBatchEnergyOptimizationPreflight(BatchEnergyOptimizationPreflightSnapshot{
		ReadAt: time.Now(), Reason: reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Resolves || view.Reason != reason || view.Plan != nil {
		t.Fatalf("unresolved preflight = %#v", view)
	}

	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["plan"]; exists {
		t.Fatalf("unresolved encoding carries a plan: %s", encoded)
	}
}

func TestBuildBatchEnergyOptimizationPreflightRejectsContradictorySnapshots(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		snapshot BatchEnergyOptimizationPreflightSnapshot
		match    string
	}{
		"resolved with reason": {
			snapshot: BatchEnergyOptimizationPreflightSnapshot{
				Plan: batchEnergyOptimizationPreflightPlan(t), Reason: "resolution failed",
			},
			match: "resolved preflight carries no reason",
		},
		"unresolved without reason": {
			snapshot: BatchEnergyOptimizationPreflightSnapshot{},
			match:    "unresolved preflight requires a reason",
		},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := BuildBatchEnergyOptimizationPreflight(test.snapshot); err == nil ||
				!strings.Contains(err.Error(), test.match) {
				t.Fatalf("BuildBatchEnergyOptimizationPreflight() error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestPreflightBatchPlanPreservesDeclaredBatchOmissionAndAlwaysSerializes(t *testing.T) {
	for _, resolved := range []bool{false, true} {
		snapshot := BatchEnergyOptimizationPreflightSnapshot{Reason: "unresolved"}
		if resolved {
			snapshot.Plan, snapshot.Reason = batchEnergyOptimizationPreflightPlan(t), ""
		}
		view, err := BuildBatchEnergyOptimizationPreflight(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields["batchPlan"]) == 0 || bytes.Equal(fields["batchPlan"], []byte("null")) ||
			view.BatchPlan.RequestedBatches == nil || view.BatchPlan.MeasuredBatches == nil ||
			view.BatchPlan.InProgressBatches == nil || view.BatchPlan.MissingBatches == nil ||
			view.BatchPlan.SubsetAllowed || view.Campaign.Spec.BatchSizes != nil {
			t.Fatalf("ambiguous batch plan: %s", encoded)
		}
		if resolved && (!view.BatchPlan.SearchMayStopEarly ||
			!reflect.DeepEqual(view.BatchPlan.PlannedBatches, []int{1, 2, 4})) {
			t.Fatalf("declared batch plan = %#v", view.BatchPlan)
		}
		if !resolved && (view.BatchPlan.ConditionMatch != "unknown" || len(view.BatchPlan.RequestableBatches) != 0) {
			t.Fatalf("unresolved batch plan = %#v", view.BatchPlan)
		}
	}
}

func TestPreflightBatchPlanExactEvidenceAndChangedConditions(t *testing.T) {
	spec := batchEnergyOptimizationPreflightPlan(t).Shared()
	image := "registry.example/image@sha256:" + strings.Repeat("a", 64)
	spec.RuntimeImage, spec.Artifact.Image, spec.Preprocessor.Image = image, image, image
	spec.ActiveProtocol.MetricObserverImage, spec.ActiveProtocol.WorkloadObserverImage = image, image
	spec.IdleProtocol.MetricObserverImage, spec.IdleProtocol.WorkloadObserverImage = image, image
	original, err := profilederivation.NewCurvePlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	id := MeasurementVariantID(spec)
	for name, change := range map[string]func(*profilederivation.CurvePlanSpec){
		"exact":         func(*profilederivation.CurvePlanSpec) {},
		"CPU":           func(s *profilederivation.CurvePlanSpec) { s.CPU.Request = "6" },
		"dataset UID":   func(s *profilederivation.CurvePlanSpec) { s.ActiveProtocol.Dataset.UID = "new-uid" },
		"protocol":      func(s *profilederivation.CurvePlanSpec) { s.ActiveProtocol.WarmupIterations++ },
		"metric source": func(s *profilederivation.CurvePlanSpec) { s.MetricSource.Endpoint = "http://new-metrics" },
		"mutable image": func(s *profilederivation.CurvePlanSpec) { s.RuntimeImage = "registry.example/image:tag" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := original.Shared()
			change(&changed)
			plan, err := profilederivation.NewCurvePlan(changed)
			if err != nil {
				t.Fatal(err)
			}
			view, err := BuildBatchEnergyOptimizationPreflight(BatchEnergyOptimizationPreflightSnapshot{
				Plan: plan, VariantID: id, RequestsComplete: true,
				Campaign: edgev1alpha1.BatchEnergyOptimization{
					Spec: edgev1alpha1.BatchEnergyOptimizationSpec{BatchSizes: []int32{1, 2, 4}},
				},
				ExactEvidence: []MeasurementPreflightEvidence{{Condition: original.Shared(), Batches: []int{1, 1}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			got := view.BatchPlan
			if !got.SubsetAllowed || !got.SearchMayStopEarly {
				t.Fatalf("batch selection flags = %#v", got)
			}
			if name == MeasurementConditionExact {
				if got.ConditionMatch != MeasurementConditionExact || got.VariantID == nil || *got.VariantID != id ||
					!reflect.DeepEqual(got.MeasuredBatches, []int{1}) || !reflect.DeepEqual(got.MissingBatches, []int{2, 4}) {
					t.Fatalf("exact batch plan = %#v", got)
				}
			} else if got.ConditionMatch == MeasurementConditionExact || got.VariantID != nil || len(got.MeasuredBatches) != 0 ||
				!reflect.DeepEqual(got.MissingBatches, []int{1, 2, 4}) {
				t.Fatalf("changed condition retained old mapping: %#v", got)
			}
		})
	}
}

func batchEnergyOptimizationPreflightPlan(t *testing.T) profilederivation.CurvePlan {
	t.Helper()
	active := batchEnergyOptimizationPreflightProtocol("saturated")
	active.WarmupIterations = 20
	active.Dataset = &profilederivation.CurveDatasetContract{Name: "vqav2-val-subset", UID: "dataset-uid"}
	idle := batchEnergyOptimizationPreflightProtocol("idle-model-loaded")
	idle.SettlingDurationSeconds = 5
	plan, err := profilederivation.NewCurvePlan(profilederivation.CurvePlanSpec{
		Name: "mobilenet-rknn-curve", LogicalModel: "mobilenet-v2-050",
		DeviceClass: "orangepi-5-plus-16g",
		NodeUID:     "node-uid", RuntimeFamily: "rknnlite", Backend: "not-applicable",
		RuntimeImage: "registry.example/chill/runtime:v1", PowerMode: "fixed",
		CPU: profilederivation.CurveCPUContract{
			Policy: "allocatable", PolicyVersion: "v1", Request: "7", Limit: "8",
		},
		MetricSource: profilederivation.CurveMetricSourceContract{
			SourceKind: "prometheus", NodeName: "orangepi", Endpoint: "http://metrics",
			PowerMetric: "power_watts",
		},
		ActiveProtocol: active,
		IdleProtocol:   idle,
		Conversion: profilederivation.CurveConversionContract{
			TargetPlatform: "rk3588", Tool: "rknn-toolkit2", ToolVersion: "2.3.2",
		},
		Artifact: profilederivation.CurveArtifactContract{
			ExecutionPath: "rknnlite", Name: "artifact",
			Files: []string{"/artifact/model.rknn"}, Image: "registry.example/chill/artifact:v1",
		},
		Preprocessor: profilederivation.CurvePreprocessorContract{
			Name: "mobilenet-imagenet", Image: "registry.example/chill/preprocessor:v1", Architecture: "arm64",
			Path: "/artifact/mobilenet-nhwc", Inputs: []string{"image"}, Outputs: []string{"label"},
		},
		PlannedBatchSizes: []int{1, 2, 4},
	})
	if err != nil {
		t.Fatalf("build curve plan: %v", err)
	}
	return plan
}

func batchEnergyOptimizationPreflightProtocol(mode string) profilederivation.CurveProtocolContract {
	return profilederivation.CurveProtocolContract{
		WorkloadMode:               mode,
		DurationSeconds:            30,
		MetricIntervalMilliseconds: 1000,
		MetricTimeoutMilliseconds:  900,
		MetricDurationSeconds:      31,
		MetricObserverImage:        "registry.example/chill/metric-observer:v1",
		WorkloadObserverImage:      "registry.example/chill/workload-observer:v1",
		WorkloadSchemaVersion:      "chill.dacs.io/workload-contract.v1alpha1",
	}
}
