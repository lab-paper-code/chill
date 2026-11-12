package queryapi

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
			State: edgev1alpha1.MeasurementStateSpec{TargetNode: "orangepi"},
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
			view.BatchPlan.MeasuredBatches == nil || view.BatchPlan.InProgressBatches == nil || view.BatchPlan.MissingBatches == nil {
			t.Fatalf("ambiguous batch plan: %s", encoded)
		}
		if resolved && (!view.BatchPlan.SearchMayStopEarly ||
			!reflect.DeepEqual(view.BatchPlan.PlannedBatches, []int{1, 2, 4})) {
			t.Fatalf("declared batch plan = %#v", view.BatchPlan)
		}
		if !resolved && view.BatchPlan.ConditionMatch != "unknown" {
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
		"protocol":      func(s *profilederivation.CurvePlanSpec) { s.ActiveProtocol.WarmupIterations++ },
		"metric source": func(s *profilederivation.CurvePlanSpec) { s.MetricSource.Endpoint = "http://new-metrics" },
		"runtime image": func(s *profilederivation.CurvePlanSpec) {
			s.RuntimeImage = "registry.example/image@sha256:" + strings.Repeat("b", 64)
		},
		"artifact image":     func(s *profilederivation.CurvePlanSpec) { s.Artifact.Image = "registry.example/artifact:new" },
		"preprocessor image": func(s *profilederivation.CurvePlanSpec) { s.Preprocessor.Image = "registry.example/preprocessor:new" },
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
				Campaign:      edgev1alpha1.BatchEnergyOptimization{},
				ExactEvidence: []MeasurementPreflightEvidence{{Condition: original.Shared(), Batches: []int{1, 1}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			got := view.BatchPlan
			if !got.SearchMayStopEarly {
				t.Fatalf("batch selection flags = %#v", got)
			}
			if name == "exact" || name == "protocol" || name == "metric source" {
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
	active := batchEnergyOptimizationPreflightProtocol("empirical-cadence-fixed-batch")
	active.WarmupIterations = 20
	active.Dataset = &profilederivation.CurveDatasetContract{Name: "vqav2-val-subset", UID: "dataset-uid"}
	idle := batchEnergyOptimizationPreflightProtocol("idle-model-loaded")
	idle.SettlingDurationSeconds = 5
	plan, err := profilederivation.NewCurvePlan(profilederivation.CurvePlanSpec{
		Name: "mobilenet-rknn-curve", LogicalModel: "mobilenet-v2-050",
		DeviceClass: "orangepi-5-plus-16g",
		NodeUID:     "node-uid", RuntimeFamily: "rknnlite", Backend: "not-applicable",
		RuntimeImage: "registry.example/chill/runtime@sha256:" + strings.Repeat("a", 64), PowerMode: "fixed",
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

func TestObserverImagesDoNotInvalidateMeasuredBatches(t *testing.T) {
	original := batchEnergyOptimizationPreflightPlan(t)
	spec := original.Shared()
	spec.ActiveProtocol.MetricObserverImage = "registry.example/metric:new"
	spec.ActiveProtocol.WorkloadObserverImage = "registry.example/workload:new"
	spec.IdleProtocol.MetricObserverImage = "registry.example/metric:other"
	spec.IdleProtocol.WorkloadObserverImage = "registry.example/workload:other"
	plan, err := profilederivation.NewCurvePlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	got := BuildMeasurementBatchPlan(BatchEnergyOptimizationPreflightSnapshot{
		Plan: plan, RequestsComplete: true, VariantID: MeasurementVariantID(original.Shared()),
		ExactEvidence: []MeasurementPreflightEvidence{{Condition: original.Shared(), Batches: []int{1, 2, 4}}},
	})
	if got.ConditionMatch != MeasurementConditionExact || got.VariantID == nil ||
		!reflect.DeepEqual(got.MeasuredBatches, []int{1, 2, 4}) || len(got.MissingBatches) != 0 {
		t.Fatalf("image versions invalidated measured batches: %+v", got)
	}
	for _, reason := range got.Reasons {
		if reason.Code == "ImmutableImagesUnconfirmed" || reason.Code == "VariantConditionChanged" {
			t.Fatalf("image validity gate survived: %+v", reason)
		}
	}
}

func TestPreflightMatchesMeasuredRuntimeVersionOnlyWhenPlanFixesIt(t *testing.T) {
	original := batchEnergyOptimizationPreflightPlan(t)
	measured := original.Shared()
	measured.RuntimeVersion = "measured-version"
	measuredID := MeasurementVariantID(measured)
	for _, version := range []string{"", "measured-version", "different-version"} {
		t.Run(version, func(t *testing.T) {
			planned := original.Shared()
			planned.RuntimeVersion = version
			plan, err := profilederivation.NewCurvePlan(planned)
			if err != nil {
				t.Fatal(err)
			}
			got := BuildMeasurementBatchPlan(BatchEnergyOptimizationPreflightSnapshot{
				Plan: plan, VariantID: measuredID, RequestsComplete: true,
				ExactEvidence: []MeasurementPreflightEvidence{{Condition: measured, Batches: []int{1}}},
			})
			if version == "different-version" {
				if len(got.MeasuredBatches) != 0 || got.VariantID != nil {
					t.Fatalf("different fixed version borrowed measured batches: %+v", got)
				}
			} else if got.ConditionMatch != MeasurementConditionExact || got.VariantID == nil ||
				*got.VariantID != measuredID || !reflect.DeepEqual(got.MeasuredBatches, []int{1}) {
				t.Fatalf("unobserved planned version stranded measured batches: %+v", got)
			}
		})
	}
}

func TestPreflightKeepsCompletedVariantWithInProgressEvidence(t *testing.T) {
	plan := batchEnergyOptimizationPreflightPlan(t)
	measured := plan.Shared()
	measured.RuntimeVersion = "measured-version"
	measuredID := MeasurementVariantID(measured)
	completed := MeasurementPreflightEvidence{Condition: measured, Batches: []int{1}}
	pending := MeasurementPreflightEvidence{Condition: plan.Shared(), Batches: []int{2}, InProgress: true}
	for _, selected := range []string{"", measuredID} {
		for _, evidence := range [][]MeasurementPreflightEvidence{{completed, pending}, {pending, completed}} {
			got := BuildMeasurementBatchPlan(BatchEnergyOptimizationPreflightSnapshot{
				Plan: plan, VariantID: selected, RequestsComplete: true, ExactEvidence: evidence,
			})
			if got.VariantID == nil || *got.VariantID != measuredID ||
				!reflect.DeepEqual(got.MeasuredBatches, []int{1}) ||
				!reflect.DeepEqual(got.InProgressBatches, []int{2}) ||
				!reflect.DeepEqual(got.MissingBatches, []int{4}) {
				t.Fatalf("in-progress plan replaced completed measurement identity: %+v", got)
			}
			for _, reason := range got.Reasons {
				if reason.Code == "VariantConditionChanged" {
					t.Fatalf("in-progress plan invalidated completed variant: %+v", got)
				}
			}
		}
	}
}

func TestPreflightComparesPlainRuntimeImageTagsWithObservedVersion(t *testing.T) {
	const image = "registry.example/runtime:measured"
	spec := batchEnergyOptimizationPreflightPlan(t).Shared()
	spec.RuntimeImage = image
	plan, err := profilederivation.NewCurvePlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, measuredImage := range []string{image, "registry.example/runtime:different"} {
		t.Run(measuredImage, func(t *testing.T) {
			measured := plan.Shared()
			measured.RuntimeImage, measured.RuntimeVersion = measuredImage, "observed-version"
			id := MeasurementVariantID(measured)
			got := BuildMeasurementBatchPlan(BatchEnergyOptimizationPreflightSnapshot{
				Plan: plan, VariantID: id, RequestsComplete: true,
				ExactEvidence: []MeasurementPreflightEvidence{{Condition: measured, Batches: []int{1}}},
			})
			if measuredImage == image {
				if got.ConditionMatch != MeasurementConditionExact || got.VariantID == nil ||
					*got.VariantID != id || !reflect.DeepEqual(got.MeasuredBatches, []int{1}) {
					t.Fatalf("plain image tag stranded observed runtime facts: %+v", got)
				}
			} else if got.VariantID != nil || len(got.MeasuredBatches) != 0 {
				t.Fatalf("different runtime image tag borrowed measured batches: %+v", got)
			}
		})
	}
}
