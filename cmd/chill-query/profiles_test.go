package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
)

func TestProfileListStatusMatchesDecoratedAssessmentWithoutClusterReads(t *testing.T) {
	view := httpapi.ProfilesView{}
	view.Profiles = []serving.Profile{{ID: "missing-facts"}, {ID: "reused", Reuse: &serving.ProfileReuseCheck{}}}
	listed := view
	listed.DisplayStatus = func(p serving.Profile) string { a, _ := assessCatalogProfile(view, p); return a.DisplayStatus }
	raw, err := (&measurementService{}).profilePayload(context.Background(), listed)
	if err != nil {
		t.Fatal(err)
	}
	var decorated struct {
		Profiles []struct {
			ID         string `json:"id"`
			Assessment struct {
				DisplayStatus string `json:"displayStatus"`
			} `json:"assessment"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &decorated); err != nil {
		t.Fatal(err)
	}
	for index, profile := range decorated.Profiles {
		status := listed.DisplayStatus(listed.Profiles[index])
		if status != profile.Assessment.DisplayStatus || status == "" {
			t.Fatalf(
				"%s list status %q differs from assessment %q",
				profile.ID,
				status,
				profile.Assessment.DisplayStatus,
			)
		}
	}
}
