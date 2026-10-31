package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const executionJobHashCharacters = 32

type ExecutionJobOptions struct {
	WorkloadObserverServiceAccountName string
	WorkloadObserverKubeAPIServer      string
	MeasurementMemoryFraction          float64
	measurementMemoryLimitBytes        int64
}

// EvidenceStoreBindingResolver resolves the current CHILL-owned delivery
// binding. It returns no credential bytes.
type EvidenceStoreBindingResolver interface {
	Resolve(context.Context) (storekube.Binding, error)
}

// ExecutionJobs owns only deterministic Job materialization and recovery.
// Status publication remains the reconciler's responsibility.
type ExecutionJobs struct {
	client  client.Client
	scheme  *runtime.Scheme
	stores  EvidenceStoreBindingResolver
	options ExecutionJobOptions
}

func NewExecutionJobs(
	kubeClient client.Client,
	scheme *runtime.Scheme,
	stores EvidenceStoreBindingResolver,
	options ExecutionJobOptions,
) (*ExecutionJobs, error) {
	if kubeClient == nil || scheme == nil || stores == nil {
		return nil, errors.New("Kubernetes client, scheme, and Evidence Store resolver are required")
	}
	if math.IsNaN(options.MeasurementMemoryFraction) || options.MeasurementMemoryFraction <= 0 || options.MeasurementMemoryFraction >= 1 {
		return nil, errors.New("measurement memory fraction must be between 0 and 1 (exclusive)")
	}
	if strings.TrimSpace(options.WorkloadObserverServiceAccountName) == "" ||
		strings.TrimSpace(options.WorkloadObserverServiceAccountName) != options.WorkloadObserverServiceAccountName {
		return nil, errors.New("WorkloadObserver ServiceAccount name is required without surrounding whitespace")
	}
	if strings.TrimSpace(options.WorkloadObserverKubeAPIServer) != options.WorkloadObserverKubeAPIServer {
		return nil, errors.New("WorkloadObserver Kubernetes API server must not have surrounding whitespace")
	}
	return &ExecutionJobs{client: kubeClient, scheme: scheme, stores: stores, options: options}, nil
}

func (j *ExecutionJobs) Ensure(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	resolved ResolvedRun,
) (*batchv1.Job, bool, error) {
	jobName, err := ExecutionJobName(run)
	if err != nil {
		return nil, false, err
	}
	key := types.NamespacedName{Namespace: run.Namespace, Name: jobName}
	existing := &batchv1.Job{}
	if err := j.client.Get(ctx, key, existing); err == nil {
		retained, err := j.verifyRetainedExecutionJob(run, resolved, existing)
		return retained, false, err
	} else if !apierrors.IsNotFound(err) {
		return nil, false, fmt.Errorf("get measurement Job %s: %w", key, err)
	}

	binding, err := j.stores.Resolve(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("resolve Evidence Store delivery binding: %w", err)
	}
	node := &corev1.Node{}
	target := resolved.Prepared.RunConfig.TargetNode
	if err := j.client.Get(ctx, types.NamespacedName{Name: target.Name}, node); err != nil {
		return nil, false, fmt.Errorf("get measurement Node memory: %w", err)
	}
	if string(node.UID) != target.UID {
		return nil, false, errors.New("measurement Node UID changed since resolution")
	}
	allocatable, ok := node.Status.Allocatable[corev1.ResourceMemory]
	if !ok || allocatable.Sign() <= 0 {
		return nil, false, errors.New("measurement Node has no positive allocatable memory")
	}
	options := j.options
	options.measurementMemoryLimitBytes = int64(float64(allocatable.Value()) * options.MeasurementMemoryFraction)
	if options.measurementMemoryLimitBytes <= 0 {
		return nil, false, errors.New("measurement Node memory fraction produces no positive limit")
	}
	desired, err := buildExecutionJob(run, resolved, binding, options, j.scheme)
	if err != nil {
		return nil, false, err
	}
	if err := j.client.Create(ctx, desired); err == nil {
		return desired, true, nil
	} else if !apierrors.IsAlreadyExists(err) {
		return nil, false, fmt.Errorf("create measurement Job %s: %w", key, err)
	}
	if err := j.client.Get(ctx, key, existing); err != nil {
		return nil, false, fmt.Errorf("get concurrently created measurement Job %s: %w", key, err)
	}
	retained, err := j.verifyRetainedExecutionJob(run, resolved, existing)
	return retained, false, err
}

// Find returns a previously created Job and the exact configuration recorded
// in it. It is the restart path for a create/status-patch interruption.
func (j *ExecutionJobs) Find(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
) (*batchv1.Job, *ResolvedRun, error) {
	job, err := findRunJob(ctx, j.client, run)
	if err != nil || job == nil {
		return job, nil, err
	}
	resolved, err := resolvedRunFromJob(run, job)
	if err != nil {
		return nil, nil, err
	}
	return job, &resolved, nil
}

func (j *ExecutionJobs) verifyRetainedExecutionJob(
	run *edgev1alpha1.ProfilingRun,
	resolved ResolvedRun,
	retained *batchv1.Job,
) (*batchv1.Job, error) {
	binding, err := executionJobEvidenceStoreBinding(retained)
	if err != nil {
		return nil, err
	}
	// A retained Job is immutable: recover its original memory cap even if the
	// Node's allocatable memory or the configured fraction has since changed.
	options := j.options
	for _, container := range retained.Spec.Template.Spec.Containers {
		if container.Name == runtimeContainerName {
			options.measurementMemoryLimitBytes = container.Resources.Limits.Memory().Value()
		}
	}
	if options.measurementMemoryLimitBytes <= 0 {
		return nil, errors.New("retained measurement Job has no positive runtime memory limit")
	}
	desired, err := buildExecutionJob(run, resolved, binding, options, j.scheme)
	if err != nil {
		return nil, err
	}
	if err := verifyExecutionJob(run, resolved, retained, desired); err != nil {
		return nil, err
	}
	return retained, nil
}

func executionJobEvidenceStoreBinding(job *batchv1.Job) (storekube.Binding, error) {
	return storekube.BindingFromJob(job, runtimeContainerName)
}

// ExecutionJobName is the native producer identity shared with evidence cleanup.
func ExecutionJobName(run *edgev1alpha1.ProfilingRun) (string, error) {
	if run == nil || run.UID == "" {
		return "", errors.New("ProfilingRun UID is required")
	}
	sum := sha256.Sum256([]byte(run.UID))
	return "profilingrun-" + hex.EncodeToString(sum[:])[:executionJobHashCharacters], nil
}
