package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPreflightEnrichmentUsesExactCatalogAndCurrentSourceSpec(t *testing.T) {
	plan := measurementPreflightTestPlan(t)
	for _, mode := range []string{"exact", "changed spec", "recreated request", "other namespace", "active exact", "active stale", "active missing plan"} {
		t.Run(mode, func(t *testing.T) {
			request := &api.BatchEnergyOptimization{
				ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "source", UID: "uid"},
				Spec:       api.BatchEnergyOptimizationSpec{BatchSizes: []int32{1, 2, 4}},
			}
			proposed := *request.DeepCopy()
			proposed.Name = "new-request"
			view, err := operatorconsole.BuildBatchEnergyOptimizationPreflight(operatorconsole.BatchEnergyOptimizationPreflightSnapshot{
				Plan: plan, Campaign: proposed, RequestsComplete: true,
			})
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
				request.Generation, request.Status.ObservedGeneration, request.Status.Phase = 1, 1, "Measuring"
				if mode == "active stale" {
					request.Status.ObservedGeneration = 0
				}
			}
			scheduler := &schedulerService{}
			profiles := []serving.Profile{{ID: profileID, Condition: plan.Shared(), Batches: []serving.BatchProfile{{Batch: 1}}}}
			if active {
				profiles = nil
			}
			scheduler.profiles.Store(&httpapi.ProfilesView{CatalogView: servingkube.CatalogView{Profiles: profiles}})
			service := &measurementService{
				client:    fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(request).Build(),
				scheduler: scheduler, opts: options{servingNamespace: "allowed", storeName: "evidence", maxEvidenceBytes: 1 << 20},
			}
			if active && mode != "active missing plan" {
				raw, err := plan.CanonicalJSON()
				if err != nil {
					t.Fatal(err)
				}
				service.objects = &operationProjectionStore{payload: raw}
			}
			payload, err := json.Marshal(view)
			if err != nil {
				t.Fatal(err)
			}
			id := operatorconsole.MeasurementVariantID(plan.Shared())
			response := &http.Response{
				Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(payload)),
				Request: httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/allowed/batch-energy-optimizations/preflight?variantId="+id, nil),
			}
			if err := service.enrichPreflightResponse(response); err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
				t.Fatal(err)
			}
			if mode == "exact" {
				if view.BatchPlan.ConditionMatch != "exact" || view.BatchPlan.VariantID == nil ||
					!reflect.DeepEqual(view.BatchPlan.MeasuredBatches, []int{1}) || !reflect.DeepEqual(view.BatchPlan.MissingBatches, []int{2, 4}) {
					t.Fatalf("exact catalog facts lost: %#v", view.BatchPlan)
				}
			} else if mode == "active exact" {
				if view.BatchPlan.ConditionMatch != "exact" || view.BatchPlan.VariantID == nil ||
					!reflect.DeepEqual(view.BatchPlan.InProgressBatches, []int{1, 2, 4}) ||
					len(view.BatchPlan.MeasuredBatches) != 0 || len(view.BatchPlan.MissingBatches) != 0 {
					t.Fatalf("frozen in-progress plan lost: %#v", view.BatchPlan)
				}
			} else if view.BatchPlan.ConditionMatch == "exact" || view.BatchPlan.VariantID != nil || len(view.BatchPlan.MeasuredBatches) != 0 || len(view.BatchPlan.InProgressBatches) != 0 {
				t.Fatalf("unconfirmed lineage became exact: %#v", view.BatchPlan)
			}
		})
	}
}

func TestPublicPreflightRequiresNamespaceListBeforePrivateRead(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "allowed"}[allowed], func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.RawQuery != "variantId=selected" || r.Header.Get("Authorization") != "" {
					t.Error("preflight display context or private authentication boundary changed")
				}
				view, err := operatorconsole.BuildBatchEnergyOptimizationPreflight(operatorconsole.BatchEnergyOptimizationPreflightSnapshot{
					Campaign: api.BatchEnergyOptimization{ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "new-request"}}, Reason: "actual unresolved request",
				})
				if err != nil {
					t.Fatal(err)
				}
				_ = json.NewEncoder(w).Encode(view)
			}))
			defer upstream.Close()
			native := kubefake.NewSimpleClientset()
			native.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
					Authenticated: true, User: authenticationv1.UserInfo{Username: "operator"},
				}}, nil
			})
			resourceReviews := 0
			native.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				review := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
				permitted := true
				if resource := review.Spec.ResourceAttributes; resource != nil {
					resourceReviews++
					if resource.Namespace != "allowed" || resource.Group != "edge.dacs.io" || resource.Resource != "batchenergyoptimizations" || resource.Verb != "list" {
						t.Errorf("wrong preflight resource authorization: %#v", resource)
					}
					permitted = allowed
				}
				return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: permitted}}, nil
			})
			opts := options{consoleURL: upstream.URL, servingNamespace: "allowed", requestTimeout: time.Second, maxRequestBytes: 1024}
			handler, err := composeChillServer(http.NotFoundHandler(), &servingkube.Authorizer{Client: native}, opts, &measurementService{opts: opts})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/allowed/batch-energy-optimizations/preflight?variantId=selected", strings.NewReader("{}"))
			request.Header.Set("Authorization", "Bearer token")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			wantStatus, wantCalls := http.StatusForbidden, 0
			if allowed {
				wantStatus, wantCalls = http.StatusOK, 1
			}
			if response.Code != wantStatus || calls != wantCalls || resourceReviews != 1 {
				t.Fatalf("status=%d calls=%d reviews=%d body=%s", response.Code, calls, resourceReviews, response.Body.String())
			}
		})
	}
}

func measurementPreflightTestPlan(t *testing.T) derivation.CurvePlan {
	t.Helper()
	image := "registry.example/image@sha256:" + strings.Repeat("a", 64)
	active := derivation.CurveProtocolContract{
		WorkloadMode: "saturated", Dataset: &derivation.CurveDatasetContract{Name: "dataset", UID: "dataset-uid"},
		DurationSeconds: 30, MetricIntervalMilliseconds: 1000, MetricTimeoutMilliseconds: 900, MetricDurationSeconds: 31,
		MetricObserverImage: image, WorkloadObserverImage: image, WorkloadSchemaVersion: "chill.dacs.io/workload-contract.v1alpha1",
	}
	idle := active
	idle.WorkloadMode, idle.Dataset, idle.SettlingDurationSeconds = "idle-model-loaded", nil, 5
	plan, err := derivation.NewCurvePlan(derivation.CurvePlanSpec{
		Name: "source", LogicalModel: "model", DeviceClass: "class", NodeUID: "node-uid",
		RuntimeFamily: "rknnlite", Backend: "not-applicable", RuntimeImage: image, PowerMode: "fixed",
		CPU:            derivation.CurveCPUContract{Policy: "allocatable", PolicyVersion: "v1", Request: "7", Limit: "8"},
		MetricSource:   derivation.CurveMetricSourceContract{SourceKind: "prometheus", NodeName: "node", Endpoint: "http://metrics", PowerMetric: "power_watts"},
		ActiveProtocol: active, IdleProtocol: idle,
		Conversion:        derivation.CurveConversionContract{TargetPlatform: "rk3588", Tool: "rknn-toolkit2", ToolVersion: "2.3.2"},
		Artifact:          derivation.CurveArtifactContract{ExecutionPath: "rknnlite", Name: "artifact", Files: []string{"/artifact/model.rknn"}, Image: image},
		Preprocessor:      derivation.CurvePreprocessorContract{Name: "preprocessor", Image: image, Architecture: "arm64", Path: "/artifact/preprocessor", Inputs: []string{"image"}, Outputs: []string{"label"}},
		PlannedBatchSizes: []int{1, 2, 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
