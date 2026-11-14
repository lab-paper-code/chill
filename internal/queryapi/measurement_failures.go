package queryapi

import (
	"regexp"
	"sort"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Failure classes a ProfilingRun's runtime termination records.
const (
	failureClassExpectedStop     = "ExpectedStop"
	failureClassRetryableFailure = "RetryableFailure"
	failureClassUnknownFailure   = "UnknownFailure"
)

var (
	runtimeLogANSI          = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	gpuMemoryFailure        = regexp.MustCompile(`cuda[^\n]*(out of memory|out_of_memory)|cublas_status_alloc_failed|cudnn_status_alloc_failed|gpu out of memory`)
	powerObservationTimeout = regexp.MustCompile(`(?m)(metric observation|metric result|metric[- ]observer|power observation|observe power)[^\n]*(timeout|timed out|deadline exceeded)|(timeout|timed out|deadline exceeded)[^\n]*(metric observation|metric result|metric[- ]observer|power observation|observe power)`)
	imagePullFailure        = regexp.MustCompile(`imagepullbackoff|errimagepull|image pull failed|failed to pull image|failed to pull and unpack image`)
	runtimeExecutionFailure = regexp.MustCompile(`runtimefailed|runtime failed|runtime container failed|runtimeerror:|execute firstfloor measurement:|segmentation fault|illegal instruction`)
	runtimeFirstError       = regexp.MustCompile(`(?m)^[^\n]*(?:\bERROR\b|\bFATAL\b|\bpanic:|\berror=)[^\n]*`)
	runtimeErrorTime        = regexp.MustCompile(`^(?:\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}|\d{4}-\d{2}-\d{2}T[0-9:.]+(?:Z|[+-]\d{2}:\d{2}))`)
)

// MeasurementRequestFailures joins only exact native controller owners. A
// session request owns no Run: its one failure is its outcome and sentence.
func MeasurementRequestFailures(task api.BatchEnergyOptimization, runs []api.ProfilingRun, assessment MeasurementAssessment) []MeasurementFailure {
	if task.Spec.Session != nil {
		return sessionRequestFailures(task, assessment)
	}
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

// classifyMeasurementFailure prefers the typed terminal cause. Legacy log
// categories are diagnostic only and never establish a discovery stop.
func classifyMeasurementFailure(termination *api.ProfilingRunRuntimeTermination, conditions []metav1.Condition, generation int64, executionPath string) MeasurementReason {
	if termination != nil && termination.Class != "" {
		return classifyTypedMeasurementFailure(termination)
	}
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
		return MeasurementReason{Code: "GPUMemoryExhausted", Label: "GPU 메모리 오류 로그", Message: "로그에 GPU 메모리 할당 오류가 기록되었습니다. 런타임 OOM 종료는 확인되지 않았습니다."}
	case arenaAllocationFailed && strings.HasPrefix(executionPath, "firstfloor-ort-cpu-"):
		return MeasurementReason{Code: "MemoryAllocationFailed", Label: "메모리 할당 오류 로그", Message: "로그에 메모리 할당 오류가 기록되었습니다. 런타임 OOM 종료는 확인되지 않았습니다."}
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

func classifyTypedMeasurementFailure(termination *api.ProfilingRunRuntimeTermination) MeasurementReason {
	executionStop := termination.Class == failureClassExpectedStop &&
		termination.Container == "runtime" && termination.Stage == "Execution"
	switch {
	case termination.Class == failureClassRetryableFailure:
		return MeasurementReason{Code: "RetryableInfrastructureFailure", Label: "일시적 인프라 중단", Message: "일시적 인프라 중단이 확인되었습니다. 요청의 재시도 한도와 현재 상태를 확인해 주세요."}
	case termination.Class == failureClassUnknownFailure:
		return MeasurementReason{Code: "UnknownFailure", Label: "종료 원인 미확인", Message: "실행 종료 원인을 확인하지 못했습니다. 원본 상태와 로그를 확인해 주세요."}
	case termination.Reason == "OOMKilled" && termination.Container == "runtime":
		if executionStop {
			return MeasurementReason{Code: "HostOOM", Label: "런타임 OOM", Message: "실행 중 런타임 컨테이너의 OOM 종료가 확인되었습니다."}
		}
		return MeasurementReason{Code: "RuntimeFailed", Label: "런타임 OOM", Message: "런타임 컨테이너가 OOM으로 종료되었습니다. 배치 탐색의 정상 종료로 판정하지 않습니다."}
	case termination.Reason == "OOMKilled" && termination.Container != "":
		return MeasurementReason{Code: "MeasurementContainerOOM", Label: "측정 컨테이너 OOM", Message: "런타임 외 측정 컨테이너가 OOM으로 종료되었습니다. 모델 배치의 메모리 한계로 판정하지 않습니다."}
	case executionStop && termination.FailureReason == "MeasurementBudgetExceeded":
		return MeasurementReason{Code: "MeasurementBudgetExceeded", Label: "측정 예산 초과", Message: "실행 중 기존 측정 예산에 도달해 해당 배치의 측정이 종료되었습니다."}
	case executionStop && termination.FailureReason == "MemoryAllocationFailed":
		return MeasurementReason{Code: "MemoryAllocationFailed", Label: "런타임 메모리 할당 실패", Message: "실행 중 네이티브 할당자가 메모리 요청을 거절해 해당 배치의 탐색이 종료되었습니다. 커널 OOM 종료나 CPU·GPU 메모리 위치를 판정하지 않습니다."}
	case termination.FailureReason == "IntegrityError":
		return MeasurementReason{Code: "EvidenceInvalid", Label: "증거 검증 실패", Message: "측정 증거의 무결성을 확인하지 못했습니다."}
	default:
		return MeasurementReason{Code: "RuntimeFailed", Label: "실행 오류", Message: "실행 오류로 측정을 완료하지 못했습니다."}
	}
}
