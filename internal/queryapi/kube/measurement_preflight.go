package kube

import (
	"context"

	"errors"

	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const measurementPhaseMeasuring = "Measuring"

type MeasurementPreflightService struct {
	Reader           client.Reader
	Objects          evidencestore.LocationReader
	Catalog          CatalogSource
	Authorize        func(string) error
	StoreName        string
	MaxEvidenceBytes int64
}

// completePreflight combines source reads before the single HTTP encoding.
func (o *MeasurementPreflightService) Complete(
	ctx context.Context,
	snapshot queryapi.BatchEnergyOptimizationPreflightSnapshot,
) queryapi.BatchEnergyOptimizationPreflightSnapshot {
	if snapshot.Plan.SchemaVersion() == "" {
		return snapshot
	}
	requests, listErr := ListMeasurementRequests(ctx, o.Reader, snapshot.Campaign.Namespace, o.Authorize)
	snapshot.RequestsComplete = listErr == nil
	catalogEvidence, confirmed := o.preflightCatalogEvidence(
		ctx,
		snapshot.Campaign.Namespace,
		snapshot.Campaign.Spec,
		requests.Items,
	)
	active, reasons := o.preflightActiveEvidence(ctx, snapshot.Campaign.Spec, requests.Items)
	snapshot.ExactEvidence = append(catalogEvidence, active...)
	snapshot.BatchPlanReasons = append(snapshot.BatchPlanReasons, reasons...)
	if !confirmed {
		snapshot.BatchPlanReasons = append(snapshot.BatchPlanReasons, queryapi.MeasurementBatchPlanReason{
			Code:    "CatalogUnconfirmed",
			Message: "The current adopted catalog is unavailable; no measured batch exclusions are confirmed.",
		})
	}
	return snapshot
}

func samePreflightSpec(current, proposed api.BatchEnergyOptimizationSpec) bool {
	return apiequality.Semantic.DeepEqual(current, proposed)
}

func (o *MeasurementPreflightService) preflightCatalogEvidence(
	ctx context.Context, namespace string, proposed api.BatchEnergyOptimizationSpec,
	requests []api.BatchEnergyOptimization,
) ([]queryapi.MeasurementPreflightEvidence, bool) {
	if o.Catalog == nil {
		return nil, false
	}
	read, err := o.Catalog.Read(ctx)
	if err != nil {
		return nil, false
	}
	catalog := queryapi.CoverageCatalog{}
	if read.Accepted != nil {
		catalog = *read.Accepted
	}

	evidence := []queryapi.MeasurementPreflightEvidence{}
	for _, profile := range catalog.Profiles {
		if !strings.HasPrefix(profile.ID, namespace+"/") {
			continue
		}
		source := strings.Split(profile.ID, "/")
		matchedSpec := false
		for _, request := range requests {
			if len(source) < 3 || request.Name != source[1] || string(request.UID) != source[2] {
				continue
			}
			matchedSpec = samePreflightSpec(request.Spec, proposed)
		}
		if !matchedSpec {
			continue
		}
		batches := append([]int{}, profile.Batches...)
		evidence = append(evidence, queryapi.MeasurementPreflightEvidence{
			Condition: profile.Condition, Batches: batches,
		})
	}
	return evidence, true
}

func (o *MeasurementPreflightService) preflightActiveEvidence(
	ctx context.Context, proposed api.BatchEnergyOptimizationSpec, requests []api.BatchEnergyOptimization,
) ([]queryapi.MeasurementPreflightEvidence, []queryapi.MeasurementBatchPlanReason) {
	evidence := []queryapi.MeasurementPreflightEvidence{}
	reasons := []queryapi.MeasurementBatchPlanReason{}
	for _, request := range requests {
		if !samePreflightSpec(request.Spec, proposed) || request.Status.Phase != measurementPhaseMeasuring {
			continue
		}
		frozen, err := o.preflightFrozenPlan(ctx, request)
		if err != nil {
			reasons = append(reasons, queryapi.MeasurementBatchPlanReason{
				Code: "InProgressIdentityUnconfirmed",
				Message: "Request " + request.Name +
					" is active but its current-generation frozen identity is not confirmed; it remains planned-only.",
			})
			continue
		}
		evidence = append(evidence, queryapi.MeasurementPreflightEvidence{
			Condition: frozen.Shared(), Batches: frozen.PlannedBatchSizes(), InProgress: true,
		})
	}
	return evidence, reasons
}

func (o *MeasurementPreflightService) preflightFrozenPlan(
	ctx context.Context, request api.BatchEnergyOptimization,
) (profilederivation.CurvePlan, error) {
	if request.UID == "" || request.Generation < 1 || request.Status.ObservedGeneration != request.Generation ||
		o.Objects == nil || o.MaxEvidenceBytes < 1 {
		return profilederivation.CurvePlan{}, errors.New("current-generation frozen plan is unavailable")
	}
	raw, err := ReadMeasurementEvidence(ctx, o.Objects, o.MaxEvidenceBytes, evidencestore.ObjectLocation{
		Store: o.StoreName, Key: "batch-energy-optimizations/" + string(request.UID) + "/curve-plan.json",
	})
	if err != nil {
		return profilederivation.CurvePlan{}, err
	}
	frozen, err := profilederivation.DecodeCurvePlan(raw)
	if err != nil {
		return profilederivation.CurvePlan{}, err
	}
	if frozen.Shared().Name != request.Name {
		return profilederivation.CurvePlan{}, errors.New("frozen plan does not belong to the current request")
	}
	return frozen, nil
}

// A typed adapter completes preflight before readapi encodes the response.
type MeasurementPreflightReader struct {
	ReadSource func(context.Context, *api.BatchEnergyOptimization) (
		queryapi.BatchEnergyOptimizationPreflightSnapshot, error,
	)
	Enrichment *MeasurementPreflightService
}

func (p MeasurementPreflightReader) Read(
	ctx context.Context,
	campaign *api.BatchEnergyOptimization,
) (queryapi.BatchEnergyOptimizationPreflightSnapshot, error) {
	snapshot, err := p.ReadSource(ctx, campaign)
	if err != nil {
		return snapshot, err
	}
	return p.Enrichment.Complete(ctx, snapshot), nil
}
