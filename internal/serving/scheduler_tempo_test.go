package serving

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestSchedulerTwoTempoSettings(t *testing.T) {
	s, _ := schedulerFixture()
	margin := 0.0
	in := SchedulerSettingsInput{SchedulerSettings: s, CompletionMarginMS: &margin}
	got, err := in.Settings()
	if err != nil || got.ScaleDownStabilization != 300*time.Second || got.TransitionReserveFraction != 0 {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	for _, d := range []time.Duration{-1, 0, 1, 120 * time.Second, 300 * time.Second, time.Duration(math.MaxInt64)} {
		in.ScaleDownStabilization = &d
		got, err = in.Settings()
		if (err == nil) != (d >= 0) || err == nil && got.ScaleDownStabilization != d {
			t.Fatalf("duration %v: %+v %v", d, got, err)
		}
	}
	for _, f := range []float64{0, 1, .25, -.1, 1.1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		s.TransitionReserveFraction = f
		if (s.Validate() == nil) != (schedulerFinite(f) && f >= 0 && f <= 1) {
			t.Fatalf("fraction %v", f)
		}
	}
	for _, value := range []string{"9223372036854775808", "1.5"} {
		if json.Unmarshal([]byte(`{"scaleDownStabilizationNanos":`+value+`}`), &in) == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	s.TransitionReserveFraction = 0
	s.ComparisonPeriod = 0
	if s.Validate() == nil {
		t.Fatal("accepted zero horizon")
	}
}
