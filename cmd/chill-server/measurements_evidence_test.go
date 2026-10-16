package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	"github.com/lab-paper-code/chill/internal/serving"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type deletionStorage struct {
	mu       sync.Mutex
	objects  map[string][]byte
	deleted  []string
	attempts int
	failKey  string
	store    *stores3.Store
}

func deletionStore(t *testing.T, initial map[string][]byte) *deletionStorage {
	t.Helper()
	storage := &deletionStorage{objects: map[string][]byte{}}
	for key, value := range initial {
		storage.objects[key] = append([]byte(nil), value...)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storage.mu.Lock()
		defer storage.mu.Unlock()
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		if !strings.HasPrefix(r.URL.Path, "/bucket/") {
			t.Error("escaped configured physical bucket")
			w.WriteHeader(400)
			return
		}
		switch r.Method {
		case http.MethodGet:
			payload, ok := storage.objects[key]
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`))
				return
			}
			_, _ = w.Write(payload)
		case http.MethodDelete:
			storage.attempts++
			if key == storage.failKey {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(403)
				_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
				return
			}
			delete(storage.objects, key)
			storage.deleted = append(storage.deleted, key)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected mutation %s to evidence store", r.Method)
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(server.Close)
	var err error
	storage.store, err = stores3.New(stores3.Config{
		LogicalStore: "evidence", Bucket: "bucket", Endpoint: server.URL, Region: "region",
		AccessKeyID: "test", SecretAccessKey: "test", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return storage
}

func (s *deletionStorage) deleteCalls() int { s.mu.Lock(); defer s.mu.Unlock(); return s.attempts }
func (s *deletionStorage) contains(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[key]
	return ok
}

func terminalDeletionTask() *api.BatchEnergyOptimization {
	return &api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{
			Namespace: "allowed", Name: "task", UID: "native-uid", ResourceVersion: "7", Generation: 1,
		},
		Status: api.BatchEnergyOptimizationStatus{
			ObservedGeneration: 1, Phase: "Complete",
			Conditions: []meta.Condition{{
				Type: "MeasurementComplete", Status: meta.ConditionTrue, Reason: "ResultWritten", ObservedGeneration: 1,
			}},
			Result: &api.ProfilingRunEvidenceReference{
				Store: "evidence", Key: "batch-energy-optimizations/native-uid/result.json", Size: 100,
			},
		},
	}
}

func ownedDeletionRun(task *api.BatchEnergyOptimization) (*api.ProfilingRun, *batch.Job) {
	run := &api.ProfilingRun{
		ObjectMeta: meta.ObjectMeta{
			Namespace: task.Namespace, Name: "owned-run", UID: "run-uid",
			OwnerReferences: []meta.OwnerReference{
				*meta.NewControllerRef(task, api.GroupVersion.WithKind("BatchEnergyOptimization")),
			},
		},
		Status: api.ProfilingRunStatus{
			Job: &api.ProfilingRunObjectReference{Name: "owned-job", UID: "job-uid"},
			Evidence: &api.ProfilingRunEvidenceReference{
				Store: "evidence", Key: "profiling-runs/run-uid/execution.json",
				SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion, Size: 100,
			},
		},
	}
	job := &batch.Job{
		ObjectMeta: meta.ObjectMeta{
			Namespace: task.Namespace, Name: "owned-job", UID: "job-uid",
			OwnerReferences: []meta.OwnerReference{*meta.NewControllerRef(run, api.GroupVersion.WithKind("ProfilingRun"))},
		},
		Status: batch.JobStatus{
			Succeeded: 1, Conditions: []batch.JobCondition{{Type: batch.JobComplete, Status: core.ConditionTrue}},
		},
	}
	return run, job
}

func TestMeasurementDeletesExactOwnedEvidenceRawFirstWithoutTouchingSharedArtifacts(t *testing.T) {
	ctx := context.Background()
	task := terminalDeletionTask()
	run, job := ownedDeletionRun(task)
	execution, err := json.Marshal(profilerevidence.ExecutionEvidence{
		SchemaVersion:  profilerevidence.ExecutionEvidenceSchemaVersion,
		RawMeasurement: evidencestore.ObjectIdentity{Store: "evidence", Key: "profiling-runs/run-uid/raw.json", Size: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := campaign.NewResultRecord(campaign.ResultRecordSpec{
		Outcome: "MinimumFound/LocalMinimum",
		MeasurementState: campaign.ResultMeasurementState{
			TargetNode: "node", ModelSpec: "model", DeviceClass: "class", ExecutionPath: "ort-cpu", PowerMode: "fixed",
		},
		CurvePlan: campaign.StoredObjectReference{
			Store: "evidence", Key: "batch-energy-optimizations/native-uid/curve-plan.json",
			SchemaVersion: "chill.dacs.io/curve-plan.v1alpha8", Size: 11,
		},
		CompletedTrials: []campaign.CompletedTrialReference{{
			Run:         campaign.RunReference{Namespace: run.Namespace, Name: run.Name, UID: string(run.UID)},
			Measurement: campaign.ResultMeasurement{WorkloadMode: campaign.WorkloadModeActive, Batch: 1},
			Evidence: campaign.StoredObjectReference{
				Store: "evidence", Key: run.Status.Evidence.Key,
				SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion, Size: int64(len(execution)),
			},
		}},
		Detail: json.RawMessage(`null`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resultBytes, err := record.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	storage := deletionStore(t, map[string][]byte{
		"profiling-runs/run-uid/raw.json":                       []byte("raw!"),
		"profiling-runs/run-uid/execution.json":                 execution,
		"batch-energy-optimizations/native-uid/curve-plan.json": []byte("owned curve"),
		"batch-energy-optimizations/native-uid/result.json":     resultBytes,
		"serving/profiles/allowed/task/native-uid/bundle.json":  []byte("owned bundle"),
		"global/artifact.onnx":                                  []byte("shared model"),
		"profiling-runs/another-uid/raw.json":                   []byte("other measurement"),
	})
	native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(task, run, job).Build()
	service := deletionService(native, storage)
	result, err := service.delete(ctx, task.Namespace, task.Name, string(task.UID), task.ResourceVersion)
	if err != nil || !result.EvidenceDeleted || result.Request != "DeletionRequested" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	want := []string{
		"profiling-runs/run-uid/raw.json", "profiling-runs/run-uid/execution.json",
		"serving/profiles/allowed/task/native-uid/bundle.json",
		"batch-energy-optimizations/native-uid/curve-plan.json", "batch-energy-optimizations/native-uid/result.json",
	}
	storage.mu.Lock()
	deleted := append([]string(nil), storage.deleted...)
	storage.mu.Unlock()
	if !reflect.DeepEqual(deleted, want) || len(result.DeletedEvidence) != len(want) {
		t.Fatalf("not exact source-owned raw-first set: %v", deleted)
	}
	for _, key := range want {
		if storage.contains(key) {
			t.Fatalf("owned evidence remains: %s", key)
		}
	}
	if !storage.contains("global/artifact.onnx") || !storage.contains("profiling-runs/another-uid/raw.json") {
		t.Fatal("foreign/global evidence deleted")
	}
	key := client.ObjectKeyFromObject(task)
	if err := native.Get(ctx, key, &api.BatchEnergyOptimization{}); !apierrors.IsNotFound(err) {
		t.Fatalf("native deletion not requested: %v", err)
	}
}

func TestMeasurementSharedReferenceAndForeignOwnershipBlockBeforeAnyS3Delete(t *testing.T) {
	modes := []string{
		"other measurement", "foreign execution", "baseline", "native raw sharing", "current cost", "accepted old cost",
		"foreign own execution", "foreign own result", "active Job", "missing Job active Pod",
		"unfinalized Run", "recoverable failure",
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			task := terminalDeletionTask()
			run, job := ownedDeletionRun(task)
			objects := []client.Object{task, run, job}
			stored := map[string][]byte{}
			if mode == "foreign own execution" {
				run.Status.Evidence.Key = "profiling-runs/foreign/execution.json"
			}
			if mode == "foreign own result" {
				task.Status.Result.Key = "batch-energy-optimizations/foreign/result.json"
			}
			if mode == "active Job" {
				job.Status = batch.JobStatus{Active: 1}
			}
			if mode == "missing Job active Pod" {
				objects = objects[:2]
			}
			if mode == "unfinalized Run" {
				run.Status.Evidence = nil
			}
			if mode == "recoverable failure" {
				task.Status.Phase = "Failed"
				task.Status.Outcome = "CurvePlanResolutionFailed"
				task.Status.Conditions[0].Status = meta.ConditionFalse
				task.Status.Conditions[0].Reason = task.Status.Outcome
			}
			shared := &api.ProfilingRunEvidenceReference{
				Store: "evidence", Key: "profiling-runs/run-uid/execution.json", Size: 100,
			}
			switch mode {
			case "other measurement":
				objects = append(objects, &api.BatchEnergyOptimization{
					ObjectMeta: meta.ObjectMeta{Namespace: "another", Name: "shared", UID: "another-beo"},
					Status:     api.BatchEnergyOptimizationStatus{Result: shared},
				})
			case "foreign execution":
				objects = append(objects, &api.ProfilingRun{
					ObjectMeta: meta.ObjectMeta{Namespace: "another", Name: "shared", UID: "another-run"},
					Status:     api.ProfilingRunStatus{Evidence: shared},
				})
			case "baseline":
				objects = append(objects, &api.ModelLoadedIdleBaseline{
					ObjectMeta: meta.ObjectMeta{Namespace: "another", Name: "baseline", UID: "baseline-uid"},
					Status: api.ModelLoadedIdleBaselineStatus{Result: &api.ProfilingRunEvidenceReference{
						Store: "evidence", Key: "baseline/result.json", Size: 100,
					}},
				})
				stored["baseline/result.json"] = []byte(`{"finalEvidence":{"trial":` +
					`{"store":"evidence","key":"profiling-runs/run-uid/execution.json"}}}`)
			case "native raw sharing":
				objects = append(objects, &api.ProfilingRun{
					ObjectMeta: meta.ObjectMeta{Namespace: "another", Name: "shared", UID: "another-run"},
					Status: api.ProfilingRunStatus{Evidence: &api.ProfilingRunEvidenceReference{
						Store: "evidence", Key: "other/execution.json", Size: 100,
					}},
				})
				stored["other/execution.json"] = []byte(`{"rawMeasurement":` +
					`{"store":"evidence","key":"profiling-runs/run-uid/raw.json"}}`)
			}
			storage := deletionStore(t, stored)
			native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(objects...).Build()
			service := deletionService(native, storage)
			if mode == "missing Job active Pod" {
				service.kube = kubefake.NewSimpleClientset(&core.Pod{
					ObjectMeta: meta.ObjectMeta{
						Namespace: run.Namespace, Name: "orphaned-running", UID: "pod-uid",
						OwnerReferences: []meta.OwnerReference{
							*meta.NewControllerRef(job, batch.SchemeGroupVersion.WithKind("Job")),
						},
					},
					Status: core.PodStatus{Phase: core.PodRunning},
				})
			}
			if mode == "current cost" || mode == "accepted old cost" {
				settings := serving.SchedulerSettings{
					Algorithm:         serving.SchedulerAlgorithmFixedHorizon,
					ObservationWindow: 1, ControlInterval: 1, Horizon: 1, MaxObservationAge: 1,
					Replays: 1, MaxCandidates: 1, MaxReplicas: 1, MaxInstances: 1, MaxReplayRequests: 1, MaxCPUs: 1,
				}
				config := schedulerConfiguration{Settings: settings}
				costConfig := config
				costConfig.CostReferences = []evidencestore.ObjectIdentity{{Store: "evidence", Key: "cost.json", Size: 100}}
				storage.objects["cost.json"] = []byte(`{"request":{"operation":{"prepare":` +
					`{"config":{"policy":{"profileID":"allowed/task/native-uid"}}}}}}`)
				if mode == "current cost" {
					config = costConfig
				}
				encoded, err := json.Marshal(config)
				if err != nil {
					t.Fatal(err)
				}
				kube := kubefake.NewSimpleClientset(&core.ConfigMap{
					ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "scheduler"},
					Data:       map[string]string{"scheduler.json": string(encoded)},
				})
				opts := service.opts
				opts.schedulerConfigMap = "scheduler"
				opts.schedulerConfigKey = "scheduler.json"
				opts.maxRequestBytes = 1 << 20
				service.kube = kube
				service.scheduler = &schedulerService{kube: kube, opts: opts}
				if mode == "accepted old cost" {
					service.scheduler.configuration.Store(&costConfig)
				}
			}
			_, err := service.delete(context.Background(), task.Namespace, task.Name, string(task.UID), task.ResourceVersion)
			var domain *serving.Error
			if !errors.As(err, &domain) || domain.Code != "MeasurementInUse" || storage.deleteCalls() != 0 {
				t.Fatalf("unsafe cleanup err=%v S3 deletes=%d", err, storage.deleteCalls())
			}
			key := client.ObjectKeyFromObject(task)
			if err := native.Get(context.Background(), key, &api.BatchEnergyOptimization{}); err != nil {
				t.Fatal("rejection mutated native measurement")
			}
		})
	}
}

func TestMeasurementPartialCleanupRetainsNativeAndRetrySurvivesMissingObjects(t *testing.T) {
	task := terminalDeletionTask()
	run, job := ownedDeletionRun(task)
	execution, _ := json.Marshal(profilerevidence.ExecutionEvidence{
		SchemaVersion:  profilerevidence.ExecutionEvidenceSchemaVersion,
		RawMeasurement: evidencestore.ObjectIdentity{Store: "evidence", Key: "profiling-runs/run-uid/raw.json", Size: 4},
	})
	storage := deletionStore(t, map[string][]byte{
		"profiling-runs/run-uid/raw.json": []byte("raw!"), "profiling-runs/run-uid/execution.json": execution,
	})
	storage.failKey = "profiling-runs/run-uid/execution.json"
	native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(task, run, job).Build()
	service := deletionService(native, storage)
	_, err := service.delete(context.Background(), task.Namespace, task.Name, string(task.UID), task.ResourceVersion)
	var domain *serving.Error
	if !errors.As(err, &domain) || domain.Code != "MeasurementEvidenceDeletionIncomplete" ||
		storage.contains("profiling-runs/run-uid/raw.json") || !storage.contains("profiling-runs/run-uid/execution.json") {
		t.Fatalf("partial state misreported: %v", err)
	}
	key := client.ObjectKeyFromObject(task)
	if err := native.Get(context.Background(), key, &api.BatchEnergyOptimization{}); err != nil {
		t.Fatal("partial S3 cleanup removed native retry identity")
	}
	storage.mu.Lock()
	storage.failKey = ""
	storage.mu.Unlock()
	result, err := service.delete(context.Background(), task.Namespace, task.Name, string(task.UID), task.ResourceVersion)
	if err != nil || !result.EvidenceDeleted || storage.contains("profiling-runs/run-uid/execution.json") {
		t.Fatalf("retry through missing raw/result failed: %+v %v", result, err)
	}
}

func TestMeasurementNativeConflictAfterEvidenceCleanupIsExplicitlyPartial(t *testing.T) {
	task := terminalDeletionTask()
	storage := deletionStore(t, map[string][]byte{
		"batch-energy-optimizations/native-uid/curve-plan.json": []byte("curve"),
	})
	native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(task).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
				resource := schema.GroupResource{Group: api.GroupVersion.Group, Resource: "batchenergyoptimizations"}
				return apierrors.NewConflict(resource, task.Name, errors.New("concurrent native update"))
			},
		}).Build()
	service := deletionService(native, storage)
	result, err := service.delete(context.Background(), task.Namespace, task.Name, string(task.UID), task.ResourceVersion)
	var domain *serving.Error
	if !errors.As(err, &domain) || domain.Code != "MeasurementDeletionIncomplete" ||
		!result.EvidenceDeleted || result.Request != "" ||
		storage.contains("batch-energy-optimizations/native-uid/curve-plan.json") {
		t.Fatalf("native conflict falsely atomic: %+v %v", result, err)
	}
	key := client.ObjectKeyFromObject(task)
	if err := native.Get(context.Background(), key, &api.BatchEnergyOptimization{}); err != nil {
		t.Fatal("native conflict removed retry identity")
	}
}

func deletionService(native client.Client, storage *deletionStorage) *measurementService {
	return &measurementService{
		client: native, kube: kubefake.NewSimpleClientset(), objects: storage.store,
		opts: options{servingNamespace: "allowed", storeName: "evidence", maxEvidenceBytes: 1 << 20},
	}
}

func TestForeignMetadataAndDiagnosticsCannotVetoMeasurementDeletion(t *testing.T) {
	task := terminalDeletionTask()
	foreign := &api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{
			Namespace: "foreign", Name: "attacker", UID: "foreign-uid",
			Annotations: map[string]string{"untrusted": "allowed/task/native-uid"},
		},
		Status: api.BatchEnergyOptimizationStatus{Result: &api.ProfilingRunEvidenceReference{
			Store: "evidence", Key: "foreign/result.json", Size: 100,
		}},
	}
	storage := deletionStore(t, map[string][]byte{
		"foreign/result.json": []byte(`{"detail":{"reason":"allowed/task/native-uid"}}`),
	})
	native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(task, foreign).Build()
	result, err := deletionService(native, storage).delete(
		context.Background(), task.Namespace, task.Name, string(task.UID), task.ResourceVersion,
	)
	if err != nil || !result.EvidenceDeleted || storage.deleteCalls() != 3 {
		t.Fatalf("unrelated untrusted metadata vetoed cleanup: %+v %v", result, err)
	}
	if !storage.contains("foreign/result.json") {
		t.Fatal("unrelated foreign result was deleted")
	}
}

func TestFailedCampaignWithPublishedPreJobFailureCanBeDeleted(t *testing.T) {
	cases := []struct {
		name, execution, reason string
		stale                   bool
	}{
		{name: "permanent actuation-policy failure"},
		{name: "unconfirmed required state", reason: measurementRequiredStateNotConfirmed},
		{name: "stale observed generation", stale: true},
		{name: "unrecorded producer Job", execution: "producer"},
		{name: "controller-owned Job", execution: "owned"},
		{name: "active orphan Pod", execution: "orphan"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			task := terminalDeletionTask()
			task.Status.Phase = "Failed"
			task.Status.Outcome = "RetryLimitExhausted"
			task.Status.Result = nil
			task.Status.Conditions[0].Status = meta.ConditionFalse
			task.Status.Conditions[0].Reason = task.Status.Outcome
			run, _ := ownedDeletionRun(task)
			run.Generation = 1
			reason := profiler.ResolutionReasonActuationPolicyInvalid
			observed := run.Generation
			if tc.reason != "" {
				reason = tc.reason
				observed = 0
			}
			run.Status = api.ProfilingRunStatus{
				ObservedGeneration: observed,
				Conditions: []meta.Condition{{
					Type: api.ProfilingRunConditionExecutionComplete, Status: meta.ConditionFalse,
					Reason: reason, ObservedGeneration: run.Generation,
				}},
			}
			if tc.stale {
				run.Status.Conditions[0].ObservedGeneration = 0
			}
			name, err := profilercontroller.ExecutionJobName(run)
			if err != nil {
				t.Fatal(err)
			}
			nativeObjects := []client.Object{task, run}
			job := &batch.Job{ObjectMeta: meta.ObjectMeta{Namespace: run.Namespace, Name: name, UID: "lost-job"}}
			switch tc.execution {
			case "producer":
				nativeObjects = append(nativeObjects, job)
			case "owned":
				job.Name = "different-name"
				job.OwnerReferences = []meta.OwnerReference{
					*meta.NewControllerRef(run, api.GroupVersion.WithKind("ProfilingRun")),
				}
				nativeObjects = append(nativeObjects, job)
			}
			native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(nativeObjects...).Build()
			storage := deletionStore(t, nil)
			service := deletionService(native, storage)
			if tc.execution == "orphan" {
				service.kube = kubefake.NewSimpleClientset(&core.Pod{
					ObjectMeta: meta.ObjectMeta{
						Namespace: run.Namespace, Name: "orphan", UID: "orphan-pod",
						OwnerReferences: []meta.OwnerReference{
							*meta.NewControllerRef(job, batch.SchemeGroupVersion.WithKind("Job")),
						},
					},
					Status: core.PodStatus{Phase: core.PodRunning},
				})
			}
			result, err := service.delete(ctx, task.Namespace, task.Name, string(task.UID), task.ResourceVersion)
			if tc.stale || tc.execution != "" {
				if err == nil || storage.deleteCalls() != 0 {
					t.Fatal("unsafe pre-Job cleanup mutated evidence")
				}
				return
			}
			if err != nil || !result.EvidenceDeleted || len(result.DeletedEvidence) != 5 {
				t.Fatalf("permanent pre-Job failed campaign is undeletable: %+v %v", result, err)
			}
		})
	}
}

func TestPowerCostReferencesProtectAcceptedAndRawNativeProfiles(t *testing.T) {
	var node transitioncost.NativeNodeObservation
	witness := `{"instances":[{"state":{"metadata":{"profileID":"allowed/task/native-uid"}}}]}`
	if err := json.Unmarshal([]byte(witness), &node); err != nil {
		t.Fatal(err)
	}
	for _, finalized := range []bool{true, false} {
		t.Run(map[bool]string{true: "accepted observation", false: "raw observation"}[finalized], func(t *testing.T) {
			record := struct {
				RawEvidence evidencestore.ObjectIdentity              `json:"rawEvidence"`
				Observation transitioncost.FinalizedObservationRecord `json:"observation"`
			}{
				RawEvidence: evidencestore.ObjectIdentity{Store: "evidence", Key: "power/raw.json", Size: 1},
			}
			if finalized {
				record.Observation.AcceptedObservation = &transitioncost.FinalizedAcceptedObservationRecord{
					BeforeNative: &node,
				}
			}
			payload, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			objects := map[string][]byte{"power/final.json": payload}
			if !finalized {
				raw, err := transitioncost.EncodeRawRecord(transitioncost.RawObservation{BeforeNative: &node})
				if err != nil {
					t.Fatal(err)
				}
				objects["power/raw.json"] = raw
			}
			storage := deletionStore(t, objects)
			service := deletionService(nil, storage)
			guard := &measurementReferenceGuard{profilePrefix: "allowed/task/"}
			reference := evidencestore.ObjectIdentity{
				Store: "evidence", Key: "power/final.json", Size: int64(len(payload)),
			}
			err = service.checkPowerTrialEvidence(context.Background(), reference, guard)
			var domain *serving.Error
			if !errors.As(err, &domain) || domain.Code != "MeasurementInUse" || storage.deleteCalls() != 0 {
				t.Fatalf("current power-cost reference was not protected: %v", err)
			}
		})
	}
}
