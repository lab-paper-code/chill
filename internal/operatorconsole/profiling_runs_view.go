package operatorconsole

import (
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// ProfilingRunsSnapshot is one namespaced or all-namespaces LIST response. An
// empty Namespace means all namespaces; it does not imply ChillSystem ownership.
type ProfilingRunsSnapshot struct {
	ReadAt          time.Time
	ResourceVersion string
	Namespace       string
	Runs            []edgev1alpha1.ProfilingRun
}

// ProfilingRunsView is the bounded read model for a ProfilingRun listing. It
// keeps each Run's immutable spec and its status separate, retains the source
// resourceVersion, and states its own scope rather than letting a reader infer
// one from the namespace.
type ProfilingRunsView struct {
	ReadAt          string             `json:"readAt"`
	ResourceVersion string             `json:"resourceVersion"`
	Namespace       string             `json:"namespace,omitempty"`
	AllNamespaces   bool               `json:"allNamespaces"`
	Runs            []ProfilingRunView `json:"runs"`
}

type ProfilingRunView struct {
	Namespace         string                          `json:"namespace"`
	Name              string                          `json:"name"`
	UID               string                          `json:"uid"`
	ResourceVersion   string                          `json:"resourceVersion"`
	Generation        int64                           `json:"generation"`
	CreationTimestamp string                          `json:"creationTimestamp"`
	OwnerReferences   []metav1.OwnerReference         `json:"ownerReferences"`
	Labels            map[string]string               `json:"labels"`
	Spec              edgev1alpha1.ProfilingRunSpec   `json:"spec"`
	Status            edgev1alpha1.ProfilingRunStatus `json:"status"`
	Assessment        MeasurementAssessment           `json:"assessment"`
}

// BuildProfilingRuns projects one snapshot onto the wire model every Console
// transport shares. An empty snapshot Namespace means the read covered every
// namespace; it never implies ChillSystem ownership.
func BuildProfilingRuns(snapshot ProfilingRunsSnapshot) ProfilingRunsView {
	view := ProfilingRunsView{
		ReadAt: snapshot.ReadAt.Format(time.RFC3339Nano), ResourceVersion: snapshot.ResourceVersion,
		Namespace: snapshot.Namespace, AllNamespaces: snapshot.Namespace == "",
		Runs: make([]ProfilingRunView, 0, len(snapshot.Runs)),
	}
	for _, run := range snapshot.Runs {
		owners := make([]metav1.OwnerReference, len(run.OwnerReferences))
		for index := range run.OwnerReferences {
			run.OwnerReferences[index].DeepCopyInto(&owners[index])
		}
		labels := make(map[string]string)
		for _, key := range []string{
			"edge.dacs.io/batch-energy-optimization", "edge.dacs.io/workload-mode",
			"edge.dacs.io/batch", "edge.dacs.io/attempt",
		} {
			if value, exists := run.Labels[key]; exists {
				labels[key] = value
			}
		}
		view.Runs = append(view.Runs, ProfilingRunView{
			Namespace: run.Namespace, Name: run.Name, UID: string(run.UID),
			ResourceVersion: run.ResourceVersion, Generation: run.Generation,
			CreationTimestamp: run.CreationTimestamp.UTC().Format(time.RFC3339),
			OwnerReferences:   owners, Labels: labels,
			Spec: run.Spec, Status: run.Status,
			Assessment: AssessProfilingRun(run),
		})
	}
	return view
}

// AssessProfilingRun uses the recorded execution and independent verification
// conditions. A successful Job alone is not accepted measurement evidence.
func AssessProfilingRun(run edgev1alpha1.ProfilingRun) MeasurementAssessment {
	execution := &MeasurementExecution{Generation: run.Generation, ObservedGeneration: run.Status.ObservedGeneration}
	input := MeasurementAssessmentInput{Execution: execution, Attempted: true}
	evidenceInvalid := false
	if ref := run.Status.Evidence; ref != nil && ref.Store != "" && ref.Key != "" {
		input.Available = true
		input.Facts = append(input.Facts, "Retained evidence: "+ref.Store+"/"+ref.Key)
	}
	for _, condition := range run.Status.Conditions {
		if condition.ObservedGeneration != run.Generation {
			continue
		}
		switch condition.Type {
		case edgev1alpha1.ProfilingRunConditionExecutionComplete:
			execution.Phase = condition.Reason
			switch {
			case condition.Status == metav1.ConditionTrue:
				execution.Phase = "Complete"
			case condition.Reason == "JobRunning":
				execution.Phase = "Running"
			case condition.Reason == "JobPending":
				execution.Phase = "Pending"
			case condition.Status == metav1.ConditionFalse:
				execution.Phase = "Failed"
			}
		case edgev1alpha1.ProfilingRunConditionEvidenceVerified:
			input.Accepted = input.Available && condition.Status == metav1.ConditionTrue && run.Status.ObservedGeneration == run.Generation
			if condition.Status == metav1.ConditionFalse && condition.Reason == "EvidenceInvalid" {
				evidenceInvalid = true
			}
		}
	}
	if execution.Phase == "Failed" || evidenceInvalid {
		reason := classifyMeasurementFailure(run.Status.RuntimeTermination, run.Status.Conditions, run.Generation, run.Spec.Candidate.ExecutionPath)
		reason.SourceIDs = []string{string(run.UID)}
		if evidenceInvalid {
			input.Problems = append(input.Problems, reason)
		} else {
			input.Reasons = append(input.Reasons, reason)
		}
	}
	assessment := AssessMeasurement(input)
	if assessment.DisplayStatus == MeasurementProblem {
		reason := assessment.Reasons[len(assessment.Reasons)-1]
		failure := MeasurementFailure{
			RunID: string(run.UID), RunNamespace: run.Namespace, RunName: run.Name,
			Batch: ptr.To(int(run.Spec.Measurement.Batch)), Relation: "unresolved",
			Code: reason.Code, Label: reason.Label, Message: reason.Message,
		}
		if termination := run.Status.RuntimeTermination; termination != nil {
			failure.ExitCode = ptr.To(int(termination.ExitCode))
		}
		assessment.FailureSummary = []MeasurementFailure{failure}
	}
	return assessment
}
