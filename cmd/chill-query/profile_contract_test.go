package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

func TestTypedProfileReadNeverEncodesExcludedRawArrays(t *testing.T) {
	samples := make([]float64, 100000)
	// JSON cannot encode this value. Success proves the actual owner/query HTTP
	// projections never visit the excluded latency vector, even temporarily.
	samples[0] = math.NaN()
	profile := serving.Profile{
		ID:      "allowed/request/uid",
		Batches: []serving.BatchProfile{{Batch: 1, DurationNanos: samples, MemoryBytes: 1024}},
	}
	owner := servingkube.CatalogView{
		Profiles:         []serving.Profile{profile},
		MeasurementFacts: map[string]serving.MeasurementFacts{"unselected": {}},
	}
	preparations := map[string]servingkube.PrepareSpec{
		profile.ID: {Name: "restore", Instance: serving.SchedulerInstance{Profile: &profile, ProfileID: profile.ID}},
	}
	wire, err := json.Marshal(servingkube.ProjectCatalogRead(owner, "rv", preparations, []string{profile.ID}))
	if err != nil {
		t.Fatalf("owner serialized excluded raw data: %v", err)
	}
	var read servingkube.CatalogRead
	if err := json.Unmarshal(wire, &read); err != nil {
		t.Fatal(err)
	}
	service := &measurementService{catalog: func(context.Context) (servingkube.CatalogRead, error) { return read, nil }}
	mux := http.NewServeMux()
	service.register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/profiles?profile="+profile.ID+"&limit=1", nil),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("public projection failed: %d %s", response.Code, response.Body.String())
	}
	for _, body := range []string{string(wire), response.Body.String()} {
		for _, key := range []string{"durationNanos", "measurementFacts"} {
			if strings.Contains(body, `"`+key+`"`) {
				t.Fatalf("raw field serialized: %s", key)
			}
		}
	}
	var got struct {
		ConfigVersion string `json:"configVersion"`
		Profiles      []struct {
			ID string `json:"id"`
		} `json:"profiles"`
		Preparations map[string]servingkube.PreparationSummary `json:"preparations"`
		Page         *httpapi.ProfilePage                      `json:"page"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	restored := got.Preparations[profile.ID]
	if got.ConfigVersion != "rv" || len(got.Profiles) != 1 || got.Profiles[0].ID != profile.ID || got.Page == nil ||
		got.Page.Total != 1 ||
		restored.Name != "restore" ||
		restored.Instance.Profile == nil ||
		restored.Instance.Profile.ID != profile.ID ||
		restored.Instance.Profile.Batches[0].MemoryBytes != 1024 {
		t.Fatalf("profile paging/restore contract lost: %s", response.Body.String())
	}
	if len(profile.Batches[0].DurationNanos) != 100000 || !math.IsNaN(profile.Batches[0].DurationNanos[0]) ||
		owner.MeasurementFacts == nil ||
		preparations[profile.ID].Instance.Profile != &profile {
		t.Fatal("read projection mutated scheduler originals")
	}
	t.Logf(
		"typed-profile: rawSamples=%d ownerBytes=%d publicBytes=%d preparations=1 rawEncoded=false",
		len(samples),
		len(wire),
		response.Body.Len(),
	)
}

func TestAcceptedCatalogProfilesRemainVisibleWithNewerReportingFacts(t *testing.T) {
	profile := serving.Profile{ID: "prior-adopted"}
	read := servingkube.ProjectCatalogRead(
		servingkube.CatalogView{Profiles: []serving.Profile{profile}},
		"accepted-config",
		map[string]servingkube.PrepareSpec{profile.ID: {Name: "accepted-preparation"}},
		[]string{profile.ID},
	)
	read.Reporting = []servingkube.CatalogReportingInput{{ProfileID: "new-unadopted"}}
	service := &measurementService{catalog: func(context.Context) (servingkube.CatalogRead, error) { return read, nil }}
	mux := http.NewServeMux()
	service.register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/profiles", nil))
	var got profileResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || got.ConfigVersion != read.ConfigVersion || len(got.Profiles) != 1 ||
		got.Profiles[0].ID != profile.ID || got.Preparations[profile.ID].Name != "accepted-preparation" {
		t.Fatalf("newer reporting hid the accepted profile or restoration data: %s", response.Body.String())
	}
}
