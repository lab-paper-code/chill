package operatorconsole

import (
	"regexp"
	"sort"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	runtimeLogANSI          = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	gpuMemoryFailure        = regexp.MustCompile(`cuda[^\n]*(out of memory|out_of_memory)|cublas_status_alloc_failed|cudnn_status_alloc_failed|gpu out of memory`)
	powerObservationTimeout = regexp.MustCompile(`(?m)(metric observation|metric result|metric[- ]observer|power observation|observe power)[^\n]*(timeout|timed out|deadline exceeded)|(timeout|timed out|deadline exceeded)[^\n]*(metric observation|metric result|metric[- ]observer|power observation|observe power)`)
	imagePullFailure        = regexp.MustCompile(`imagepullbackoff|errimagepull|image pull failed|failed to pull image|failed to pull and unpack image`)
	runtimeExecutionFailure = regexp.MustCompile(`runtimefailed|runtime failed|runtime container failed|runtimeerror:|execute firstfloor measurement:|segmentation fault|illegal instruction`)
)

// MeasurementRequestFailures joins only exact native controller owners.
func MeasurementRequestFailures(task api.BatchEnergyOptimization, runs []api.ProfilingRun, assessment MeasurementAssessment) []MeasurementFailure {
	failures := []MeasurementFailure{}
	owned := make([]*api.ProfilingRun, 0, len(runs))
	for i := range runs {
		run := &runs[i]
		if run.Namespace != task.Namespace {
			continue
		}
		for _, owner := range run.OwnerReferences {
			if owner.Controller != nil && *owner.Controller && owner.APIVersion == api.GroupVersion.String() &&
				owner.Kind == "BatchEnergyOptimization" && owner.Name == task.Name && owner.UID == task.UID {
				owned = append(owned, run)
				break
			}
		}
	}
	sort.Slice(owned, func(i, j int) bool {
		if owned[i].CreationTimestamp.Equal(&owned[j].CreationTimestamp) {
			return owned[i].Name > owned[j].Name
		}
		return owned[j].CreationTimestamp.Before(&owned[i].CreationTimestamp)
	})
	requestID := task.Namespace + "/" + task.Name + "/" + string(task.UID)
	for _, run := range owned {
		runAssessment := AssessProfilingRun(*run)
		if runAssessment.DisplayStatus != MeasurementProblem {
			continue
		}
		failure := runAssessment.FailureSummary[0]
		failure.RequestID = requestID
		failures = append(failures, failure)
	}
	if len(failures) == 0 && assessment.DisplayStatus == MeasurementProblem {
		reason := classifyMeasurementFailure(nil, task.Status.Conditions, task.Generation, task.Spec.State.Candidate.ExecutionPath)
		failures = append(failures, MeasurementFailure{
			RequestID: requestID, Code: reason.Code, Label: reason.Label, Message: reason.Message, Relation: "unresolved",
		})
	}
	return failures
}

// classifyMeasurementFailure applies ordered evidence rules, never returning raw
// condition messages or logs. Specific causes win over generic runtime exits.
func classifyMeasurementFailure(termination *api.ProfilingRunRuntimeTermination, conditions []metav1.Condition, generation int64, executionPath string) MeasurementReason {
	var evidence strings.Builder
	if termination != nil {
		evidence.WriteString(termination.LogTail)
	}
	for _, condition := range conditions {
		if condition.ObservedGeneration != generation || condition.Status != metav1.ConditionFalse {
			continue
		}
		evidence.WriteByte('\n')
		evidence.WriteString(condition.Reason)
		evidence.WriteByte(' ')
		evidence.WriteString(condition.Message)
	}
	detail := strings.ToLower(runtimeLogANSI.ReplaceAllString(evidence.String(), ""))
	arenaAllocationFailed := strings.Contains(detail, "bfcarena") && strings.Contains(detail, "failed to allocate memory for requested buffer")
	switch {
	case gpuMemoryFailure.MatchString(detail) || arenaAllocationFailed && strings.HasPrefix(executionPath, "firstfloor-ort-cuda-"):
		return MeasurementReason{Code: "GPUMemoryExhausted", Label: "GPU 메모리 부족", Message: "GPU 메모리가 부족해 측정을 완료하지 못했습니다."}
	case termination != nil && termination.ExitCode == 137 || strings.Contains(detail, "oomkilled") ||
		arenaAllocationFailed && strings.HasPrefix(executionPath, "firstfloor-ort-cpu-"):
		return MeasurementReason{Code: "HostOOM", Label: "호스트 메모리 부족", Message: "호스트 메모리 부족으로 측정 실행이 종료되었습니다."}
	case powerObservationTimeout.MatchString(detail):
		return MeasurementReason{Code: "PowerObservationTimeout", Label: "전력 관측 시간 초과", Message: "제한 시간 안에 전력 관측을 완료하지 못했습니다."}
	case imagePullFailure.MatchString(detail):
		return MeasurementReason{Code: "ImagePullFailed", Label: "이미지 가져오기 실패", Message: "실행 이미지를 가져오지 못해 측정을 시작하지 못했습니다."}
	case termination != nil && termination.ExitCode != 0 || runtimeExecutionFailure.MatchString(detail):
		return MeasurementReason{Code: "RuntimeFailed", Label: "실행 오류", Message: "실행 오류로 측정을 완료하지 못했습니다."}
	default:
		return MeasurementReason{Code: "Other", Label: "기타 오류", Message: "측정을 완료하지 못했습니다. 실행 상세에서 상태를 확인해 주세요."}
	}
}
