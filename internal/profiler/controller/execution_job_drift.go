package controller

import (
	"errors"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func verifyExecutionJob(run *edgev1alpha1.ProfilingRun, existing, desired *batchv1.Job) error {
	if existing.Namespace != desired.Namespace || existing.Name != desired.Name || !metav1.IsControlledBy(existing, run) {
		return errors.New("existing measurement Job identity or ownership mismatch")
	}
	if existing.Annotations[intentDigestAnnotation] != desired.Annotations[intentDigestAnnotation] ||
		existing.Annotations[executionPolicyAnnotation] != desired.Annotations[executionPolicyAnnotation] {
		return errors.New("existing measurement Job execution identity mismatch")
	}
	// Job specs are immutable. Fake clients do not apply API defaults, while a
	// real API server does, so compare the fields CHILL owns before creation.
	if !apiequality.Semantic.DeepDerivative(desired.Spec.Template.Spec, existing.Spec.Template.Spec) ||
		existing.Spec.Parallelism == nil || *existing.Spec.Parallelism != 1 ||
		existing.Spec.Completions == nil || *existing.Spec.Completions != 1 ||
		existing.Spec.BackoffLimit == nil || *existing.Spec.BackoffLimit != 0 ||
		existing.Spec.ActiveDeadlineSeconds == nil ||
		*existing.Spec.ActiveDeadlineSeconds != *desired.Spec.ActiveDeadlineSeconds {
		return errors.New("existing measurement Job does not match the frozen execution contract")
	}
	return nil
}
