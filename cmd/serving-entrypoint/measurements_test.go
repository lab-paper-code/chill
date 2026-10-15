package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/entrypoint"
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
			console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				preflights++
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "" ||
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
				result := operatorconsole.BatchEnergyOptimizationPreflight{
					APIVersion: operatorconsole.BatchEnergyOptimizationPreflightAPIVersion,
					Kind:       "BatchEnergyOptimizationPreflight",
					Campaign: operatorconsole.BatchEnergyOptimizationPreflightCampaign{
						Namespace: task.Namespace, Name: name,
					},
					Resolves: mode != "reject", Reason: "actual unresolved input",
				}
				if err := json.NewEncoder(w).Encode(result); err != nil {
					t.Error(err)
				}
			}))
			defer console.Close()
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
			opts := options{servingNamespace: "allowed", consoleURL: console.URL, requestTimeout: time.Second}
			owner, err := newMeasurementOwner(native, kubefake.NewSimpleClientset(), nil, nil, opts)
			if err != nil {
				t.Fatal(err)
			}
			result, err := owner.submit(context.Background(), task, mode == measurementDryRunTest)
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

func TestMeasurementNamespaceEscapeAndListActualNativeState(t *testing.T) {
	ctx := context.Background()
	pending := &api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "pending", UID: "pending-uid"},
	}
	failed := &api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "failed"},
		Status: api.BatchEnergyOptimizationStatus{
			Phase: "Failed", Outcome: "CurvePlanResolutionFailed",
			Conditions: []meta.Condition{{
				Type: "Done", Status: meta.ConditionFalse, Reason: "ActualFailure", Message: "unsupported actual input",
			}},
		},
	}
	complete := &api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "complete"},
		Status:     api.BatchEnergyOptimizationStatus{Phase: "Complete", CompletedTrials: 9},
	}
	escaped := &api.BatchEnergyOptimization{ObjectMeta: meta.ObjectMeta{Namespace: "outside", Name: "hidden"}}
	native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).
		WithObjects(pending, failed, complete, escaped).Build()
	owner := &measurementOwner{client: native, opts: options{profileNamespaces: []string{"allowed"}}}
	list, err := owner.list(ctx, "allowed")
	if err != nil || len(list.Items) != 3 {
		t.Fatalf("native list: %+v %v", list, err)
	}
	if list.Items[0].Name != "complete" || list.Items[1].Status.Conditions[0].Reason != "ActualFailure" ||
		list.Items[2].Status.Phase != "" || list.Items[2].UID != pending.UID {
		t.Fatalf("native states lost: %+v", list)
	}
	if _, err := owner.list(ctx, "outside"); !errors.Is(err, servingkube.ErrForbidden) {
		t.Fatal("namespace list escaped")
	}
	if _, err := owner.submit(ctx, *escaped, false); !errors.Is(err, servingkube.ErrForbidden) {
		t.Fatal("namespace submit escaped before preflight")
	}
	if _, err := owner.delete(ctx, "outside", "hidden", "uid", "1"); !errors.Is(err, servingkube.ErrForbidden) {
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
		"stale version", "replaced UID", "selected", "preparation source", "physical source",
		"active measurement", measurementFinalizerTest, "deleted",
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			task := terminalDeletionTask()
			if mode == "active measurement" {
				task.Status.Phase = "Measuring"
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
						return c.Delete(ctx, object, opts...)
					},
				}).Build()
			settings := serving.SchedulerSettings{
				ObservationWindow: time.Second, ControlInterval: time.Second, Horizon: time.Second,
				MaxObservationAge: time.Second, Replays: 1, MaxCandidates: 1, MaxReplicas: 1,
				MaxInstances: 1, MaxReplayRequests: 1, MaxCPUs: 1,
			}
			config := schedulerConfiguration{Settings: settings}
			if mode == "selected" {
				config.ProfileCampaigns = []entrypoint.ProfileCampaign{{Namespace: "allowed", Name: "task"}}
			}
			if mode == "preparation source" {
				config.Preparations = map[string]servingkube.PrepareSpec{"allowed/task/native-uid": {}}
			}
			encoded, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			kube := kubefake.NewSimpleClientset(&core.ConfigMap{
				ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "scheduler"},
				Data:       map[string]string{"scheduler.json": string(encoded)},
			})
			if mode == "physical source" {
				pod := &core.Pod{ObjectMeta: meta.ObjectMeta{
					Name: "starting", Annotations: map[string]string{
						servingkube.ServingProfileAnnotation: "allowed/task/native-uid",
					},
				}}
				if _, err := kube.CoreV1().Pods("serving").Create(ctx, pod, meta.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			storage := deletionStore(t, nil)
			opts := options{
				servingNamespace: "serving", profileNamespaces: []string{"allowed"}, schedulerConfigMap: "scheduler",
				schedulerConfigKey: "scheduler.json", maxRequestBytes: 1 << 20, maxEvidenceBytes: 1 << 20, storeName: "evidence",
			}
			owner := &measurementOwner{
				client: native, kube: kube, objects: storage.store, opts: opts,
				scheduler: &schedulerOwner{kube: kube, opts: opts},
			}
			uid, version := "native-uid", "7"
			if mode == "stale version" {
				version = "6"
			}
			if mode == "replaced UID" {
				uid = "old-uid"
			}
			result, err := owner.delete(ctx, "allowed", "task", uid, version)
			accepted := mode == "deleted" || mode == measurementFinalizerTest
			if (err == nil) != accepted || (deletes == 1) != accepted {
				t.Fatalf("result=%+v err=%v deletes=%d", result, err, deletes)
			}
			current := &api.BatchEnergyOptimization{}
			getErr := native.Get(ctx, types.NamespacedName{Namespace: "allowed", Name: "task"}, current)
			if mode == "deleted" {
				if !apierrors.IsNotFound(getErr) {
					t.Fatal("native object survived accepted deletion")
				}
			} else if getErr != nil || current.Status.Result.Key != task.Status.Result.Key || current.UID != task.UID {
				t.Fatal("rejection/finalizer lost native object identity")
			}
			if mode == measurementFinalizerTest && (current.DeletionTimestamp.IsZero() || len(current.Finalizers) != 1) {
				t.Fatal("owner bypassed native finalizer")
			}
			if accepted && (!result.EvidenceDeleted || len(result.DeletedEvidence) != 3 ||
				result.Task.Status.Result.Key != task.Status.Result.Key) {
				t.Fatal("native deletion omitted exact owned evidence cleanup")
			}
			if !accepted && storage.deleteCalls() != 0 {
				t.Fatal("rejected deletion touched Evidence Store")
			}
		})
	}
}
