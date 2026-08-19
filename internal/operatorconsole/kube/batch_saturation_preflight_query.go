package kube

import (
	"context"
	"errors"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	"github.com/lab-paper-code/chill/internal/profilederivation"
)

// CurvePlanProjector resolves the immutable plan one campaign selects. The
// Console declares only the port it needs and the composition root supplies
// the implementation, as it does for ResultObjectReader, so a read surface
// never depends on the controller that owns the projection.
type CurvePlanProjector interface {
	Project(context.Context, *edgev1alpha1.BatchSaturation) (profilederivation.CurvePlan, error)
}

// KubernetesBatchSaturationPreflightQuery answers whether one submitted
// campaign's intent resolves. The campaign it reads need not exist in the
// cluster: the projection is what is being asked about, so the object travels
// in rather than being fetched by name.
type KubernetesBatchSaturationPreflightQuery struct {
	projector CurvePlanProjector
	now       func() time.Time
}

func NewKubernetesBatchSaturationPreflightQuery(
	projector CurvePlanProjector,
) (*KubernetesBatchSaturationPreflightQuery, error) {
	if projector == nil {
		return nil, errors.New("a curve-plan projector is required")
	}
	return &KubernetesBatchSaturationPreflightQuery{projector: projector, now: time.Now}, nil
}

// Read records projection failures as reasons because an intent that cannot be
// frozen is the answer the caller requested, rather than a failed query.
func (q *KubernetesBatchSaturationPreflightQuery) Read(
	ctx context.Context,
	campaign *edgev1alpha1.BatchSaturation,
) (operatorconsole.BatchSaturationPreflightSnapshot, error) {
	if campaign == nil {
		return operatorconsole.BatchSaturationPreflightSnapshot{}, errors.New("a BatchSaturation campaign is required")
	}
	if err := validateObjectReference("BatchSaturation", campaign.Namespace, campaign.Name); err != nil {
		return operatorconsole.BatchSaturationPreflightSnapshot{}, err
	}

	plan, err := q.projector.Project(ctx, campaign)
	snapshot := operatorconsole.BatchSaturationPreflightSnapshot{
		ReadAt: q.now().UTC(), Campaign: *campaign.DeepCopy(),
	}
	if err != nil {
		snapshot.Reason = err.Error()
		return snapshot, nil
	}
	snapshot.Plan = plan
	return snapshot, nil
}
