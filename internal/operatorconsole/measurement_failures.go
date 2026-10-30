package operatorconsole

import (
	"regexp"
	"sort"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"k8s.io/utils/ptr"
)

var runtimeLogANSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

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
		failure := MeasurementFailure{
			RequestID: requestID, RunID: string(run.UID), RunNamespace: run.Namespace, RunName: run.Name,
			Batch: ptr.To(int(run.Spec.Measurement.Batch)), Relation: "unresolved", Code: "RunFailed",
		}
		for _, reason := range runAssessment.Reasons {
			failure.Code, failure.Message = reason.Code, shortFailureMessage(reason.Message)
		}
		if termination := run.Status.RuntimeTermination; termination != nil {
			failure.ExitCode = ptr.To(int(termination.ExitCode))
			if message := runtimeFailureLine(termination.LogTail); message != "" {
				failure.Message = message
			}
		}
		failures = append(failures, failure)
	}
	if len(failures) == 0 && assessment.DisplayStatus == MeasurementProblem {
		message := task.Status.Outcome
		for _, condition := range task.Status.Conditions {
			if condition.Message != "" {
				message = condition.Message
			}
		}
		failures = append(failures, MeasurementFailure{
			RequestID: requestID, Code: task.Status.Outcome, Message: shortFailureMessage(message), Relation: "unresolved",
		})
	}
	return failures
}

func runtimeFailureLine(log string) string {
	lines := strings.Split(runtimeLogANSI.ReplaceAllString(log, ""), "\n")
	for _, line := range lines {
		lower := strings.ToLower(line)
		for _, marker := range []string{"error", "failed", "out of memory", "oomkilled", "timed out", "timeout", "deadline exceeded"} {
			if strings.Contains(lower, marker) {
				return shortFailureMessage(line)
			}
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return shortFailureMessage(lines[i])
		}
	}
	return ""
}

func shortFailureMessage(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	runes := []rune(message)
	if len(runes) > 160 {
		return string(runes[:160])
	}
	return message
}
