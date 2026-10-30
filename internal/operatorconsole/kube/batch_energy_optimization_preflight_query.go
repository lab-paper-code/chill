package kube

import (
	"context"
	"errors"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubernetesBatchEnergyOptimizationPreflightQuery answers whether one submitted
// campaign request resolves. The campaign it reads need not exist in the
// cluster: the projection is what is being asked about, so the object travels
// in rather than being fetched by name.
type KubernetesBatchEnergyOptimizationPreflightQuery struct {
	reader    client.Reader
	projector func(context.Context, *edgev1alpha1.BatchEnergyOptimization) (profilederivation.CurvePlan, error)
	now       func() time.Time
}

func NewKubernetesBatchEnergyOptimizationPreflightQuery(
	reader client.Reader,
	projector func(context.Context, *edgev1alpha1.BatchEnergyOptimization) (profilederivation.CurvePlan, error),
) (*KubernetesBatchEnergyOptimizationPreflightQuery, error) {
	if reader == nil {
		return nil, errors.New("a Kubernetes preflight reader is required")
	}
	if projector == nil {
		return nil, errors.New("a curve-plan projector is required")
	}
	return &KubernetesBatchEnergyOptimizationPreflightQuery{reader: reader, projector: projector, now: time.Now}, nil
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

	plan, err := q.projector(ctx, campaign)
	snapshot := operatorconsole.BatchEnergyOptimizationPreflightSnapshot{
		ReadAt: q.now().UTC(), Campaign: *campaign.DeepCopy(),
	}
	if err != nil {
		snapshot.Reason = err.Error()
		return snapshot, nil
	}
	snapshot.Plan = plan
	var requests edgev1alpha1.BatchEnergyOptimizationList
	if err := q.reader.List(ctx, &requests, client.InNamespace(campaign.Namespace)); err != nil {
		return snapshot, nil
	}
	snapshot.RequestsComplete = true
	for i := range requests.Items {
		request := &requests.Items[i]
		current, proposed := request.Spec, campaign.Spec
		current.BatchSizes, proposed.BatchSizes = nil, nil
		if !apiequality.Semantic.DeepEqual(current, proposed) {
			continue
		}
		projected, err := q.projector(ctx, request)
		if err != nil {
			snapshot.RequestsComplete = false
			continue
		}
		snapshot.Requests = append(snapshot.Requests, operatorconsole.BatchEnergyOptimizationPreflightRequest{
			Campaign: *request.DeepCopy(), Plan: projected,
		})
	}
	return snapshot, nil
}
