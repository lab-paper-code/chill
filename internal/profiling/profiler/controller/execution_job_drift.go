package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const normalizedRunConfigArgument = "--run-config=<validated>"

func verifyExecutionJob(
	run *edgev1alpha1.ProfilingRun,
	expected ResolvedRun,
	existing, desired *batchv1.Job,
) error {
	if existing.Namespace != desired.Namespace || existing.Name != desired.Name || !metav1.IsControlledBy(existing, run) {
		return errors.New("existing measurement Job identity or ownership mismatch")
	}
	recorded, err := resolvedRunFromJob(run, existing)
	if err != nil {
		return fmt.Errorf("decode existing measurement Job record: %w", err)
	}
	if !reflect.DeepEqual(recorded, expected) {
		return errors.New("existing measurement Job execution identity mismatch")
	}
	existingPod := existing.Spec.Template.Spec.DeepCopy()
	desiredPod := desired.Spec.Template.Spec.DeepCopy()
	if err := normalizeRunConfigArguments(existingPod, expected.Prepared.RunConfig); err != nil {
		return fmt.Errorf("validate existing measurement Job RunConfig arguments: %w", err)
	}
	if err := normalizeRunConfigArguments(desiredPod, expected.Prepared.RunConfig); err != nil {
		return fmt.Errorf("validate desired measurement Job RunConfig arguments: %w", err)
	}
	// Job specs are immutable. Fake clients do not apply API defaults, while a
	// real API server does, so compare the fields CHILL owns before creation.
	if !apiequality.Semantic.DeepDerivative(*desiredPod, *existingPod) ||
		existing.Spec.Parallelism == nil || *existing.Spec.Parallelism != 1 ||
		existing.Spec.Completions == nil || *existing.Spec.Completions != 1 ||
		existing.Spec.BackoffLimit == nil || *existing.Spec.BackoffLimit != 0 ||
		existing.Spec.ActiveDeadlineSeconds == nil ||
		*existing.Spec.ActiveDeadlineSeconds != *desired.Spec.ActiveDeadlineSeconds {
		return errors.New("existing measurement Job does not match the recorded execution contract")
	}
	return nil
}

func normalizeRunConfigArguments(pod *corev1.PodSpec, expected profilingrun.RunConfig) error {
	if pod == nil {
		return errors.New("Pod spec is required")
	}
	for containerIndex := range pod.Containers {
		container := &pod.Containers[containerIndex]
		raw, err := argumentValue(container.Args, runConfigArgumentPrefix)
		if err != nil {
			return fmt.Errorf("container %q: %w", container.Name, err)
		}
		var recorded profilingrun.RunConfig
		if err := json.Unmarshal([]byte(raw), &recorded); err != nil {
			return fmt.Errorf("container %q: decode RunConfig: %w", container.Name, err)
		}
		if err := recorded.Validate(); err != nil {
			return fmt.Errorf("container %q: validate RunConfig: %w", container.Name, err)
		}
		if !reflect.DeepEqual(recorded, expected) {
			return fmt.Errorf("container %q RunConfig does not match the Job record", container.Name)
		}
		for argumentIndex := range container.Args {
			if strings.HasPrefix(container.Args[argumentIndex], runConfigArgumentPrefix) {
				container.Args[argumentIndex] = normalizedRunConfigArgument
			}
		}
	}
	return nil
}
