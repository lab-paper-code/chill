package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	"github.com/lab-paper-code/chill/internal/profiler"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	evidencekube "github.com/lab-paper-code/chill/internal/profiler/evidence/kube"
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
	Reference evidencestore.ObjectIdentity
	Pod       edgev1alpha1.ProfilingRunObjectReference
	Node      edgev1alpha1.ProfilingRunObjectReference
}

type RunEvidenceFinalizer interface {
	Finalize(context.Context, *edgev1alpha1.ProfilingRun, profiler.PreparedRun, *batchv1.Job) (FinalizedRunEvidence, error)
}

type EvidenceObjectStoreFactory func(EvidenceStoreBinding, string, string) (profilerevidence.ObjectStore, error)

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
	if _, err := validatePreparedForRun(run, prepared); err != nil {
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
	result, err := coordinator.Finalize(ctx, evidencekube.FinalizeObservationRequest{
		Observation:          observation,
		Intent:               prepared.Intent,
		CandidateReportBytes: prepared.CandidateReportBytes,
		ReceiptContainerName: runtimeContainerName,
		FinalLocation: evidencestore.ObjectLocation{
			Store: binding.Store,
			Key:   finalizedEvidenceObjectKey(run.UID, prepared.IntentDigest),
		},
		FinalizedAt: f.now().UTC(),
	})
	if err != nil {
		return FinalizedRunEvidence{}, err
	}
	return FinalizedRunEvidence{
		Reference: result.Reference,
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
	pods := &corev1.PodList{}
	if err := f.reader.List(ctx, pods, client.InNamespace(run.Namespace)); err != nil {
		return evidencekube.Observation{}, fmt.Errorf("list measurement Pods: %w", err)
	}
	owned := make([]corev1.Pod, 0, 1)
	for i := range pods.Items {
		owner := metav1.GetControllerOf(&pods.Items[i])
		if owner != nil && owner.Kind == "Job" && owner.UID == job.UID {
			owned = append(owned, *pods.Items[i].DeepCopy())
		}
	}
	if len(owned) != 1 {
		return evidencekube.Observation{}, fmt.Errorf("expected one Pod owned by measurement Job, found %d", len(owned))
	}
	pod := &owned[0]
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

func finalizedEvidenceObjectKey(uid types.UID, digest string) string {
	return "profiling-runs/" + string(uid) + "/execution/" + strings.TrimPrefix(digest, "sha256:") + ".json"
}
