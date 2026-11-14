package kube

import (
	"context"
	"errors"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubernetesBatchEnergyOptimizationPreflightQuery answers whether one submitted
// campaign request resolves. The campaign it reads need not exist in the
// cluster: the projection is what is being asked about, so the object travels
// in rather than being fetched by name.
type KubernetesBatchEnergyOptimizationPreflightQuery struct {
	reader client.Reader
	now    func() time.Time
	// Session resolves a request with session settings, the call the session
	// runner prepares a measurement with.
	Session func(context.Context, *edgev1alpha1.BatchEnergyOptimization) (queryapi.SessionPreflightPlan, error)
}

func NewKubernetesBatchEnergyOptimizationPreflightQuery(
	reader client.Reader,
) (*KubernetesBatchEnergyOptimizationPreflightQuery, error) {
	if reader == nil {
		return nil, errors.New("a Kubernetes preflight reader is required")
	}
	return &KubernetesBatchEnergyOptimizationPreflightQuery{reader: reader, now: time.Now}, nil
}

// Read records projection failures as reasons because an unresolved request is
// the answer the caller requested, rather than a failed query.
func (q *KubernetesBatchEnergyOptimizationPreflightQuery) Read(
	ctx context.Context,
	campaign *edgev1alpha1.BatchEnergyOptimization,
) (queryapi.BatchEnergyOptimizationPreflightSnapshot, error) {
	if campaign == nil {
		return queryapi.BatchEnergyOptimizationPreflightSnapshot{}, errors.New("a BatchEnergyOptimization campaign is required")
	}
	if err := validateObjectReference("BatchEnergyOptimization", campaign.Namespace, campaign.Name); err != nil {
		return queryapi.BatchEnergyOptimizationPreflightSnapshot{}, err
	}
	snapshot := queryapi.BatchEnergyOptimizationPreflightSnapshot{ReadAt: q.now().UTC(), Campaign: *campaign.DeepCopy()}
	if campaign.Spec.Session == nil {
		snapshot.Reason = "the measurement has no session settings"
		return snapshot, nil
	}
	if q.Session == nil {
		snapshot.Reason = "this Query API does not resolve measurement sessions"
		return snapshot, nil
	}
	plan, err := q.Session(ctx, campaign)
	if err != nil {
		snapshot.Reason = err.Error()
		return snapshot, nil
	}
	snapshot.SessionPlan = &plan
	return snapshot, nil
}
