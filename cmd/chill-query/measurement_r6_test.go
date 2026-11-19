package main

import (
	"context"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	"github.com/lab-paper-code/chill/test/fixtures"
)

func TestR6ProfileResponseOmitsRawCatalogAndLatencySamples(t *testing.T) {
	view := httpapi.ProfilesView{ConfigVersion: "rv"}
	samples := make([]firstfloor.ServiceSample, 100000)
	for i := range samples {
		samples[i] = fixtures.ServiceSample("physical-trial", uint64(i), 1000)
	}
	view.Profiles = []serving.Profile{{
		ID: "profile", Batches: []serving.BatchProfile{{Batch: 1, ServiceSamples: samples}},
	}}
	view.MeasurementFacts = map[string]serving.MeasurementFacts{"unselected": {}}
	raw, err := (&measurementService{}).profilePayload(context.Background(), view)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"serviceSamples", "measurementFacts"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("response contains internal/raw %s", key)
		}
	}
	// preparations is the catalog restore contract (관측 원시 기록 내보내기; targeted restoration)
	// and must survive the query projection.
	if !strings.Contains(string(raw), `"preparations"`) {
		t.Error("response dropped preparations")
	}
	if len(view.Profiles[0].Batches[0].ServiceSamples) != 100000 {
		t.Fatal("read projection mutated scheduler samples")
	}
}
