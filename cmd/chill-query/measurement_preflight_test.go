package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const measurementPreflightTestPath = "/api/v1/namespaces/allowed/batch-energy-optimizations/preflight"

func TestPreflightEnrichmentUsesExactCatalogAndCurrentSourceSpec(t *testing.T) {
	plan := measurementPreflightTestPlan(t)
	modes := []string{
		queryapi.MeasurementConditionExact, "changed spec", "recreated request", "other namespace",
		"active exact", "active stale", "active missing plan",
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			request := &api.BatchEnergyOptimization{
				ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "source", UID: "uid"},
				Spec:       api.BatchEnergyOptimizationSpec{BatchSizes: []int32{1, 2, 4}},
			}
			proposed := *request.DeepCopy()
			proposed.Name = "new-request"
			snapshot := queryapi.BatchEnergyOptimizationPreflightSnapshot{
				Plan: plan, Campaign: proposed, RequestsComplete: true,
			}
			view, err := queryapi.BuildBatchEnergyOptimizationPreflight(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "changed spec" {
				request.Spec.TrialsPerBatch++
			}
			if mode == "recreated request" {
				request.UID = "replacement-uid"
			}
			profileID := "allowed/source/uid"
			if mode == "other namespace" {
				profileID = "other/source/uid"
			}
			active := strings.HasPrefix(mode, "active ")
			if active {
				request.Generation, request.Status.ObservedGeneration, request.Status.Phase = 1, 1, measurementPhaseMeasuring
				if mode == "active stale" {
					request.Status.ObservedGeneration = 0
				}
			}
			profiles := []serving.Profile{{ID: profileID, Condition: plan.Shared(), Batches: []serving.BatchProfile{{Batch: 1}}}}
			if active {
				profiles = nil
			}
			read := servingkube.ProjectCatalogRead(servingkube.CatalogView{Profiles: profiles}, "", nil, []string{profileID})

			service := &measurementService{
				client:  fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(request).Build(),
				catalog: func(context.Context) (servingkube.CatalogRead, error) { return read, nil }, opts: options{servingNamespace: "allowed", storeName: "evidence", maxEvidenceBytes: 1 << 20},
			}
			if active && mode != "active missing plan" {
				raw, err := plan.CanonicalJSON()
				if err != nil {
					t.Fatal(err)
				}
				service.objects = &operationProjectionStore{payload: raw}
			}
			snapshot.VariantID = queryapi.MeasurementVariantID(plan.Shared())
			view, err = queryapi.BuildBatchEnergyOptimizationPreflight(service.completePreflight(context.Background(), snapshot))
			if err != nil {
				t.Fatal(err)
			}

			assertMeasurementPreflightBatchPlan(t, mode, view.BatchPlan)
		})
	}
}

func assertMeasurementPreflightBatchPlan(t *testing.T, mode string, plan queryapi.MeasurementBatchPlan) {
	t.Helper()
	switch mode {
	case queryapi.MeasurementConditionExact:
		if plan.ConditionMatch != queryapi.MeasurementConditionExact || plan.VariantID == nil ||
			!reflect.DeepEqual(plan.MeasuredBatches, []int{1}) ||
			!reflect.DeepEqual(plan.MissingBatches, []int{2, 4}) {
			t.Fatalf("exact catalog facts lost: %#v", plan)
		}
	case "active exact":
		if plan.ConditionMatch != queryapi.MeasurementConditionExact || plan.VariantID == nil ||
			!reflect.DeepEqual(plan.InProgressBatches, []int{1, 2, 4}) ||
			len(plan.MeasuredBatches) != 0 || len(plan.MissingBatches) != 0 {
			t.Fatalf("frozen in-progress plan lost: %#v", plan)
		}
	default:
		if plan.ConditionMatch == queryapi.MeasurementConditionExact || plan.VariantID != nil ||
			len(plan.MeasuredBatches) != 0 || len(plan.InProgressBatches) != 0 {
			t.Fatalf("unconfirmed lineage became exact: %#v", plan)
		}
	}
}

func measurementPreflightTestPlan(t *testing.T) derivation.CurvePlan {
	t.Helper()
	image := "registry.example/image@sha256:" + strings.Repeat("a", 64)
	active := derivation.CurveProtocolContract{
		WorkloadMode: "saturated", Dataset: &derivation.CurveDatasetContract{Name: "dataset", UID: "dataset-uid"},
		DurationSeconds: 30, MetricIntervalMilliseconds: 1000, MetricTimeoutMilliseconds: 900, MetricDurationSeconds: 31,
		MetricObserverImage: image, WorkloadObserverImage: image,
		WorkloadSchemaVersion: "chill.dacs.io/workload-contract.v1alpha1",
	}
	idle := active
	idle.WorkloadMode, idle.Dataset, idle.SettlingDurationSeconds = "idle-model-loaded", nil, 5
	plan, err := derivation.NewCurvePlan(derivation.CurvePlanSpec{
		Name: "source", LogicalModel: "model", DeviceClass: "class", NodeUID: "node-uid",
		RuntimeFamily: "rknnlite", Backend: "not-applicable", RuntimeImage: image, PowerMode: "fixed",
		CPU: derivation.CurveCPUContract{Policy: "allocatable", PolicyVersion: "v1", Request: "7", Limit: "8"},
		MetricSource: derivation.CurveMetricSourceContract{
			SourceKind: "prometheus", NodeName: "node", Endpoint: "http://metrics", PowerMetric: "power_watts",
		},
		ActiveProtocol: active, IdleProtocol: idle,
		Conversion: derivation.CurveConversionContract{
			TargetPlatform: "rk3588", Tool: "rknn-toolkit2", ToolVersion: "2.3.2",
		},
		Artifact: derivation.CurveArtifactContract{
			ExecutionPath: "rknnlite", Name: "artifact", Files: []string{"/artifact/model.rknn"}, Image: image,
		},
		Preprocessor: derivation.CurvePreprocessorContract{
			Name: "preprocessor", Image: image, Architecture: "arm64", Path: "/artifact/preprocessor",
			Inputs: []string{"image"}, Outputs: []string{"label"},
		},
		PlannedBatchSizes: []int{1, 2, 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
