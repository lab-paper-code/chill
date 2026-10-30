package kube

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const preflightProjectorFailure = "referenced DeviceClass does not exist"

func TestBatchEnergyOptimizationPreflightQueryReturnsResolvedPlan(t *testing.T) {
	t.Parallel()
	plan := batchEnergyOptimizationPreflightQueryPlan(t)
	campaign := batchEnergyOptimizationPreflightQueryCampaign()
	projector := &recordingCurvePlanProjector{plan: plan}
	query, err := NewKubernetesBatchEnergyOptimizationPreflightQuery(projector.Project)
	if err != nil {
		t.Fatal(err)
	}
	readAt := time.Date(2026, 8, 19, 6, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	query.now = func() time.Time { return readAt }

	got, err := query.Read(context.Background(), campaign)
	if err != nil {
		t.Fatal(err)
	}
	if projector.calls != 1 || projector.campaign != campaign {
		t.Fatalf("projector calls/campaign = %d, %#v", projector.calls, projector.campaign)
	}
	if !got.ReadAt.Equal(readAt.UTC()) || got.ReadAt.Location() != time.UTC ||
		got.Campaign.Namespace != campaign.Namespace || got.Campaign.Name != campaign.Name ||
		!reflect.DeepEqual(got.Plan.Shared(), plan.Shared()) || got.Reason != "" {
		t.Fatalf("snapshot = %#v", got)
	}

	got.Campaign.Spec.BatchSizes[0] = 128
	if campaign.Spec.BatchSizes[0] == 128 {
		t.Fatal("query returned a mutable alias of the submitted campaign")
	}
}

func TestBatchEnergyOptimizationPreflightQueryRecordsProjectionFailure(t *testing.T) {
	t.Parallel()
	campaign := batchEnergyOptimizationPreflightQueryCampaign()
	projector := &recordingCurvePlanProjector{
		plan: batchEnergyOptimizationPreflightQueryPlan(t),
		err:  errors.New(preflightProjectorFailure),
	}
	query, err := NewKubernetesBatchEnergyOptimizationPreflightQuery(projector.Project)
	if err != nil {
		t.Fatal(err)
	}

	got, err := query.Read(context.Background(), campaign)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if got.Reason != preflightProjectorFailure || got.Plan.SchemaVersion() != "" ||
		got.Campaign.Namespace != campaign.Namespace || got.Campaign.Name != campaign.Name {
		t.Fatalf("snapshot = %#v", got)
	}
}

func TestBatchEnergyOptimizationPreflightQueryRejectsInvalidCampaign(t *testing.T) {
	t.Parallel()
	tests := map[string]*edgev1alpha1.BatchEnergyOptimization{
		"nil campaign":        nil,
		"empty namespace":     batchEnergyOptimizationPreflightQueryCampaign(),
		"spaced namespace":    batchEnergyOptimizationPreflightQueryCampaign(),
		"empty name":          batchEnergyOptimizationPreflightQueryCampaign(),
		"space-suffixed name": batchEnergyOptimizationPreflightQueryCampaign(),
	}
	tests["empty namespace"].Namespace = ""
	tests["spaced namespace"].Namespace = " " + consoleTestNamespace
	tests["empty name"].Name = ""
	tests["space-suffixed name"].Name += " "

	for name, campaign := range tests {
		name, campaign := name, campaign
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			projector := &recordingCurvePlanProjector{}
			query, err := NewKubernetesBatchEnergyOptimizationPreflightQuery(projector.Project)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := query.Read(context.Background(), campaign); err == nil {
				t.Fatal("invalid campaign was accepted")
			}
			if projector.calls != 0 {
				t.Fatalf("invalid campaign reached projector: calls = %d", projector.calls)
			}
		})
	}
}

func TestNewBatchEnergyOptimizationPreflightQueryRejectsNilProjector(t *testing.T) {
	t.Parallel()
	if _, err := NewKubernetesBatchEnergyOptimizationPreflightQuery(nil); err == nil ||
		!strings.Contains(err.Error(), "curve-plan projector is required") {
		t.Fatalf("constructor error = %v", err)
	}
}

type recordingCurvePlanProjector struct {
	plan     profilederivation.CurvePlan
	err      error
	campaign *edgev1alpha1.BatchEnergyOptimization
	calls    int
}

func (p *recordingCurvePlanProjector) Project(
	_ context.Context,
	campaign *edgev1alpha1.BatchEnergyOptimization,
) (profilederivation.CurvePlan, error) {
	p.calls++
	p.campaign = campaign
	return p.plan, p.err
}

func batchEnergyOptimizationPreflightQueryCampaign() *edgev1alpha1.BatchEnergyOptimization {
	return &edgev1alpha1.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{Namespace: consoleTestNamespace, Name: "campaign"},
		Spec: edgev1alpha1.BatchEnergyOptimizationSpec{
			State:      edgev1alpha1.MeasurementStateSpec{TargetNode: "orangepi"},
			BatchSizes: []int32{1, 2, 4},
		},
	}
}

func batchEnergyOptimizationPreflightQueryPlan(t *testing.T) profilederivation.CurvePlan {
	t.Helper()
	active := batchEnergyOptimizationPreflightQueryProtocol("saturated")
	active.WarmupIterations = 20
	active.Dataset = &profilederivation.CurveDatasetContract{Name: "vqav2-val-subset", UID: "dataset-uid"}
	idle := batchEnergyOptimizationPreflightQueryProtocol("idle-model-loaded")
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

func batchEnergyOptimizationPreflightQueryProtocol(mode string) profilederivation.CurveProtocolContract {
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
