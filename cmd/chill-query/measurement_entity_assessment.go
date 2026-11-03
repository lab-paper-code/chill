package main

import (
	"context"

	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

type profileProjection struct {
	servingkube.ProfileSummary
	Assessment queryapi.MeasurementAssessment `json:"assessment"`
	ResultRows []queryapi.ResultRow           `json:"resultRows"`
}
type profileResponse struct {
	ConfigVersion   string                                    `json:"configVersion"`
	Profiles        []profileProjection                       `json:"profiles"`
	Insufficiencies []servingkube.CatalogIssue                `json:"insufficiencies"`
	ReuseChecks     []serving.ProfileReuseCheck               `json:"reuseChecks,omitempty"`
	Preparations    map[string]servingkube.PreparationSummary `json:"preparations"`
	Page            *httpapi.ProfilePage                      `json:"page,omitempty"`
	Filters         httpapi.ProfileFilters                    `json:"filters"`
}

func (o *measurementService) buildProfiles(ctx context.Context, view httpapi.ProfilesView) (profileResponse, error) {
	if err := ctx.Err(); err != nil {
		return profileResponse{}, err
	}
	queryView := queryCoverageCatalog(view)
	profiles := make([]profileProjection, 0, len(view.Profiles))
	for i, profile := range view.Profiles {
		assessment, rows := queryapi.ProjectMeasurementProfile(queryView, queryView.Profiles[i])
		profiles = append(profiles, profileProjection{servingkube.SummarizeProfile(profile), assessment, rows})
	}
	return profileResponse{
		view.ConfigVersion,
		profiles,
		view.Insufficiencies,
		view.ReuseChecks,
		servingkube.SummarizePreparations(view.Preparations),
		view.Page,
		view.Filters,
	}, nil
}
