package measurement

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"testing"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	"github.com/lab-paper-code/chill/internal/queryapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	measurementAcceptedTest  = "accepted"
	measurementDryRunTest    = "dry run"
	measurementFinalizerTest = "finalizer lifecycle"
	measurementInvalidRunSpecTest = "invalid Run spec"
)

func measurementScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batch.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func TestMeasurementSubmitPreflightBeforeNativeCreate(t *testing.T) {
	for _, mode := range []string{"reject", "outage", "wrong identity", measurementAcceptedTest, measurementDryRunTest} {
		t.Run(mode, func(t *testing.T) {
			preflights, creates := 0, 0
			task := api.BatchEnergyOptimization{
				TypeMeta:   meta.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "BatchEnergyOptimization"},
				ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "task"},
			}
			query := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				preflights++
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer mutation-caller" ||
					r.URL.Path != "/api/v1/namespaces/allowed/batch-energy-optimizations/preflight" {
					t.Fatal("unexpected preflight transport")
				}
				if mode == "outage" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				name := task.Name
				if mode == "wrong identity" {
					name = "another"
				}
				result := queryapi.BatchEnergyOptimizationPreflight{
					APIVersion: queryapi.BatchEnergyOptimizationPreflightAPIVersion,
					Kind:       "BatchEnergyOptimizationPreflight",
					Campaign: queryapi.BatchEnergyOptimizationPreflightCampaign{
						Namespace: task.Namespace, Name: name,
					},
					Resolves: mode != "reject", Reason: "actual unresolved input",
				}
				if err := json.NewEncoder(w).Encode(result); err != nil {
					t.Error(err)
				}
			}))
			defer query.Close()
			native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
					creates++
					if preflights != 1 {
						t.Fatal("create before production preflight")
					}
					options := &client.CreateOptions{}
					for _, opt := range opts {
						opt.ApplyToCreate(options)
					}
					if mode == measurementDryRunTest {
						if !reflect.DeepEqual(options.DryRun, []string{meta.DryRunAll}) {
							t.Fatal("dry run was not forwarded to Kubernetes")
						}
						return nil
					}
					return c.Create(ctx, object, opts...)
				},
			}).Build()
			opts := Config{ServingNamespace: "allowed"}
			origin, err := url.Parse(query.URL)
			if err != nil {
				t.Fatal(err)
			}
			service := New(native, kubefake.NewSimpleClientset(), nil, nil, opts, origin, &http.Client{Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(context.Background(), QueryBearerContextKey{}, "Bearer mutation-caller")
			result, err := service.Submit(ctx, task, mode == measurementDryRunTest)
			accepted := mode == measurementAcceptedTest || mode == measurementDryRunTest
			if (err == nil) != accepted || (creates == 1) != accepted || preflights != 1 {
				t.Fatalf("result=%+v err=%v preflights=%d creates=%d", result, err, preflights, creates)
			}
			stored := &api.BatchEnergyOptimization{}
			getErr := native.Get(context.Background(), client.ObjectKeyFromObject(&task), stored)
			if mode == measurementAcceptedTest {
				if getErr != nil || result.Request != "Accepted" || result.Task == nil {
					t.Fatal("accepted create missing native object")
				}
			} else if !apierrors.IsNotFound(getErr) {
				t.Fatal("rejected or dry-run create persisted")
			}
			if mode == measurementDryRunTest && result.Request != "NOT_SUBMITTED" {
				t.Fatal("dry run claimed submission")
			}
		})
	}
}

func TestMeasurementMutationNamespaceEscape(t *testing.T) {
	service := &Service{opts: Config{ProfileNamespaces: []string{"allowed"}}}
	escaped := api.BatchEnergyOptimization{ObjectMeta: meta.ObjectMeta{Namespace: "outside", Name: "hidden"}}
	if _, err := service.Submit(context.Background(), escaped, false); !errors.Is(err, servingkube.ErrForbidden) {
		t.Fatal("namespace submit escaped before preflight")
	}
	_, err := service.Delete(context.Background(), "outside", "hidden", "uid", "1")
	if !errors.Is(err, servingkube.ErrForbidden) {
		t.Fatal("namespace delete escaped")
	}
}

func assertNativeMeasurementDeleteOptions(t *testing.T, task *api.BatchEnergyOptimization, opts []client.DeleteOption) {
	t.Helper()
	options := &client.DeleteOptions{}
	for _, opt := range opts {
		opt.ApplyToDelete(options)
	}
	if options.Preconditions == nil || *options.Preconditions.UID != task.UID ||
		*options.Preconditions.ResourceVersion != "7" || options.PropagationPolicy == nil ||
		*options.PropagationPolicy != meta.DeletePropagationBackground {
		t.Fatal("native UID/RV or GC lifecycle lost")
	}
}

func TestMeasurementDeleteFencesIdentityAndNativeFinalizerLifecycle(t *testing.T) {
	modes := []string{
		"stale version", "replaced UID", "physical source",
		"active measurement", measurementFinalizerTest, "deleted", measurementInvalidRunSpecTest,
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			task := terminalDeletionTask()
			if mode == measurementInvalidRunSpecTest {
				task.Status.Phase = measurementPhaseFailed
				task.Status.Outcome = profiler.ResolutionReasonInvalidRunSpec
				task.Status.Result = nil
				task.Status.Conditions[0].Status = meta.ConditionFalse
				task.Status.Conditions[0].Reason = task.Status.Outcome
			}
			if mode == "active measurement" {
				task.Status.Phase = string(api.MeasurementPhaseMeasuring)
			}
			if mode == measurementFinalizerTest {
				task.Finalizers = []string{"external/finalizer"}
			}
			deletes := 0
			native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(task).
				WithInterceptorFuncs(interceptor.Funcs{
					Delete: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
						deletes++
						assertNativeMeasurementDeleteOptions(t, task, opts)
						if !reflect.DeepEqual(object.(*api.BatchEnergyOptimization).Spec, task.Spec) {
							t.Fatal("measurement spec changed before native deletion")
						}
						return c.Delete(ctx, object, opts...)
					},
				}).Build()
			kube := kubefake.NewSimpleClientset()
			if mode == "physical source" {
				pod := &core.Pod{ObjectMeta: meta.ObjectMeta{
					Name: "starting", Annotations: map[string]string{
						servingstate.ServingProfileAnnotation: "allowed/task/native-uid",
					},
				}}
				if _, err := kube.CoreV1().Pods("serving").Create(ctx, pod, meta.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			storage := deletionStore(t, nil)
			opts := Config{
				ServingNamespace: "serving", ProfileNamespaces: []string{"allowed"}, SchedulerConfigMap: "scheduler",
				SchedulerConfigKey: "scheduler.json", MaxRequestBytes: 1 << 20, MaxEvidenceBytes: 1 << 20, StoreName: "evidence",
			}
			service := &Service{
				client: native, kube: kube, objects: storage.store, opts: opts,
				scheduler: &SchedulerInputs{Lock: &sync.Mutex{}},
			}
			uid, version := "native-uid", "7"
			if mode == "stale version" {
				version = "6"
			}
			if mode == "replaced UID" {
				uid = "old-uid"
			}
			result, err := service.Delete(ctx, "allowed", "task", uid, version)
			accepted := mode == "deleted" || mode == measurementFinalizerTest || mode == measurementInvalidRunSpecTest
			if (err == nil) != accepted || (deletes == 1) != accepted {
				t.Fatalf("result=%+v err=%v deletes=%d", result, err, deletes)
			}
			current := &api.BatchEnergyOptimization{}
			getErr := native.Get(ctx, types.NamespacedName{Namespace: "allowed", Name: "task"}, current)
			if mode == "deleted" || mode == measurementInvalidRunSpecTest {
				if !apierrors.IsNotFound(getErr) {
					t.Fatal("native object survived accepted deletion")
				}
			} else if getErr != nil || !reflect.DeepEqual(current.Status.Result, task.Status.Result) || current.UID != task.UID {
				t.Fatal("rejection/finalizer lost native object identity")
			}
			if mode == measurementFinalizerTest && (current.DeletionTimestamp.IsZero() || len(current.Finalizers) != 1) {
				t.Fatal("owner bypassed native finalizer")
			}
			if accepted && (!result.EvidenceDeleted || len(result.DeletedEvidence) != 3 ||
				!reflect.DeepEqual(result.Task.Status.Result, task.Status.Result)) {
				t.Fatal("native deletion omitted exact owned evidence cleanup")
			}
			if !accepted && storage.deleteCalls() != 0 {
				t.Fatal("rejected deletion touched Evidence Store")
			}
		})
	}
}
