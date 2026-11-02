package main

import (
	"context"

	"errors"

	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
)

const measurementPhaseMeasuring = "Measuring"

// completePreflight combines source reads before the single HTTP encoding.
func (o *measurementService) completePreflight(
	ctx context.Context,
	snapshot queryapi.BatchEnergyOptimizationPreflightSnapshot,
) queryapi.BatchEnergyOptimizationPreflightSnapshot {
	if snapshot.Plan.SchemaVersion() == "" {
		return snapshot
	}
	requests, listErr := o.list(ctx, snapshot.Campaign.Namespace)
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
	current.BatchSizes, proposed.BatchSizes = nil, nil
	return apiequality.Semantic.DeepEqual(current, proposed)
}

func (o *measurementService) preflightCatalogEvidence(
	ctx context.Context, namespace string, proposed api.BatchEnergyOptimizationSpec,
	requests []api.BatchEnergyOptimization,
) ([]queryapi.MeasurementPreflightEvidence, bool) {
	if o.catalog == nil {
		return nil, false
	}
	read, err := o.catalog(ctx)
	if err != nil {
		return nil, false
	}
	adopted := map[string]bool{}
	for _, id := range read.Adopted {
		adopted[id] = true
	}
	catalog := catalogView(read)

	evidence := []queryapi.MeasurementPreflightEvidence{}
	for _, profile := range catalog.Profiles {
		if !adopted[profile.ID] || !strings.HasPrefix(profile.ID, namespace+"/") {
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
		batches := make([]int, 0, len(profile.Batches))
		for _, batch := range profile.Batches {
			batches = append(batches, batch.Batch)
		}
		evidence = append(evidence, queryapi.MeasurementPreflightEvidence{
			Condition: profile.Condition, Batches: batches,
		})
	}
	return evidence, true
}

func (o *measurementService) preflightActiveEvidence(
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

func (o *measurementService) preflightFrozenPlan(
	ctx context.Context, request api.BatchEnergyOptimization,
) (profilederivation.CurvePlan, error) {
	if request.UID == "" || request.Generation < 1 || request.Status.ObservedGeneration != request.Generation ||
		o.objects == nil || o.opts.maxEvidenceBytes < 1 {
		return profilederivation.CurvePlan{}, errors.New("current-generation frozen plan is unavailable")
	}
	raw, err := o.readEvidence(ctx, evidencestore.ObjectLocation{
		Store: o.opts.storeName, Key: "batch-energy-optimizations/" + string(request.UID) + "/curve-plan.json",
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
