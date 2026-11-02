package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

func TestCapturedNativeSelectionPreservesLatestFailureAndOperatorReplacement(t *testing.T) {
	dir := t.TempDir()
	put := func(name, data string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cells := put("cells.json", `[{"id":"m/c/p/fixed","model":{"name":"m"},"deviceClass":{"name":"c"},"executionPath":"p","powerMode":"fixed","variants":[{"id":"condition-old","requestIds":["n/old/u1"],"assessment":{"status":"complete","displayStatus":"usable","evidence":{"available":true,"accepted":true}}},{"id":"condition-new","requestIds":["n/new/u2"],"assessment":{"status":"failed","displayStatus":"problem","execution":{"phase":"Failed","generation":1,"observedGeneration":1}}}]}]`)
	profiles := put("profiles.json", `{"profiles":[{"id":"n/old/u1","measuredAt":"2026-01-01T00:00:00Z"}]}`)
	coverage := put("coverage.json", `{"sources":[{"kind":"BEO","complete":true},{"kind":"Run","complete":true}],"summary":{"countsByDisplayStatus":{"usable":1}},"cells":[{"id":"m/c/p/fixed","assessment":{"displayStatus":"usable"}}]}`)
	for _, override := range []bool{false, true} {
		annotations := `{}`
		if override {
			annotations = `{"` + profilederivation.ReplacesMeasurementAnnotation + `":"new"}`
		}
		requests := put("requests.json", `{"items":[{"kind":"BatchEnergyOptimization","metadata":{"namespace":"n","name":"old","uid":"u1","generation":1,"annotations":`+annotations+`},"status":{"phase":"Complete","observedGeneration":1}},{"kind":"BatchEnergyOptimization","metadata":{"namespace":"n","name":"new","uid":"u2","generation":1},"status":{"phase":"Failed","observedGeneration":1,"conditions":[{"type":"MeasurementComplete","status":"False","observedGeneration":1,"lastTransitionTime":"2026-01-02T00:00:00Z"}]}}]}`)
		original, err := os.ReadFile(requests)
		if err != nil {
			t.Fatal(err)
		}
		assessed := put("assessed-requests.json", string(original))
		report, err := inspect(cells, requests, profiles, coverage, assessed)
		if err != nil {
			t.Fatal(err)
		}
		selected := report["currentMeasurements"].([]map[string]any)
		id := selected[0]["selection"].(*profilederivation.MeasurementSelection).ID
		if !override && (id != "n/new/u2" || report["currentFailedMeasurements"] != 1 || report["countsByDisplayStatus"].(map[string]any)["problem"] != 1) {
			t.Fatalf("latest failed was hidden: %v", report)
		}
		if override && (id != "n/old/u1" || report["currentFailedMeasurements"] != 0 || report["countsByDisplayStatus"].(map[string]any)["usable"] != 1) {
			t.Fatalf("explicit replacement was ignored: %v", report)
		}
		for _, transition := range []string{"new-native-measurement", "captured-failure-completed"} {
			var tasks measurementList
			if err := json.Unmarshal(original, &tasks); err != nil {
				t.Fatal(err)
			}
			if transition == "new-native-measurement" {
				task := tasks.Items[1]
				task.Name, task.UID = "newer", "u3"
				tasks.Items = append(tasks.Items, task)
			} else {
				tasks.Items[1].Status.Phase = "Complete"
				tasks.Items[1].Status.Conditions[0].Status = "True"
			}
			raw, err := json.Marshal(tasks)
			if err != nil {
				t.Fatal(err)
			}
			put("requests.json", string(raw))
			stale, err := inspect(cells, requests, profiles, coverage, assessed)
			if err != nil {
				t.Fatal(err)
			}
			if stale["projectionAvailable"] != false || stale["currentMeasurements"] != nil || stale["countsByDisplayStatus"] != nil {
				t.Fatalf("%s invented a current assessment: %v", transition, stale)
			}
		}
		put("requests.json", string(original))
		incomplete := put("incomplete-coverage.json", `{"sources":[{"kind":"BEO","complete":true},{"kind":"Run","complete":false}],"summary":{"countsByDisplayStatus":{"unmeasured":null}}}`)
		unknown, err := inspect(cells, requests, profiles, incomplete, assessed)
		if err != nil {
			t.Fatal(err)
		}
		if unknown["countsBefore"].(map[string]*int)["unmeasured"] != nil || unknown["countsByDisplayStatus"].(map[string]any)["unmeasured"] != nil {
			t.Fatalf("unknown count became zero: %v", unknown)
		}
	}
}
