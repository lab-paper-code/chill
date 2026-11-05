package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/test/fixtures"
)

func TestTypedProfileReadNeverEncodesExcludedRawArrays(t *testing.T) {
	samples := make([]firstfloor.ServiceSample, 100000)
	for i := range samples {
		samples[i] = fixtures.ServiceSample("physical-trial", uint64(i), 123456789)
	}
	// The actual owner/query HTTP
	// projections must omit the excluded paired service population.
	samples[0].ServiceDurationNanos = 987654321
	profile := serving.Profile{
		ID:      "allowed/request/uid",
		Batches: []serving.BatchProfile{{Batch: 1, ServiceSamples: samples, MemoryBytes: 1024}},
	}
	owner := servingkube.CatalogView{
		Profiles:         []serving.Profile{profile},
		MeasurementFacts: map[string]serving.MeasurementFacts{"unselected": {}},
	}
	preparations := map[string]servingstate.PrepareSpec{
		profile.ID: {Name: "restore", Instance: serving.SchedulerInstance{Profile: &profile, ProfileID: profile.ID}},
	}
	snapshot := servingkube.ProjectCatalogSnapshot(owner, "rv", preparations)
	wire, err := json.Marshal(servingkube.CatalogRead{CatalogSnapshot: snapshot, Accepted: &snapshot})
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
		for _, key := range []string{"serviceSamples", "measurementFacts"} {
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
	if len(profile.Batches[0].ServiceSamples) != 100000 ||
		profile.Batches[0].ServiceSamples[0].ServiceDurationNanos != 987654321 ||
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
	for _, mode := range []string{"missing latest", "conflicting latest", "missing accepted"} {
		t.Run(mode, func(t *testing.T) {
			targetEnergy, sourceEnergy, freshEnergy := 3.0, 7.0, 99.0
			reuse := &serving.ProfileReuseCheck{
				SourceProfileID: "allowed/source/uid", ConfirmationProfileID: "allowed/target/uid",
				Outcome: queryapi.MeasurementVerdictAccepted,
			}
			profile := serving.Profile{
				ID: reuse.ConfirmationProfileID, Reuse: reuse, Batches: []serving.BatchProfile{{Batch: 1}},
			}
			accepted := httpapi.ProfilesView{
				ConfigVersion: "accepted-config",
				CatalogView: servingkube.CatalogView{
					Profiles: []serving.Profile{profile},
					Insufficiencies: []servingkube.CatalogIssue{
						{Code: "PreparationUnavailable", Reason: "accepted diagnostic"},
					},
					ReuseChecks: []serving.ProfileReuseCheck{*reuse},
					MeasurementFacts: map[string]serving.MeasurementFacts{
						profile.ID: {
							Batches: []profilederivation.MeasurementBatchFacts{
								{Batch: 1, TotalJoulesPerItem: &targetEnergy},
							},
						},
						reuse.SourceProfileID: {
							Batches: []profilederivation.MeasurementBatchFacts{
								{Batch: 1, TotalJoulesPerItem: &sourceEnergy},
							},
						},
					},
				},
				Preparations: map[string]servingstate.PrepareSpec{profile.ID: {Name: "accepted-preparation"}},
			}
			latest := servingkube.CatalogView{
				Insufficiencies: []servingkube.CatalogIssue{
					{Code: "LatestOnlyDiagnostic", Reason: "current reporting"},
				},
				MeasurementFacts: map[string]serving.MeasurementFacts{},
			}
			if mode != "missing latest" {
				latest.MeasurementFacts[profile.ID] = serving.MeasurementFacts{
					Batches: []profilederivation.MeasurementBatchFacts{{Batch: 1, TotalJoulesPerItem: &freshEnergy}},
				}
			}
			if mode == "missing accepted" {
				delete(accepted.MeasurementFacts, profile.ID)
			}
			expected, err := (&measurementService{}).buildProfiles(context.Background(), accepted)
			if err != nil {
				t.Fatal(err)
			}
			// Compare decoded wire values, including the existing omitempty rules.
			before, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			var expectedWire profileResponse
			if err := json.Unmarshal(before, &expectedWire); err != nil {
				t.Fatal(err)
			}
			selected := servingkube.ProjectCatalogSnapshot(
				accepted.CatalogView, accepted.ConfigVersion, accepted.Preparations,
			)
			raw, err := json.Marshal(servingkube.CatalogRead{
				CatalogSnapshot: servingkube.ProjectCatalogSnapshot(latest, "latest-config", nil), Accepted: &selected,
			})
			if err != nil {
				t.Fatal(err)
			}
			var read servingkube.CatalogRead
			if err := json.Unmarshal(raw, &read); err != nil {
				t.Fatal(err)
			}
			service := &measurementService{
				catalog: func(context.Context) (servingkube.CatalogRead, error) { return read, nil },
			}
			mux := http.NewServeMux()
			service.register(mux)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/profiles", nil))
			var got profileResponse
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || got.ConfigVersion != accepted.ConfigVersion ||
				!reflect.DeepEqual(got.Insufficiencies, accepted.Insufficiencies) ||
				!reflect.DeepEqual(got.ReuseChecks, accepted.ReuseChecks) ||
				got.Preparations[profile.ID].Name != "accepted-preparation" {
				t.Fatalf("accepted catalog metadata changed: %s", response.Body.String())
			}
			if !reflect.DeepEqual(got.Profiles, expectedWire.Profiles) {
				t.Fatalf("accepted resultRows/assessment changed or used latest facts: %s", response.Body.String())
			}
			wantRows := 2
			if mode == "missing accepted" {
				wantRows = 1 // the accepted source population remains; latest facts cannot repair the target.
			}
			if len(got.Profiles) != 1 || len(got.Profiles[0].ResultRows) != wantRows {
				t.Fatalf("accepted result populations disappeared: %s", response.Body.String())
			}
		})
	}
}
