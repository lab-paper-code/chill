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
	raw := []byte(`{"periodNanos":600000000000,"routeNanos":1000000000,"search":{"excludedUndecidable":1},"plans":[{"node":{"name":"genesis2","uid":"g2"},"key":"keep","idleCosts":{"before":{"source":"exact","watts":118.44,"references":["profile"],"powerMode":"GPU_1500","state":["profile"],"requestWatts":8.77,"requestReferences":["profile"]}},"actionCosts":[{"action":"prepare","kind":"prepare","seconds":35.2,"joules":127,"provenance":{"source":"node","costRef":"cpu-prepare"}}]}],"energyComparison":{"status":"undecidable","missingCells":[{"node":{"name":"orangepi","uid":"opi"},"kind":"prepare","powerMode":"fixed","from":"unloaded","to":"rknn-profile","required":"node-wall operation seconds and joules for this exact action cell"}]},"undecidableCandidates":[{"key":"rknn","excluded":true,"missingCells":[{"node":{"name":"orangepi","uid":"opi"},"kind":"prepare","powerMode":"fixed","required":"node-wall operation seconds and joules for this exact action cell"}]}]}`)
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
	if !ok || comparison["status"] != "undecidable" || len(comparison["missingCells"].([]any)) != 1 {
		t.Fatalf("missing measurement lost: %s", encoded)
	}
	action := plans[0].(map[string]any)["actionCosts"].([]any)[0].(map[string]any)
	if action["seconds"] != 35.2 || action["joules"] != float64(127) {
		t.Fatalf("action prediction lost: %s", encoded)
	}
}
