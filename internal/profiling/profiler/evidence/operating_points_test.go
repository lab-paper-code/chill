package evidence

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestActiveRawRetainsEachOperatingPointPowerWindow(t *testing.T) {
	config := loadRunConfig(t)
	raw := validRaw(t, config)
	payload, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded RawMeasurement
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Runtime.Phases) != 3 || len(decoded.Metrics.Samples) == 0 {
		t.Fatalf("raw phase windows/power samples missing: %s", payload)
	}
	for _, phase := range decoded.Runtime.Phases {
		from, err := time.Parse(time.RFC3339Nano, phase.MeasurementStartedAt)
		if err != nil {
			t.Fatal(err)
		}
		to, err := time.Parse(time.RFC3339Nano, phase.MeasurementEndedAt)
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, sample := range decoded.Metrics.Samples {
			if !sample.ObservedAt.Before(from) && !sample.ObservedAt.After(to) {
				found++
			}
		}
		if found < 2 {
			t.Fatalf("phase %s lacks measured node power population", phase.ID)
		}
	}
	if bytes.Contains(payload, []byte(`"repetitionResults"`)) || bytes.Contains(payload, []byte(`"phasePower"`)) || bytes.Contains(payload, []byte(`"dispatchOccupancyNanosPerInput"`)) {
		t.Fatal("raw evidence contains obsolete/derived fields")
	}
}
