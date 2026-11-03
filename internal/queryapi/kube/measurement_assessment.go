package kube

import (
	"context"
	"encoding/json"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func AssessMeasurementRequests(
	ctx context.Context, reader client.Reader, catalog CatalogSource, list api.BatchEnergyOptimizationList,
) ([]json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	view := queryapi.CoverageCatalog{}
	reporting := queryapi.CoverageCatalog{}
	var catalogUnavailable queryapi.MeasurementReason
	if catalog != nil {
		snapshot, err := catalog.Read(ctx)
		if err != nil {
			catalogUnavailable = queryapi.MeasurementReason{Code: "CatalogUnavailable", Message: err.Error()}
		} else {
			if snapshot.Accepted != nil {
				view = *snapshot.Accepted
			}
			reporting = snapshot.Reporting
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var runs api.ProfilingRunList
	if len(list.Items) > 0 {
		if err := reader.List(ctx, &runs, client.InNamespace(list.Items[0].Namespace)); err != nil {
			return nil, err
		}
	}
	owners := queryapi.CoverageOwnedRuns(list.Items, runs.Items)
	assessments := make([]json.RawMessage, len(list.Items))
	for i, task := range list.Items {
		var err error
		assessment := queryapi.AssessMeasurementRequest(task, view, reporting)
		if catalogUnavailable.Code != "" {
			assessment.Reasons = append(assessment.Reasons, catalogUnavailable)
		}
		assessment.FailureSummary = queryapi.MeasurementRequestFailures(task, owners[string(task.UID)], assessment)
		assessments[i], err = json.Marshal(assessment)
		if err != nil {
			return nil, err
		}
	}
	return assessments, nil
}
