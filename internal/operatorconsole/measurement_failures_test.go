package operatorconsole

import (
	"encoding/json"
	"strings"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestMeasurementFailureClassificationIsSharedAndSafe(t *testing.T) {
	categories := map[string][2]string{
		"GPUMemoryExhausted":      {"GPU 메모리 부족", "GPU 메모리가 부족해 측정을 완료하지 못했습니다."},
		"HostOOM":                 {"호스트 메모리 부족", "호스트 메모리 부족으로 측정 실행이 종료되었습니다."},
		"PowerObservationTimeout": {"전력 관측 시간 초과", "제한 시간 안에 전력 관측을 완료하지 못했습니다."},
		"ImagePullFailed":         {"이미지 가져오기 실패", "실행 이미지를 가져오지 못해 측정을 시작하지 못했습니다."},
		"RuntimeFailed":           {"실행 오류", "실행 오류로 측정을 완료하지 못했습니다."},
		"Other":                   {"기타 오류", "측정을 완료하지 못했습니다. 실행 상세에서 상태를 확인해 주세요."},
	}
	for _, test := range []struct {
		name          string
		exit          *int32
		log           string
		message       string
		code          string
		executionPath string
	}{
		{name: "CUDA OOM beats host exit", exit: ptr.To(int32(137)), log: "\x1b[31mCUDA out of memory\x1b[0m", code: "GPUMemoryExhausted"},
		{name: "CUDA allocation error", exit: ptr.To(int32(1)), log: "CUDA_ERROR_OUT_OF_MEMORY", code: "GPUMemoryExhausted"},
		{name: "cuBLAS allocation", message: "CUBLAS_STATUS_ALLOC_FAILED", code: "GPUMemoryExhausted"},
		{name: "cuDNN allocation", message: "CUDNN_STATUS_ALLOC_FAILED", code: "GPUMemoryExhausted"},
		{name: "GPU memory", message: "GPU out of memory", code: "GPUMemoryExhausted"},
		{name: "ONNX CUDA arena allocation", executionPath: "firstfloor-ort-cuda-amd64", exit: ptr.To(int32(3)), log: "onnxruntime::BFCArena::AllocateRawInternal Failed to allocate memory for requested buffer of size 8589934592", code: "GPUMemoryExhausted"},
		{name: "ONNX CUDA arena beats host exit", executionPath: "firstfloor-ort-cuda-arm64", exit: ptr.To(int32(137)), log: "Failed to allocate memory for requested buffer of size 8589934592 BFCArena", code: "GPUMemoryExhausted"},
		{name: "ONNX CPU arena is host memory", executionPath: "firstfloor-ort-cpu-amd64", exit: ptr.To(int32(3)), log: "onnxruntime::BFCArena::AllocateRawInternal Failed to allocate memory for requested buffer of size 8589934592", code: "HostOOM"},
		{name: "ONNX CPU arena beats observation", executionPath: "firstfloor-ort-cpu-arm64", message: "BFCArena Failed to allocate memory for requested buffer of size 8589934592\npower observation timed out", code: "HostOOM"},
		{name: "ONNX arena without backend context", message: "BFCArena Failed to allocate memory for requested buffer of size 8589934592", code: "Other"},
		{name: "CUDA path alone is not memory evidence", executionPath: "firstfloor-ort-cuda-amd64", message: "unrecognized failure", code: "Other"},
		{name: "host exit beats observation", exit: ptr.To(int32(137)), log: "power observation timed out", code: "HostOOM"},
		{name: "host reason beats image", message: "ErrImagePull\nOOMKilled", code: "HostOOM"},
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
			}
			assessment := AssessProfilingRun(run)
			want := categories[test.code]
			if assessment.Status != MeasurementFailed || len(assessment.Reasons) != 1 {
				t.Fatalf("failed assessment = %+v", assessment)
			}
			reason := assessment.Reasons[0]
			if reason.Code != test.code || reason.Label != want[0] || reason.Message != want[1] {
				t.Fatalf("classification = %+v; want %s %v", reason, test.code, want)
			}
			detail, err := BuildBatchEnergyOptimizationResult(BatchEnergyOptimizationResultSnapshot{Campaign: task, FailedRuns: []api.ProfilingRun{run}})
			if err != nil {
				t.Fatal(err)
			}
			list := BuildProfilingRuns(ProfilingRunsSnapshot{Runs: []api.ProfilingRun{run}})
			for _, failures := range [][]MeasurementFailure{
				assessment.FailureSummary,
				list.Runs[0].Assessment.FailureSummary,
				detail.Assessment.FailureSummary,
				MeasurementRequestFailures(task, []api.ProfilingRun{run}, detail.Assessment),
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
