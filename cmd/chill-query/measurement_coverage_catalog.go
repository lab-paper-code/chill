package main

import (
	"context"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
	querykube "github.com/lab-paper-code/chill/internal/queryapi/kube"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

type coverageCatalogSourceAdapter struct {
	read func(context.Context) (servingkube.CatalogRead, error)
}

func queryCatalogSource(read func(context.Context) (servingkube.CatalogRead, error)) querykube.CatalogSource {
	if read == nil {
		return nil
	}
	return coverageCatalogSourceAdapter{read: read}
}

func (a coverageCatalogSourceAdapter) Read(ctx context.Context) (queryapi.CoverageCatalogRead, error) {
	read, err := a.read(ctx)
	out := queryapi.CoverageCatalogRead{
		Reporting: queryCoverageSnapshot(read.CatalogSnapshot), SourceCheckedAt: read.SourceCheckedAt,
	}
	if read.Accepted != nil {
		accepted := queryCoverageSnapshot(*read.Accepted)
		out.Accepted = &accepted
		if read.Accepted.DispatchOccupancy != nil {
			out.AcceptedOccupancy = make([]queryapi.CoverageSelectedOccupancy, len(read.Accepted.DispatchOccupancy))
			for i, selected := range read.Accepted.DispatchOccupancy {
				out.AcceptedOccupancy[i] = queryapi.CoverageSelectedOccupancy{
					Node:  queryapi.MeasurementIdentity{Name: selected.Node.Name, UID: selected.Node.UID},
					Model: selected.Model, PowerMode: selected.PowerMode, ExecutionPath: selected.ExecutionPath,
					ProfileID: selected.ProfileID, Batch: selected.Batch,
				}
				if p := selected.Provenance; p != nil {
					out.AcceptedOccupancy[i].Provenance = &queryapi.SchedulerOccupancy{
						Source: p.Source, Value: p.Value, Pairs: p.Pairs,
						Window: (*queryapi.OccupancyMeasurementWindow)(p.Window), UpdatedAt: p.UpdatedAt,
					}
				}
			}
		}
		if read.Accepted.OccupancyEstimates != nil {
			out.ObservedOccupancy = make([]queryapi.OccupancyEstimate, len(read.Accepted.OccupancyEstimates))
			for i := range read.Accepted.OccupancyEstimates {
				out.ObservedOccupancy[i] = *queryOccupancyEstimate(&read.Accepted.OccupancyEstimates[i])
			}
		}
	}
	return out, err
}

func queryCoverageProfile(profile serving.Profile) queryapi.CoverageProfile {
	out := queryapi.CoverageProfile{
		ID: profile.ID, Reference: profile.Reference, Condition: profile.Condition,
		NodeName: profile.NodeName, MeasuredAt: profile.MeasuredAt,
		Batches: make([]int, len(profile.Batches)), Reuse: queryCoverageReuse(profile.Reuse),
		PowerIdentified: powerIdentified(profile.Power),
	}
	if profile.Reuse != nil {
		out.ReusePayload = profile.Reuse
	}
	for i, batch := range profile.Batches {
		out.Batches[i] = batch.Batch
	}
	return out
}

func queryCoverageReuse(reuse *serving.ProfileReuseCheck) *queryapi.CoverageReuseIdentity {
	if reuse == nil {
		return nil
	}
	return &queryapi.CoverageReuseIdentity{
		SourceProfileID: reuse.SourceProfileID, ConfirmationProfileID: reuse.ConfirmationProfileID, Outcome: reuse.Outcome,
	}
}

func queryCoverageCatalog(view httpapi.ProfilesView) queryapi.CoverageCatalog {
	out := queryapi.CoverageCatalog{
		ConfigVersion: view.ConfigVersion, Revision: view.Revision, SourceError: view.SourceError,
		CurrentMeasurements: view.CurrentMeasurements, MeasurementFacts: view.MeasurementFacts,
		Profiles:        make([]queryapi.CoverageProfile, len(view.Profiles)),
		Insufficiencies: queryCoverageIssues(view.Insufficiencies),
	}
	for i, profile := range view.Profiles {
		out.Profiles[i] = queryCoverageProfile(profile)
	}
	return out
}

func queryCoverageSnapshot(read servingkube.CatalogSnapshot) queryapi.CoverageCatalog {
	out := queryapi.CoverageCatalog{
		ConfigVersion: read.ConfigVersion, Revision: read.Revision, SourceError: read.SourceError,
		CurrentMeasurements: read.CurrentMeasurements,
		Profiles:            make([]queryapi.CoverageProfile, len(read.Profiles)),
		MeasurementFacts:    make(map[string]profilederivation.MeasurementFacts, len(read.Reporting)),
		Insufficiencies:     queryCoverageIssues(read.Insufficiencies),
	}
	for i, profile := range read.Profiles {
		out.Profiles[i] = queryapi.CoverageProfile{
			ID: profile.ID, Reference: profile.Reference, Condition: profile.Condition,
			NodeName: profile.NodeName, MeasuredAt: profile.MeasuredAt,
			Batches: make([]int, len(profile.Batches)), Reuse: queryCoverageReuse(profile.Reuse),
			PowerIdentified: powerIdentified(profile.Power),
		}
		if profile.Reuse != nil {
			out.Profiles[i].ReusePayload = profile.Reuse
		}
		for j, batch := range profile.Batches {
			out.Profiles[i].Batches[j] = batch.Batch
		}
	}
	for _, r := range read.Reporting {
		out.MeasurementFacts[r.ProfileID] = profilederivation.MeasurementFacts{
			Request: r.Request, ResultReference: r.ResultReference, Condition: r.Condition,
			PointPolicy: profilederivation.PointPolicy{MinimumTrials: r.MinimumTrials}, Batches: r.Batches,
		}
	}
	return out
}

func queryCoverageIssues(issues []servingkube.CatalogIssue) []queryapi.CoverageCatalogIssue {
	if issues == nil {
		return nil
	}
	out := make([]queryapi.CoverageCatalogIssue, len(issues))
	for i, issue := range issues {
		out[i] = queryapi.CoverageCatalogIssue{Source: issue.Source, Code: issue.Code, Reason: issue.Reason}
	}
	return out
}

// powerIdentified reports whether a session profile identified all three
// power parameters, nil for a profile without a power model.
func powerIdentified(power *serving.ProfilePower) *bool {
	if power == nil {
		return nil
	}
	identified := power.IdleWatts.Status() == profilederivation.ParameterIdentified &&
		power.AwakeStepWatts.Status() == profilederivation.ParameterIdentified &&
		power.HoldSeconds.Status() == profilederivation.ParameterIdentified
	return &identified
}
