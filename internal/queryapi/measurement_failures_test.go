package queryapi

import (
	"encoding/json"
	"strings"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestMeasurementFailureClassificationIsSharedAndSafe(t *testing.T) {
	for _, test := range []struct {
		name          string
		exit          *int32
		log           string
		message       string
		code          string
		executionPath string
		native        *api.ProfilingRunRuntimeTermination
	}{
		{name: "typed runtime OOM wins over legacy GPU log", exit: ptr.To(int32(137)), log: "CUDA out of memory", native: &api.ProfilingRunRuntimeTermination{Class: "ExpectedStop", Container: "runtime", Stage: "Execution", Reason: "OOMKilled"}, code: "HostOOM"},
		{name: "worker OOM is not a runtime capacity limit", exit: ptr.To(int32(137)), native: &api.ProfilingRunRuntimeTermination{Class: "FatalFailure", Container: "measurement-worker", Stage: "Execution", Reason: "OOMKilled"}, code: "MeasurementContainerOOM"},
		{name: "observer OOM is not a runtime capacity limit", exit: ptr.To(int32(137)), native: &api.ProfilingRunRuntimeTermination{Class: "FatalFailure", Container: "metric-observer", Stage: "Execution", Reason: "OOMKilled"}, code: "MeasurementContainerOOM"},
		{name: "runtime OOM outside execution is not an expected stop", exit: ptr.To(int32(137)), native: &api.ProfilingRunRuntimeTermination{Class: "FatalFailure", Container: "runtime", Stage: "Preparation", Reason: "OOMKilled"}, code: "RuntimeFailed"},
		{name: "typed fatal execution wins over legacy memory log", exit: ptr.To(int32(3)), log: "CUDA out of memory", native: &api.ProfilingRunRuntimeTermination{Class: "FatalFailure", Container: "runtime", Stage: "Execution", FailureReason: "ExecutionFailed"}, code: "RuntimeFailed"},
		{name: "typed unknown exit remains unknown despite legacy OOM log", exit: ptr.To(int32(137)), log: "OOMKilled CUDA out of memory", native: &api.ProfilingRunRuntimeTermination{Class: "UnknownFailure"}, code: "UnknownFailure"},
		{name: "typed eviction overrides retained native runtime OOM", exit: ptr.To(int32(137)), log: "CUDA out of memory", native: &api.ProfilingRunRuntimeTermination{Class: "RetryableFailure", Container: "runtime", Stage: "Execution", Reason: "OOMKilled", PodReason: "Evicted"}, code: "RetryableInfrastructureFailure"},
		{name: "typed runtime measurement budget", exit: ptr.To(int32(4)), native: &api.ProfilingRunRuntimeTermination{Class: "ExpectedStop", Container: "runtime", Stage: "Execution", FailureReason: "MeasurementBudgetExceeded"}, code: "MeasurementBudgetExceeded"},
		{name: "typed allocator stop is not kernel OOM", exit: ptr.To(int32(3)), native: &api.ProfilingRunRuntimeTermination{Class: "ExpectedStop", Container: "runtime", Stage: "Execution", FailureReason: "MemoryAllocationFailed"}, code: "MemoryAllocationFailed"},
		{name: "startup allocation is not a discovery stop", exit: ptr.To(int32(3)), native: &api.ProfilingRunRuntimeTermination{Class: "FatalFailure", Container: "runtime", Stage: "Startup", FailureReason: "MemoryAllocationFailed"}, code: "RuntimeFailed"},
		{name: "eviction overrides allocation reason", exit: ptr.To(int32(3)), native: &api.ProfilingRunRuntimeTermination{Class: "RetryableFailure", Container: "runtime", Stage: "Execution", FailureReason: "MemoryAllocationFailed", PodReason: "Evicted"}, code: "RetryableInfrastructureFailure"},
		{name: "typed integrity failure wins over legacy memory log", exit: ptr.To(int32(3)), log: "CUDA out of memory", native: &api.ProfilingRunRuntimeTermination{Class: "FatalFailure", Container: "runtime", Stage: "Execution", FailureReason: "IntegrityError"}, code: "EvidenceInvalid"},
		{name: "exit 137 alone does not prove OOM", exit: ptr.To(int32(137)), code: "RuntimeFailed"},
		{name: "CUDA OOM beats host exit", exit: ptr.To(int32(137)), log: "\x1b[31mCUDA out of memory\x1b[0m", code: "GPUMemoryExhausted"},
		{name: "CUDA allocation error", exit: ptr.To(int32(1)), log: "CUDA_ERROR_OUT_OF_MEMORY", code: "GPUMemoryExhausted"},
		{name: "cuBLAS allocation", message: "CUBLAS_STATUS_ALLOC_FAILED", code: "GPUMemoryExhausted"},
		{name: "cuDNN allocation", message: "CUDNN_STATUS_ALLOC_FAILED", code: "GPUMemoryExhausted"},
		{name: "GPU memory", message: "GPU out of memory", code: "GPUMemoryExhausted"},
		{name: "ONNX CUDA arena allocation", executionPath: "firstfloor-ort-cuda-amd64", exit: ptr.To(int32(3)), log: "onnxruntime::BFCArena::AllocateRawInternal Failed to allocate memory for requested buffer of size 8589934592", code: "GPUMemoryExhausted"},
		{name: "ONNX CUDA arena beats host exit", executionPath: "firstfloor-ort-cuda-arm64", exit: ptr.To(int32(137)), log: "Failed to allocate memory for requested buffer of size 8589934592 BFCArena", code: "GPUMemoryExhausted"},
		{name: "ONNX CPU arena records allocation diagnostic", executionPath: "firstfloor-ort-cpu-amd64", exit: ptr.To(int32(3)), log: "onnxruntime::BFCArena::AllocateRawInternal Failed to allocate memory for requested buffer of size 8589934592", code: "MemoryAllocationFailed"},
		{name: "ONNX CPU arena diagnostic beats observation", executionPath: "firstfloor-ort-cpu-arm64", message: "BFCArena Failed to allocate memory for requested buffer of size 8589934592\npower observation timed out", code: "MemoryAllocationFailed"},
		{name: "ONNX arena without backend context", message: "BFCArena Failed to allocate memory for requested buffer of size 8589934592", code: "Other"},
		{name: "CUDA path alone is not memory evidence", executionPath: "firstfloor-ort-cuda-amd64", message: "unrecognized failure", code: "Other"},
		{name: "exit 137 does not override observation diagnostic", exit: ptr.To(int32(137)), log: "power observation timed out", code: "PowerObservationTimeout"},
		{name: "unattributed OOM token does not override image diagnostic", message: "ErrImagePull\nOOMKilled", code: "ImagePullFailed"},
		{name: "metric result deadline beats image", exit: ptr.To(int32(4)), log: "ErrImagePull\nmetric observation failed: wait for metric result file: context deadline exceeded", code: "PowerObservationTimeout"},
		{name: "power timeout", message: "power observation timeout", code: "PowerObservationTimeout"},
		{name: "observer timeout", message: "metric-observer timed out", code: "PowerObservationTimeout"},
		{name: "image backoff beats runtime", exit: ptr.To(int32(1)), log: "runtime container failed\nImagePullBackOff", code: "ImagePullFailed"},
		{name: "image pull error", message: "failed to pull and unpack image registry/private", code: "ImagePullFailed"},
		{name: "runtime exit", exit: ptr.To(int32(3)), log: "private runtime details", code: "RuntimeFailed"},
		{name: "runtime signal", message: "Segmentation fault", code: "RuntimeFailed"},
		{name: "runtime worker error", message: "execute firstfloor measurement: timed out", code: "RuntimeFailed"},
		{name: "unknown", message: "unrecognized failure", code: "Other"},
		{name: "unqualified memory", message: "out of memory", code: "Other"},
		{name: "unqualified deadline", message: "context deadline exceeded", code: "Other"},
		{name: "unrelated observation and timeout", message: "power observation started\nworkload timed out", code: "Other"},
		{name: "zero exit is not runtime failure", exit: ptr.To(int32(0)), log: "unrecognized failure", code: "Other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			task := api.BatchEnergyOptimization{
				ObjectMeta: metav1.ObjectMeta{Namespace: "allowed", Name: "request", UID: "request-uid", Generation: 1},
				Status:     api.BatchEnergyOptimizationStatus{Phase: "Failed", ObservedGeneration: 1},
			}
			run := api.ProfilingRun{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: task.Namespace, Name: "run", UID: "run-uid", Generation: 1,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: api.GroupVersion.String(), Kind: "BatchEnergyOptimization",
						Name: task.Name, UID: task.UID, Controller: ptr.To(true),
					}},
				},
				Status: api.ProfilingRunStatus{ObservedGeneration: 1, Conditions: []metav1.Condition{
					{Type: api.ProfilingRunConditionExecutionComplete, Status: metav1.ConditionFalse, Reason: "JobFailed", Message: test.message + " private-token", ObservedGeneration: 1},
					{Type: api.ProfilingRunConditionEvidenceVerified, Status: metav1.ConditionFalse, Reason: "OOMKilled", ObservedGeneration: 0},
				}},
			}
			task.Spec.State.Candidate.ExecutionPath = test.executionPath
			run.Spec.Candidate.ExecutionPath = test.executionPath
			if test.exit != nil {
				run.Status.RuntimeTermination = &api.ProfilingRunRuntimeTermination{ExitCode: *test.exit, LogTail: test.log + " private-token"}
				if test.native != nil {
					*run.Status.RuntimeTermination = *test.native
					run.Status.RuntimeTermination.ExitCode = *test.exit
					run.Status.RuntimeTermination.LogTail = test.log + " private-token"
				}
			}
			assessment := AssessProfilingRun(run)
			if assessment.Status != MeasurementFailed || len(assessment.Reasons) != 1 {
				t.Fatalf("failed assessment = %+v", assessment)
			}
			reason := assessment.Reasons[0]
			if reason.Code != test.code || reason.Label == "" || reason.Message == "" {
				t.Fatalf("classification = %+v; want %s", reason, test.code)
			}
			list := BuildProfilingRuns(ProfilingRunsSnapshot{Runs: []api.ProfilingRun{run}})
			for _, failures := range [][]MeasurementFailure{
				assessment.FailureSummary,
				list.Runs[0].Assessment.FailureSummary,
				MeasurementRequestFailures(task, []api.ProfilingRun{run}, assessment),
			} {
				if len(failures) != 1 {
					t.Fatalf("failure summary = %+v", failures)
				}
				failure := failures[0]
				if failure.Code != reason.Code || failure.Label != reason.Label || failure.Message != reason.Message ||
					failure.RunID != string(run.UID) || failure.RunNamespace != run.Namespace || failure.RunName != run.Name {
					t.Fatalf("inconsistent classification or lost run link: %+v; reason %+v", failure, reason)
				}
				if (test.exit == nil) != (failure.ExitCode == nil) || test.exit != nil && *failure.ExitCode != int(*test.exit) {
					t.Fatalf("exit code changed: %+v", failure)
				}
			}
			body, err := json.Marshal(assessment)
			if err != nil || strings.Contains(string(body), "private-token") {
				t.Fatalf("assessment leaks raw error: %s, %v", body, err)
			}
		})
	}
}

func TestR19FailureSummaryRetainsFirstConcreteLogError(t *testing.T) {
	first := `2026/11/02 02:14:41 ERROR serving worker stopped node=jetsonx error="start runner: EOF"`
	run := api.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status: api.ProfilingRunStatus{
			ObservedGeneration: 1,
			Conditions: []metav1.Condition{{
				Type: api.ProfilingRunConditionExecutionComplete, Status: metav1.ConditionFalse,
				Reason: "JobFailed", ObservedGeneration: 1,
			}},
			RuntimeTermination: &api.ProfilingRunRuntimeTermination{
				ExitCode: 3, LogTail: "dependency warning\n" + first +
					"\n2026-11-02T02:14:42.027723Z ERROR serving-worker exited before readiness: 1",
			},
		},
	}
	raw, err := json.Marshal(AssessProfilingRun(run).FailureSummary)
	if err != nil {
		t.Fatal(err)
	}
	var failures []map[string]any
	if err := json.Unmarshal(raw, &failures); err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0]["firstErrorLine"] != first ||
		failures[0]["firstErrorTime"] != "2026/11/02 02:14:41" {
		t.Fatalf("first error source lost or inferred: %s", raw)
	}
}
