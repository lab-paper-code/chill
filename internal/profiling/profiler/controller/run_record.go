package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	runConfigArgumentPrefix   = "--run-config="
	executionPolicyAnnotation = "edge.dacs.io/profiling-execution-policy"
	actuationPolicyAnnotation = "edge.dacs.io/profiling-actuation-policy"
)

func findRunJob(
	ctx context.Context,
	reader client.Reader,
	run *edgev1alpha1.ProfilingRun,
) (*batchv1.Job, error) {
	name, err := ExecutionJobName(run)
	if err != nil {
		return nil, err
	}
	job := &batchv1.Job{}
	key := types.NamespacedName{Namespace: run.Namespace, Name: name}
	if err := reader.Get(ctx, key, job); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get measurement Job %s: %w", key, err)
	}
	if !metav1.IsControlledBy(job, run) {
		return nil, errors.New("deterministically named measurement Job is not controlled by ProfilingRun")
	}
	return job, nil
}

func resolvedRunFromJob(run *edgev1alpha1.ProfilingRun, job *batchv1.Job) (ResolvedRun, error) {
	if run == nil || job == nil || !metav1.IsControlledBy(job, run) {
		return ResolvedRun{}, errors.New("ProfilingRun and its controlled measurement Job are required")
	}
	runtimeContainer, err := namedContainer(job.Spec.Template.Spec.Containers, runtimeContainerName)
	if err != nil {
		return ResolvedRun{}, err
	}
	rawConfig, err := argumentValue(runtimeContainer.Args, runConfigArgumentPrefix)
	if err != nil {
		return ResolvedRun{}, err
	}
	var config profilingrun.RunConfig
	if err := json.Unmarshal([]byte(rawConfig), &config); err != nil {
		return ResolvedRun{}, fmt.Errorf("decode measurement Job RunConfig: %w", err)
	}
	if err := config.Validate(); err != nil {
		return ResolvedRun{}, fmt.Errorf("validate measurement Job RunConfig: %w", err)
	}

	executionPolicy, err := profiler.DecodeFrozenExecutionPolicy(
		[]byte(job.Annotations[executionPolicyAnnotation]),
	)
	if err != nil {
		return ResolvedRun{}, fmt.Errorf("decode measurement Job execution policy: %w", err)
	}
	prepared := profiler.PreparedRun{RunConfig: config}
	if rawActuation, present := job.Annotations[actuationPolicyAnnotation]; present {
		policy, err := profiler.DecodeFrozenActuationPolicy([]byte(rawActuation))
		if err != nil {
			return ResolvedRun{}, fmt.Errorf("decode measurement Job actuation policy: %w", err)
		}
		prepared.ActuationPolicy = &policy
	}
	resolved := ResolvedRun{
		Prepared: prepared, ExecutionPolicy: executionPolicy,
	}
	if err := validateResolvedRunForRun(run, resolved); err != nil {
		return ResolvedRun{}, fmt.Errorf("validate measurement Job RunConfig: %w", err)
	}
	return resolved, nil
}

func namedContainer(containers []corev1.Container, name string) (corev1.Container, error) {
	var matched *corev1.Container
	for index := range containers {
		if containers[index].Name != name {
			continue
		}
		if matched != nil {
			return corev1.Container{}, fmt.Errorf("measurement Job contains multiple %q containers", name)
		}
		copy := containers[index].DeepCopy()
		matched = copy
	}
	if matched == nil {
		return corev1.Container{}, fmt.Errorf("measurement Job omits %q container", name)
	}
	return *matched, nil
}

func argumentValue(args []string, prefix string) (string, error) {
	var value string
	for _, argument := range args {
		if !strings.HasPrefix(argument, prefix) {
			continue
		}
		if value != "" {
			return "", fmt.Errorf("measurement Job contains multiple %q arguments", prefix)
		}
		value = strings.TrimPrefix(argument, prefix)
	}
	if value == "" {
		return "", fmt.Errorf("measurement Job omits %q argument", prefix)
	}
	return value, nil
}
