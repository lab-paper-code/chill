package measurement

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
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

// deletionStorage is an in-memory Evidence Store for one logical store.
type deletionStorage struct {
	mu       sync.Mutex
	objects  map[string][]byte
	attempts int
	failKey  string
}

func deletionStore(_ *testing.T, initial map[string][]byte) *deletionStorage {
	storage := &deletionStorage{objects: map[string][]byte{}}
	for key, value := range initial {
		storage.objects[key] = append([]byte(nil), value...)
	}
	return storage
}

func (s *deletionStorage) OpenLocation(_ context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, ok := s.objects[location.Key]
	if !ok {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func (s *deletionStorage) List(_ context.Context, prefix string) ([]evidencestore.ObjectIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var listed []evidencestore.ObjectIdentity
	for key, payload := range s.objects {
		if strings.HasPrefix(key, prefix) {
			listed = append(listed, evidencestore.ObjectIdentity{Store: "evidence", Key: key, Size: int64(len(payload))})
		}
	}
	slices.SortFunc(listed, func(a, b evidencestore.ObjectIdentity) int { return strings.Compare(a.Key, b.Key) })
	return listed, nil
}

func (s *deletionStorage) Delete(_ context.Context, location evidencestore.ObjectLocation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	if location.Key == s.failKey {
		return errors.New("injected store failure")
	}
	delete(s.objects, location.Key)
	return nil
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

func TestMeasurementTerminalKnownFailureReasons(t *testing.T) {
	for _, reason := range []string{
		"EnergyObjectivePolicyMismatch", "InvalidCampaignRun",
		profiler.ResolutionReasonInvalidRunSpec, profiler.ResolutionReasonActuationPolicyInvalid,
		string(campaign.FailureClassFatalFailure),
		string(campaign.FailureClassUnknownFailure),
	} {
		for _, mode := range []string{
			"current failure", "stale status generation", "stale condition generation",
			"unversioned task", "different outcome", "true condition", "active phase",
		} {
			t.Run(reason+"/"+mode, func(t *testing.T) {
				task := terminalDeletionTask()
				task.Status.Phase = measurementPhaseFailed
				task.Status.Outcome = reason
				task.Status.Result = nil
				task.Status.Conditions[0].Status = meta.ConditionFalse
				task.Status.Conditions[0].Reason = task.Status.Outcome
				switch mode {
				case "stale status generation":
					task.Status.ObservedGeneration = 0
				case "stale condition generation":
					task.Status.Conditions[0].ObservedGeneration = 0
				case "unversioned task":
					task.Generation = 0
					task.Status.ObservedGeneration = 0
					task.Status.Conditions[0].ObservedGeneration = 0
				case "different outcome":
					task.Status.Outcome = "DifferentOutcome"
				case "true condition":
					task.Status.Conditions[0].Status = meta.ConditionTrue
				case "active phase":
					task.Status.Phase = "Pending"
				}
				if got, want := measurementTerminal(task), mode == "current failure"; got != want {
					t.Fatalf("measurementTerminal() = %v, want %v", got, want)
				}
			})
		}
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
				SchemaVersion: "chill.dacs.io/execution-evidence.v1alpha16", Size: 100,
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

func deletionService(native client.Client, storage *deletionStorage) *Service {
	return &Service{
		client: native, kube: kubefake.NewSimpleClientset(), objects: storage,
		opts: Config{ServingNamespace: "allowed", StoreName: "evidence", MaxEvidenceBytes: 1 << 20},
	}
}

// Objects written under older schemas, under names no current writer uses, or
// not decodable at all are deleted because their owner UID owns them.
func TestMeasurementDeleteRemovesOwnedPrefixesWhateverTheStoredFormat(t *testing.T) {
	for name, execution := range map[string]string{
		"older schema": `{"schemaVersion":"chill.dacs.io/execution-evidence.v1alpha16",` +
			`"rawMeasurement":{"store":"evidence","key":"profiling-runs/run-uid/raw.json"}}`,
		"not JSON": "not json",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			task := terminalDeletionTask()
			task.Status.Result.Key = "batch-energy-optimizations/native-uid/result.shared-cpu.json"
			run, job := ownedDeletionRun(task)
			run.Status.Evidence.Key = "profiling-runs/run-uid/execution.shared-cpu.json"
			owned := []string{
				"profiling-runs/run-uid/raw.json",
				"profiling-runs/run-uid/execution.json",
				"profiling-runs/run-uid/execution.shared-cpu.json",
				"batch-energy-optimizations/native-uid/curve-plan.json",
				"batch-energy-optimizations/native-uid/result.json",
				"batch-energy-optimizations/native-uid/result.shared-cpu.json",
				"serving/profiles/allowed/task/native-uid/bundle.json",
			}
			foreign := []string{
				"profiling-runs/run-uid-2/raw.json",
				"batch-energy-optimizations/native-uid-2/result.json",
				"serving/profiles/allowed/task/native-uid-2/bundle.json",
				"global/artifact.onnx",
			}
			stored := map[string][]byte{}
			for _, key := range append(owned, foreign...) {
				stored[key] = []byte(`{"schemaVersion":"chill.dacs.io/result.v1alpha4"}`)
			}
			stored["profiling-runs/run-uid/execution.json"] = []byte(execution)
			storage := deletionStore(t, stored)
			native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(task, run, job).Build()
			result, err := deletionService(native, storage).Delete(
				ctx, task.Namespace, task.Name, string(task.UID), task.ResourceVersion)
			if err != nil || !result.EvidenceDeleted || result.Request != measurementRequestDeletionRequested ||
				len(result.DeletedEvidence) != len(owned) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			for _, key := range owned {
				if storage.contains(key) {
					t.Errorf("owned object remains: %s", key)
				}
			}
			for _, key := range foreign {
				if !storage.contains(key) {
					t.Errorf("object of another owner deleted: %s", key)
				}
			}
			if err := native.Get(ctx, client.ObjectKeyFromObject(task), &api.BatchEnergyOptimization{}); !apierrors.IsNotFound(err) {
				t.Fatalf("native deletion not requested: %v", err)
			}
		})
	}
}

func TestMeasurementDeleteRefusesWhileSomethingCanStillWriteOrServe(t *testing.T) {
	const (
		notTerminal = "only a current terminal measurement can be deleted; " +
			"cancellation and recoverable failure interruption are unsupported"
		jobActive  = "an owned measurement Job remains active"
		podActive  = "an owned measurement Pod remains active even if its Job is missing"
		podServing = "a physical serving Pod still references the measurement"
	)
	for _, tc := range []struct{ mode, reason string }{
		{"pending", notTerminal},
		{"measuring", notTerminal},
		{"stale generation", notTerminal},
		{"recoverable failure", notTerminal},
		{"active Job", jobActive},
		{"running Pod without Job", podActive},
		{"serving Pod", podServing},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			ctx := context.Background()
			task := terminalDeletionTask()
			run, job := ownedDeletionRun(task)
			objects := []client.Object{task, run, job}
			var pods []*core.Pod
			switch tc.mode {
			case "pending":
				task.Status = api.BatchEnergyOptimizationStatus{Phase: "Pending"}
			case "measuring":
				task.Status.Phase = string(api.MeasurementPhaseMeasuring)
			case "stale generation":
				task.Generation = 2
			case "recoverable failure":
				task.Status.Phase = measurementPhaseFailed
				task.Status.Outcome = "CurvePlanResolutionFailed"
				task.Status.Conditions[0].Status = meta.ConditionFalse
				task.Status.Conditions[0].Reason = task.Status.Outcome
			case "active Job":
				job.Status = batch.JobStatus{Active: 1}
			case "running Pod without Job":
				objects = objects[:2]
				pods = append(pods, &core.Pod{
					ObjectMeta: meta.ObjectMeta{
						Namespace: run.Namespace, Name: "orphaned-running", UID: "pod-uid",
						OwnerReferences: []meta.OwnerReference{
							*meta.NewControllerRef(job, batch.SchemeGroupVersion.WithKind("Job")),
						},
					},
					Status: core.PodStatus{Phase: core.PodRunning},
				})
			case "serving Pod":
				pods = append(pods, &core.Pod{
					ObjectMeta: meta.ObjectMeta{
						Namespace: "allowed", Name: "serving",
						Annotations: map[string]string{servingstate.ServingProfileAnnotation: "allowed/task/native-uid"},
					},
					Status: core.PodStatus{Phase: core.PodRunning},
				})
			}
			storage := deletionStore(t, map[string][]byte{"profiling-runs/run-uid/raw.json": []byte("raw")})
			native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(objects...).Build()
			service := deletionService(native, storage)
			kube := kubefake.NewSimpleClientset()
			for _, pod := range pods {
				if _, err := kube.CoreV1().Pods(pod.Namespace).Create(ctx, pod, meta.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			service.kube = kube
			_, err := service.Delete(ctx, task.Namespace, task.Name, string(task.UID), task.ResourceVersion)
			var domain *serving.Error
			if !errors.As(err, &domain) || domain.Code != "MeasurementInUse" || domain.Message != tc.reason {
				t.Fatalf("err=%v, want MeasurementInUse %q", err, tc.reason)
			}
			if storage.deleteCalls() != 0 {
				t.Fatal("refused deletion touched the Evidence Store")
			}
			if err := native.Get(ctx, client.ObjectKeyFromObject(task), &api.BatchEnergyOptimization{}); err != nil {
				t.Fatal("refusal mutated native measurement")
			}
		})
	}
}

func TestMeasurementDeleteRetryCompletesAfterStoreFailure(t *testing.T) {
	ctx := context.Background()
	task := terminalDeletionTask()
	run, job := ownedDeletionRun(task)
	storage := deletionStore(t, map[string][]byte{
		"batch-energy-optimizations/native-uid/result.json":    []byte(`{"schemaVersion":"chill.dacs.io/result.v1alpha4"}`),
		"profiling-runs/run-uid/execution.json":                []byte(`{"schemaVersion":"v1alpha16"}`),
		"profiling-runs/run-uid/execution.shared-cpu.json":     []byte(`{}`),
		"profiling-runs/run-uid/raw.json":                      []byte("raw"),
		"serving/profiles/allowed/task/native-uid/bundle.json": []byte("bundle"),
	})
	storage.failKey = "profiling-runs/run-uid/execution.shared-cpu.json"
	native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(task, run, job).Build()
	service := deletionService(native, storage)
	_, err := service.Delete(ctx, task.Namespace, task.Name, string(task.UID), task.ResourceVersion)
	var domain *serving.Error
	if !errors.As(err, &domain) || domain.Code != "MeasurementEvidenceDeletionIncomplete" ||
		storage.contains("profiling-runs/run-uid/execution.json") {
		t.Fatalf("partial state misreported: %v", err)
	}
	if err := native.Get(ctx, client.ObjectKeyFromObject(task), &api.BatchEnergyOptimization{}); err != nil {
		t.Fatal("partial cleanup removed native retry identity")
	}
	storage.mu.Lock()
	storage.failKey = ""
	storage.mu.Unlock()
	result, err := service.Delete(ctx, task.Namespace, task.Name, string(task.UID), task.ResourceVersion)
	if err != nil || !result.EvidenceDeleted || len(storage.objects) != 0 {
		t.Fatalf("retry did not complete: %+v %v remaining=%d", result, err, len(storage.objects))
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
	result, err := service.Delete(context.Background(), task.Namespace, task.Name, string(task.UID), task.ResourceVersion)
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
