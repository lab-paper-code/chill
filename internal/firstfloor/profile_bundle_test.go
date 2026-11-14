package firstfloor

import (
	"encoding/json"
	"testing"
)

func TestNativeBundleOmitsUnanchoredExecutions(t *testing.T) {
	batch := testBatch(1, 1, startupSample("trial", 100), pairedSample("trial", 0, 10))
	raw, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	var loaded BatchProfile
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Distribution.RecordCount() != batch.Distribution.RecordCount() || loaded.Distribution.DroppedFirstExecutions() != 0 {
		t.Fatal("bundle retained the first unanchored execution")
	}
	if mean, _ := loaded.Distribution.MeanNanos(); mean != 1e7 {
		t.Fatalf("unanchored execution changed service mean: %g", mean)
	}
}

func TestNativeBundleRejectsUnpairedOrInvalidRecords(t *testing.T) {
	for _, raw := range []string{
		`{"size":1,"serviceNanos":[10],"precedingIdleNanos":[],"energyPerItemJ":1}`,
		`{"size":1,"serviceNanos":[0],"precedingIdleNanos":[0],"energyPerItemJ":1}`,
		`{"size":1,"serviceNanos":[10],"precedingIdleNanos":[-1],"energyPerItemJ":1}`,
		`{"size":1,"serviceSamples":[{"serviceDurationNanos":10}],"energyPerItemJ":1}`,
	} {
		var batch BatchProfile
		if err := json.Unmarshal([]byte(raw), &batch); err == nil {
			t.Fatalf("accepted invalid native bundle: %s", raw)
		}
	}
}
