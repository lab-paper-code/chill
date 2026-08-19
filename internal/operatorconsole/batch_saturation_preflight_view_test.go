package operatorconsole

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profilederivation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildBatchSaturationPreflightProjectsAResolvedPlan(t *testing.T) {
	t.Parallel()
	plan := batchSaturationPreflightPlan(t)
	canonical, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	readAt := time.Date(2026, 8, 19, 6, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	campaign := edgev1alpha1.BatchSaturation{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "campaign"},
		Spec: edgev1alpha1.BatchSaturationSpec{
			State:      edgev1alpha1.MeasurementStateSpec{TargetNode: "orangepi"},
			BatchSizes: []int32{1, 2, 4},
		},
	}

	view, err := BuildBatchSaturationPreflight(BatchSaturationPreflightSnapshot{
		ReadAt: readAt, Campaign: campaign, Plan: plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.APIVersion != BatchSaturationPreflightAPIVersion ||
		view.Kind != "BatchSaturationPreflight" || !view.ReadAt.Equal(readAt.UTC()) ||
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

func TestBuildBatchSaturationPreflightProjectsAnUnresolvedIntent(t *testing.T) {
	t.Parallel()
	const reason = " referenced DeviceClass does not exist "

	view, err := BuildBatchSaturationPreflight(BatchSaturationPreflightSnapshot{
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

func TestBuildBatchSaturationPreflightRejectsContradictorySnapshots(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		snapshot BatchSaturationPreflightSnapshot
		match    string
	}{
		"resolved with reason": {
			snapshot: BatchSaturationPreflightSnapshot{
				Plan: batchSaturationPreflightPlan(t), Reason: "resolution failed",
			},
			match: "resolved preflight carries no reason",
		},
		"unresolved without reason": {
			snapshot: BatchSaturationPreflightSnapshot{},
			match:    "unresolved preflight requires a reason",
		},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := BuildBatchSaturationPreflight(test.snapshot); err == nil ||
				!strings.Contains(err.Error(), test.match) {
				t.Fatalf("BuildBatchSaturationPreflight() error = %v, want %q", err, test.match)
			}
		})
	}
}

func batchSaturationPreflightPlan(t *testing.T) profilederivation.CurvePlan {
	t.Helper()
	active := batchSaturationPreflightProtocol("saturated")
	active.WarmupIterations = 20
	idle := batchSaturationPreflightProtocol("idle-model-loaded")
	idle.InputGenerator = "none"
	idle.OutputValidation = "none"
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
			Path: "/artifact/model.rknn", Image: "registry.example/chill/artifact:v1",
		},
		PlannedBatchSizes: []int{1, 2, 4},
		Tensor: profilederivation.CurveTensorContract{
			Layout: "NHWC", DataType: "float32", InputSampleShape: []int{224, 224, 3},
		},
	})
	if err != nil {
		t.Fatalf("build curve plan: %v", err)
	}
	return plan
}

func batchSaturationPreflightProtocol(mode string) profilederivation.CurveProtocolContract {
	return profilederivation.CurveProtocolContract{
		WorkloadMode: mode, InputGenerator: "seeded", OutputValidation: "shape",
		DurationSeconds:            30,
		MetricIntervalMilliseconds: 1000,
		MetricTimeoutMilliseconds:  900,
		MetricDurationSeconds:      31,
		MetricObserverImage:        "registry.example/chill/metric-observer:v1",
		WorkloadObserverImage:      "registry.example/chill/workload-observer:v1",
		WorkloadSchemaVersion:      "chill.dacs.io/workload-contract.v1alpha1",
	}
}
