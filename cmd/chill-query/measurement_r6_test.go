package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	"github.com/lab-paper-code/chill/test/fixtures"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

func TestR6MissingOccupancyEvidenceDoesNotHideWholeList(t *testing.T) {
	cr := &api.DispatchOccupancyMeasurement{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "missing", UID: "uid", Generation: 1},
		Status: api.DispatchOccupancyMeasurementStatus{Phase: api.MeasurementPhaseComplete,
			Result: &api.ProfilingRunEvidenceReference{Store: "evidence", Key: "missing.json"}},
	}
	service := &measurementService{
		client:  fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(cr).Build(),
		objects: &operationProjectionStore{err: evidencestore.ErrNotFound},
		opts:    options{servingNamespace: "allowed", maxEvidenceBytes: 1 << 20},
	}
	list, err := service.listDispatchOccupancies(context.Background(), "allowed")
	if err != nil {
		t.Fatalf("one missing object hid native list: %v", err)
	}
	var assessment queryapi.MeasurementAssessment
	if len(list.Items) != 1 {
		t.Fatalf("missing record dropped: %+v", list)
	}
	if err := json.Unmarshal(list.Items[0].Assessment, &assessment); err != nil {
		t.Fatal(err)
	}
	if assessment.DisplayStatus != queryapi.MeasurementProblem || assessment.Evidence.Accepted ||
		assessment.Evidence.Available || len(assessment.Reasons) == 0 ||
		assessment.Reasons[0].Code != "OccupancyEvidenceMissing" {
		t.Fatalf("missing evidence falsely accepted or hidden: %+v", assessment)
	}
}
