package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	evidencekube "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence/kube"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	runtimeContainerName             = "runtime"
	maxFinalizationObjectBytes int64 = 64 << 20
)

type FinalizedRunEvidence struct {
	Reference     evidencestore.ObjectIdentity
	SchemaVersion string
	Binding       *edgev1alpha1.ProfilingRunEvidenceStoreBinding
	Pod           edgev1alpha1.ProfilingRunObjectReference
	Node          edgev1alpha1.ProfilingRunObjectReference
}

type EvidenceObjectStoreFactory func(storekube.Binding, string, string) (profilerevidence.ObjectStore, error)

// KubernetesExecutionFinalizer reads terminal native objects and delegates
// immutable evidence construction to the Kubernetes-free evidence finalizer.
// It never runs measurement work or retains Run state between reconciles.
type KubernetesExecutionFinalizer struct {
	reader       client.Reader
	now          func() time.Time
	storeFactory EvidenceObjectStoreFactory
}

func NewKubernetesExecutionFinalizer(
	reader client.Reader,
	storeFactory EvidenceObjectStoreFactory,
) (*KubernetesExecutionFinalizer, error) {
	if reader == nil || storeFactory == nil {
		return nil, errors.New("Kubernetes API reader and evidence object-store factory are required")
	}
	return &KubernetesExecutionFinalizer{
		reader: reader, now: time.Now, storeFactory: storeFactory,
	}, nil
}

func (f *KubernetesExecutionFinalizer) Finalize(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	prepared profiler.PreparedRun,
	job *batchv1.Job,
) (FinalizedRunEvidence, error) {
	if run == nil || job == nil {
		return FinalizedRunEvidence{}, errors.New("ProfilingRun and measurement Job are required")
	}
	if err := validatePreparedForRun(run, prepared); err != nil {
		return FinalizedRunEvidence{}, err
	}
	if run.Status.Job == nil || run.Status.Job.Name != job.Name || run.Status.Job.UID != string(job.UID) ||
		!metav1.IsControlledBy(job, run) {
		return FinalizedRunEvidence{}, errors.New("measurement Job does not match the recorded Run execution")
	}

	binding, err := executionJobEvidenceStoreBinding(job)
	if err != nil {
		return FinalizedRunEvidence{}, err
	}
	credentials, err := storekube.LoadCredentials(ctx, f.reader, run.Namespace, binding.CredentialsSecretName)
	if err != nil {
		return FinalizedRunEvidence{}, err
	}
	store, err := f.storeFactory(binding, credentials.AccessKeyID, credentials.SecretAccessKey)
	if err != nil {
		return FinalizedRunEvidence{}, fmt.Errorf("construct retained execution evidence store: %w", err)
	}
	domainFinalizer, err := profilerevidence.NewFinalizer(store, maxFinalizationObjectBytes)
	if err != nil {
		return FinalizedRunEvidence{}, fmt.Errorf("construct execution evidence finalizer: %w", err)
	}
	coordinator, err := evidencekube.NewCoordinator(domainFinalizer)
	if err != nil {
		return FinalizedRunEvidence{}, fmt.Errorf("construct terminal evidence coordinator: %w", err)
	}

	observation, err := f.observe(ctx, run, job)
	if err != nil {
		return FinalizedRunEvidence{}, err
	}
	actuation, err := observeActuation(run, prepared, observation.Node)
	if err != nil {
		return FinalizedRunEvidence{}, err
	}
	result, err := coordinator.Finalize(ctx, evidencekube.FinalizeObservationRequest{
		Observation:          observation,
		ReceiptContainerName: runtimeContainerName,
		FinalLocation: evidencestore.ObjectLocation{
			Store: binding.Store,
			Key:   finalizedEvidenceObjectKey(run.UID),
		},
		Actuation:   actuation,
		FinalizedAt: f.now().UTC(),
	})
	if err != nil {
		return FinalizedRunEvidence{}, err
	}
	return FinalizedRunEvidence{
		Reference: result.Reference, SchemaVersion: result.SchemaVersion,
		Binding: &edgev1alpha1.ProfilingRunEvidenceStoreBinding{
			Store: binding.Store, Endpoint: binding.Endpoint, Bucket: binding.Bucket,
			Region: binding.Region, CredentialsSecretName: binding.CredentialsSecretName,
		},
		Pod: edgev1alpha1.ProfilingRunObjectReference{
			Name: observation.Pod.Name, UID: string(observation.Pod.UID),
		},
		Node: edgev1alpha1.ProfilingRunObjectReference{
			Name: observation.Node.Name, UID: string(observation.Node.UID),
		},
	}, nil
}

func (f *KubernetesExecutionFinalizer) observe(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	job *batchv1.Job,
) (evidencekube.Observation, error) {
	pod, err := f.jobPod(ctx, run, job)
	if err != nil {
		return evidencekube.Observation{}, err
	}
	if pod == nil {
		return evidencekube.Observation{}, errors.New("expected one Pod owned by measurement Job, found 0")
	}
	if pod.Spec.NodeName == "" {
		return evidencekube.Observation{}, errors.New("measurement Pod has no bound Node")
	}
	node := &corev1.Node{}
	if err := f.reader.Get(ctx, types.NamespacedName{Name: pod.Spec.NodeName}, node); err != nil {
		return evidencekube.Observation{}, fmt.Errorf("get measurement Node %q: %w", pod.Spec.NodeName, err)
	}
	nodePods := &corev1.PodList{}
	if err := f.reader.List(ctx, nodePods, client.MatchingFields{"spec.nodeName": pod.Spec.NodeName}); err != nil {
		return evidencekube.Observation{}, fmt.Errorf("list workloads on measurement Node %q: %w", pod.Spec.NodeName, err)
	}
	return evidencekube.Observation{
		Job: job.DeepCopy(), Pod: pod, Node: node,
		NodePods: nodePods.Items, ObservedAt: f.now().UTC(),
	}, nil
}

// RuntimeTermination reads the failed runtime container's exit code and log
// tail. It returns nil when the Pod is gone or the runtime did not fail.
func (f *KubernetesExecutionFinalizer) RuntimeTermination(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	job *batchv1.Job,
) (*edgev1alpha1.ProfilingRunRuntimeTermination, error) {
	pod, err := f.jobPod(ctx, run, job)
	if err != nil || pod == nil {
		return nil, err
	}
	for _, status := range pod.Status.ContainerStatuses {
		if terminated := status.State.Terminated; status.Name == runtimeContainerName &&
			terminated != nil && terminated.ExitCode != 0 {
			return &edgev1alpha1.ProfilingRunRuntimeTermination{
				ExitCode: terminated.ExitCode, LogTail: terminated.Message,
			}, nil
		}
	}
	return nil, nil
}

// jobPod returns the one Pod owned by the measurement Job, or nil when none remains.
func (f *KubernetesExecutionFinalizer) jobPod(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	job *batchv1.Job,
) (*corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := f.reader.List(ctx, pods, client.InNamespace(run.Namespace)); err != nil {
		return nil, fmt.Errorf("list measurement Pods: %w", err)
	}
	owned := make([]corev1.Pod, 0, 1)
	for i := range pods.Items {
		owner := metav1.GetControllerOf(&pods.Items[i])
		if owner != nil && owner.Kind == "Job" && owner.UID == job.UID {
			owned = append(owned, *pods.Items[i].DeepCopy())
		}
	}
	switch len(owned) {
	case 0:
		return nil, nil
	case 1:
		return &owned[0], nil
	default:
		return nil, fmt.Errorf("expected one Pod owned by measurement Job, found %d", len(owned))
	}
}
