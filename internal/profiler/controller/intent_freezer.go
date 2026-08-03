package controller

import (
	"context"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiler"
	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type RunPreparer interface {
	Prepare(context.Context, profiler.PreparationRequest) (profiler.PreparedRun, error)
}

type IntentRecordRepository interface {
	Ensure(context.Context, *edgev1alpha1.ProfilingRun, FrozenRun) (*corev1.ConfigMap, bool, error)
	Load(context.Context, *edgev1alpha1.ProfilingRun) (FrozenRun, error)
}

type AdmissionPolicyResolver interface {
	Resolve(context.Context, string) (profileradmission.FrozenPolicy, error)
}

// IntentFreezer coordinates only the durable transition from mutable observed
// facts to one immutable Run intent. It does not create execution resources.
type IntentFreezer struct {
	kubeClient client.Client
	preparer   RunPreparer
	policies   AdmissionPolicyResolver
	records    IntentRecordRepository
}

type FreezeIntentResult struct {
	Run             FrozenRun
	RecordCreated   bool
	RecordRecovered bool
	StatusUpdated   bool
	Stopped         bool
}

func NewIntentFreezer(
	kubeClient client.Client,
	preparer RunPreparer,
	policies AdmissionPolicyResolver,
	records IntentRecordRepository,
) (*IntentFreezer, error) {
	if kubeClient == nil || preparer == nil || policies == nil || records == nil {
		return nil, errors.New("Kubernetes client, Run preparer, admission-policy resolver, and intent records are required")
	}
	return &IntentFreezer{kubeClient: kubeClient, preparer: preparer, policies: policies, records: records}, nil
}

// Freeze makes the immutable record durable before publishing its digest in
// status. On restart it always recovers a pre-existing record before resolving
// mutable cluster facts again.
func (f *IntentFreezer) Freeze(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
) (FreezeIntentResult, error) {
	if run == nil {
		return FreezeIntentResult{}, errors.New("ProfilingRun is required")
	}
	if !run.DeletionTimestamp.IsZero() {
		return FreezeIntentResult{Stopped: true}, nil
	}

	if run.Status.IntentDigest != "" {
		frozen, err := f.records.Load(ctx, run)
		if err != nil {
			return FreezeIntentResult{}, fmt.Errorf("load recorded Run intent: %w", err)
		}
		if frozen.Prepared.IntentDigest != run.Status.IntentDigest {
			return FreezeIntentResult{}, errors.New("status intent digest does not match frozen intent record")
		}
		updated, err := f.publishStatus(ctx, run, frozen.Prepared.IntentDigest)
		if err != nil {
			return FreezeIntentResult{}, err
		}
		return FreezeIntentResult{Run: frozen, StatusUpdated: updated}, nil
	}

	frozen, err := f.records.Load(ctx, run)
	if err == nil {
		updated, err := f.publishStatus(ctx, run, frozen.Prepared.IntentDigest)
		if err != nil {
			return FreezeIntentResult{}, err
		}
		return FreezeIntentResult{
			Run: frozen, RecordRecovered: true, StatusUpdated: updated,
		}, nil
	}
	if !apierrors.IsNotFound(err) {
		return FreezeIntentResult{}, fmt.Errorf("recover frozen Run intent: %w", err)
	}

	requested, err := ProjectRequest(run.Spec)
	if err != nil {
		return FreezeIntentResult{}, err
	}
	prepared, err := f.preparer.Prepare(ctx, requested.Preparation)
	if err != nil {
		return FreezeIntentResult{}, fmt.Errorf("prepare Run intent: %w", err)
	}
	policy, err := f.policies.Resolve(ctx, requested.AdmissionPolicy)
	if err != nil {
		return FreezeIntentResult{}, fmt.Errorf("resolve admission policy: %w", err)
	}
	frozen = FrozenRun{Prepared: prepared, AdmissionPolicy: policy}
	_, created, err := f.records.Ensure(ctx, run, frozen)
	if err != nil {
		return FreezeIntentResult{}, fmt.Errorf("retain frozen Run intent: %w", err)
	}
	updated, err := f.publishStatus(ctx, run, prepared.IntentDigest)
	if err != nil {
		return FreezeIntentResult{}, err
	}
	return FreezeIntentResult{
		Run: frozen, RecordCreated: created, StatusUpdated: updated,
	}, nil
}

func (f *IntentFreezer) publishStatus(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	digest string,
) (bool, error) {
	if run.Status.IntentDigest == digest && run.Status.ObservedGeneration == run.Generation {
		return false, nil
	}
	if run.Status.IntentDigest != "" && run.Status.IntentDigest != digest {
		return false, errors.New("refusing to replace recorded Run intent digest")
	}

	base := run.DeepCopy()
	desired := run.DeepCopy()
	desired.Status.IntentDigest = digest
	desired.Status.ObservedGeneration = run.Generation
	if err := f.kubeClient.Status().Patch(ctx, desired, client.MergeFrom(base)); err != nil {
		return false, fmt.Errorf("publish frozen Run intent status: %w", err)
	}
	run.Status = desired.Status
	return true, nil
}
