package kube

import (
	"context"

	"github.com/lab-paper-code/chill/internal/queryapi"
)

// MeasurementResultReader joins results to the currently accepted measurement identity.
type MeasurementResultReader struct {
	ReadSource func(context.Context, string, string) (queryapi.BatchEnergyOptimizationResultSnapshot, error)
	Catalog    CatalogSource
}

func (p MeasurementResultReader) Read(
	ctx context.Context, namespace, name string,
) (queryapi.BatchEnergyOptimizationResultSnapshot, error) {
	snapshot, err := p.ReadSource(ctx, namespace, name)
	if err != nil || p.Catalog == nil {
		return snapshot, err
	}
	read, err := p.Catalog.Read(ctx)
	if err != nil || read.Accepted == nil || read.Accepted.CurrentMeasurements == nil {
		return snapshot, nil
	}
	used := false
	id := queryapi.CoverageRequestID(snapshot.Campaign)
	view := *read.Accepted
	for _, profile := range view.Profiles {
		profileID := profile.ID
		if profile.Reuse != nil {
			profileID = profile.Reuse.ConfirmationProfileID
		}
		if profileID == id && queryapi.MeasurementProfileCurrent(view, profile) {
			used = true
			break
		}
	}
	snapshot.SchedulerUsed = &used
	return snapshot, nil
}
