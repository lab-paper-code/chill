package kube

import (
	"context"
	"errors"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

// CurvePlanProjector resolves the immutable plan one campaign selects. The
// Console declares only the port it needs and the composition root supplies
// the implementation, as it does for ResultObjectReader, so a read surface
// never depends on the controller that owns the projection.
type CurvePlanProjector interface {
	Project(context.Context, *edgev1alpha1.BatchEnergyOptimization) (profilederivation.CurvePlan, error)
}

// KubernetesBatchEnergyOptimizationPreflightQuery answers whether one submitted
// campaign request resolves. The campaign it reads need not exist in the
// cluster: the projection is what is being asked about, so the object travels
// in rather than being fetched by name.
type KubernetesBatchEnergyOptimizationPreflightQuery struct {
	projector CurvePlanProjector
	now       func() time.Time
}

func NewKubernetesBatchEnergyOptimizationPreflightQuery(
	projector CurvePlanProjector,
) (*KubernetesBatchEnergyOptimizationPreflightQuery, error) {
	if projector == nil {
		return nil, errors.New("a curve-plan projector is required")
	}
	return &KubernetesBatchEnergyOptimizationPreflightQuery{projector: projector, now: time.Now}, nil
}

// Read records projection failures as reasons because an unresolved request is
// the answer the caller requested, rather than a failed query.
func (q *KubernetesBatchEnergyOptimizationPreflightQuery) Read(
	ctx context.Context,
	campaign *edgev1alpha1.BatchEnergyOptimization,
) (operatorconsole.BatchEnergyOptimizationPreflightSnapshot, error) {
	if campaign == nil {
		return operatorconsole.BatchEnergyOptimizationPreflightSnapshot{}, errors.New("a BatchEnergyOptimization campaign is required")
	}
	if err := validateObjectReference("BatchEnergyOptimization", campaign.Namespace, campaign.Name); err != nil {
		return operatorconsole.BatchEnergyOptimizationPreflightSnapshot{}, err
	}

	plan, err := q.projector.Project(ctx, campaign)
	snapshot := operatorconsole.BatchEnergyOptimizationPreflightSnapshot{
		ReadAt: q.now().UTC(), Campaign: *campaign.DeepCopy(),
	}
	if err != nil {
		snapshot.Reason = err.Error()
		return snapshot, nil
	}
	snapshot.Plan = plan
	return snapshot, nil
}
