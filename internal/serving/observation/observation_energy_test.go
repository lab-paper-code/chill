package observation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

func TestObservationEnergyTermsRoundTrip(t *testing.T) {
	log, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	at := time.Now().UTC()
	raw := []byte(`{
		"periodNanos":600000000000,"routeNanos":1000000000,"search":{"excludedUndecidable":1},
		"plans":[
			{"node":{"name":"genesis2","uid":"g2"},"key":"keep",
			 "idleCosts":{"before":{"source":"exact","watts":118.44,"references":["profile"],"powerMode":"GPU_1500","state":["profile"],"requestWatts":8.77,"requestReferences":["profile"]}},
			 "actionCosts":[{"action":"prepare","kind":"prepare","seconds":35.2,"joules":127,"provenance":{"source":"node","costRef":"cpu-prepare"}},
			                {"action":"remove","kind":"remove","seconds":1,"joules":0,"provenance":{"source":"exact","costRef":"zero-remove"}}]},
			{"node":{"name":"genesis2","uid":"g2"},"key":"unselected",
			 "idleCosts":{"before":{"source":"exact","watts":0,"references":["unloaded"]}},
			 "actionCosts":[{"action":"prepare","kind":"prepare","provenance":{"source":"node","costRef":"cpu-prepare"}}]}
		],
		"missingCells":[{"node":{"name":"orangepi","uid":"opi"},"kind":"prepare","powerMode":"fixed","from":"unloaded","to":"rknn-profile","required":"node-wall operation seconds and joules for this exact action cell"}],
		"energyComparison":{"status":"undecidable","missingCellRefs":[0]},
		"undecidableCandidates":[{"key":"rknn","excluded":true,"missingCellRefs":[0]},
		                        {"key":"rknn-other","excluded":true,"missingCellRefs":[0]}]
	}`)
	var capacity serving.SchedulerCapacity
	if err = json.Unmarshal(raw, &capacity); err != nil {
		t.Fatal(err)
	}
	d := serving.SchedulerDecision{At: at, Capacity: &capacity}
	if err = log.SchedulerRecord(SchedulerObservation{At: at, Decision: &d}); err != nil {
		t.Fatal(err)
	}
	if err = log.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, _, _, _, err := log.SchedulerObservationPage(context.Background(), at, at.Add(time.Second), 1, 0, SchedulerObservationSelection{})
	if err != nil || len(page) != 1 {
		t.Fatalf("page=%+v error=%v", page, err)
	}
	encoded, err := json.Marshal(page[0].Decision.Capacity)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	plans := got["plans"].([]any)
	before := plans[0].(map[string]any)["idleCosts"].(map[string]any)["before"].(map[string]any)
	if before["watts"] != 118.44 || before["requestWatts"] != 8.77 || before["powerMode"] != "GPU_1500" || len(before["state"].([]any)) != 1 {
		t.Fatalf("node power terms lost: %s", encoded)
	}
	comparison, ok := got["energyComparison"].(map[string]any)
	if !ok || comparison["status"] != "undecidable" || len(comparison["missingCellRefs"].([]any)) != 1 {
		t.Fatalf("missing measurement lost: %s", encoded)
	}
	action := plans[0].(map[string]any)["actionCosts"].([]any)[0].(map[string]any)
	if action["seconds"] != 35.2 || action["joules"] != float64(127) {
		t.Fatalf("action prediction lost: %s", encoded)
	}
	stored := page[0].Decision.Capacity
	if len(stored.MissingCells) != 1 || len(stored.UndecidableCandidates) != 2 {
		t.Fatalf("shared missing-cell table lost: %s", encoded)
	}
	for _, candidate := range stored.UndecidableCandidates {
		if len(candidate.MissingCellRefs) != 1 || candidate.MissingCellRefs[0] != 0 ||
			stored.MissingCells[candidate.MissingCellRefs[0]].To != "rknn-profile" {
			t.Fatalf("stored cell reference does not resolve: %+v", candidate)
		}
	}
	if _, inline := comparison["missingCells"]; inline {
		t.Fatalf("comparison repeated missing cells: %s", encoded)
	}
	zero := plans[0].(map[string]any)["actionCosts"].([]any)[1].(map[string]any)
	if zero["joules"] != float64(0) {
		t.Fatalf("selected zero prediction lost: %s", encoded)
	}
	unselected := plans[1].(map[string]any)
	idle := unselected["idleCosts"].(map[string]any)["before"].(map[string]any)
	for _, field := range []string{"powerMode", "state", "requestWatts", "requestReferences"} {
		if _, present := idle[field]; present {
			t.Fatalf("unselected idle prediction appeared: %s", encoded)
		}
	}
	unselectedAction := unselected["actionCosts"].([]any)[0].(map[string]any)
	for _, field := range []string{"seconds", "joules"} {
		if _, present := unselectedAction[field]; present {
			t.Fatalf("unselected action prediction appeared: %s", encoded)
		}
	}
}
