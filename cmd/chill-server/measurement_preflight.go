package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
)

const measurementPhaseMeasuring = "Measuring"

// enrichPreflightResponse uses the published catalog, never retained-result crawling.
func (o *measurementService) enrichPreflightResponse(response *http.Response) error {
	const maximumPreflightBytes = 4 << 20
	payload, err := io.ReadAll(io.LimitReader(response.Body, maximumPreflightBytes+1))
	closeErr := response.Body.Close()
	if errors.Join(err, closeErr) != nil || len(payload) > maximumPreflightBytes {
		return errors.New("read Console preflight response")
	}
	var view operatorconsole.BatchEnergyOptimizationPreflight
	if err := json.Unmarshal(payload, &view); err != nil {
		return err
	}
	parts := strings.Split(response.Request.URL.Path, "/")
	if len(parts) != 7 || view.Campaign.Namespace != parts[4] ||
		view.APIVersion != operatorconsole.BatchEnergyOptimizationPreflightAPIVersion || view.Kind != "BatchEnergyOptimizationPreflight" {
		return errors.New("unexpected Console preflight identity")
	}
	if view.Resolves {
		plan, err := profilederivation.DecodeCurvePlan(view.Plan)
		if err != nil {
			return err
		}
		requests, listErr := o.list(response.Request.Context(), view.Campaign.Namespace)
		snapshot := operatorconsole.BatchEnergyOptimizationPreflightSnapshot{
			Plan: plan, Campaign: api.BatchEnergyOptimization{Spec: view.Campaign.Spec},
			VariantID: response.Request.URL.Query().Get("variantId"), RequestsComplete: listErr == nil,
		}
		catalogEvidence, catalogConfirmed := o.preflightCatalogEvidence(
			response.Request.Context(), view.Campaign.Namespace, view.Campaign.Spec, requests.Items,
		)
		activeEvidence, activeReasons := o.preflightActiveEvidence(
			response.Request.Context(), view.Campaign.Spec, requests.Items,
		)
		snapshot.ExactEvidence = append(catalogEvidence, activeEvidence...)
		batchPlan := operatorconsole.BuildMeasurementBatchPlan(snapshot)
		batchPlan.Reasons = append(batchPlan.Reasons, activeReasons...)
		if !catalogConfirmed {
			batchPlan.Reasons = append(batchPlan.Reasons, operatorconsole.MeasurementBatchPlanReason{
				Code: "CatalogUnconfirmed", Message: "The current adopted catalog is unavailable; no measured batch exclusions are confirmed.",
			})
		}
		for _, reason := range view.BatchPlan.Reasons {
			if reason.Code == "PlannedRequestOverlap" || reason.Code == "RequestListUnconfirmed" {
				batchPlan.Reasons = append(batchPlan.Reasons, reason)
			}
		}
		view.BatchPlan = batchPlan
	}
	payload, err = json.Marshal(view)
	if err != nil {
		return err
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	response.ContentLength = int64(len(payload))
	response.Header.Set("Content-Length", strconv.Itoa(len(payload)))
	return nil
}

func samePreflightSpec(current, proposed api.BatchEnergyOptimizationSpec) bool {
	current.BatchSizes, proposed.BatchSizes = nil, nil
	return apiequality.Semantic.DeepEqual(current, proposed)
}

func (o *measurementService) preflightCatalogEvidence(
	ctx context.Context, namespace string, proposed api.BatchEnergyOptimizationSpec,
	requests []api.BatchEnergyOptimization,
) ([]operatorconsole.MeasurementPreflightEvidence, bool) {
	if o.scheduler == nil {
		return nil, false
	}
	catalog, err := o.scheduler.profilesView(ctx)
	if err != nil {
		return nil, false
	}
	evidence := []operatorconsole.MeasurementPreflightEvidence{}
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
		batches := make([]int, 0, len(profile.Batches))
		for _, batch := range profile.Batches {
			batches = append(batches, batch.Batch)
		}
		evidence = append(evidence, operatorconsole.MeasurementPreflightEvidence{
			Condition: profile.Condition, Batches: batches,
		})
	}
	return evidence, true
}

func (o *measurementService) preflightActiveEvidence(
	ctx context.Context, proposed api.BatchEnergyOptimizationSpec, requests []api.BatchEnergyOptimization,
) ([]operatorconsole.MeasurementPreflightEvidence, []operatorconsole.MeasurementBatchPlanReason) {
	evidence := []operatorconsole.MeasurementPreflightEvidence{}
	reasons := []operatorconsole.MeasurementBatchPlanReason{}
	for _, request := range requests {
		if !samePreflightSpec(request.Spec, proposed) || request.Status.Phase != measurementPhaseMeasuring {
			continue
		}
		frozen, err := o.preflightFrozenPlan(ctx, request)
		if err != nil {
			reasons = append(reasons, operatorconsole.MeasurementBatchPlanReason{
				Code: "InProgressIdentityUnconfirmed",
				Message: "Request " + request.Name +
					" is active but its current-generation frozen identity is not confirmed; it remains planned-only.",
			})
			continue
		}
		evidence = append(evidence, operatorconsole.MeasurementPreflightEvidence{
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
	raw, err := o.readDeletionEvidence(ctx, evidencestore.ObjectLocation{
		Store: o.opts.storeName, Key: "batch-energy-optimizations/" + string(request.UID) + "/curve-plan.json",
	}, true)
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
