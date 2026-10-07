package v1alpha1_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateWorkload(t *testing.T) {
	valid := edgev1alpha1.WorkloadSpec{
		ModelRef: edgev1alpha1.WorkloadModelReference{Name: "detector.v2"},
		SLO:      edgev1alpha1.WorkloadSLO{Percentile: 99, TargetLatency: metav1.Duration{Duration: 100 * time.Millisecond}},
	}
	for _, tc := range []struct {
		name       string
		objectName string
		modelName  string
		percentile float64
		latency    time.Duration
		valid      bool
	}{
		{"valid", "detector-fast", "detector.v2", 99, 100 * time.Millisecond, true},
		{"upper percentile", "detector-fast", "detector", 100, time.Nanosecond, true},
		{"fractional percentile", "detector-fast", "detector", 0.01, time.Second, true},
		{"empty object", "", "detector", 99, time.Second, false},
		{"invalid object", "Detector", "detector", 99, time.Second, false},
		{"long object", strings.Repeat("a", 254), "detector", 99, time.Second, false},
		{"empty model", "detector-fast", "", 99, time.Second, false},
		{"invalid model", "detector-fast", "detector/name", 99, time.Second, false},
		{"long model", "detector-fast", strings.Repeat("a", 254), 99, time.Second, false},
		{"zero percentile", "detector-fast", "detector", 0, time.Second, false},
		{"negative percentile", "detector-fast", "detector", -1, time.Second, false},
		{"excess percentile", "detector-fast", "detector", 101, time.Second, false},
		{"NaN", "detector-fast", "detector", math.NaN(), time.Second, false},
		{"positive infinity", "detector-fast", "detector", math.Inf(1), time.Second, false},
		{"negative infinity", "detector-fast", "detector", math.Inf(-1), time.Second, false},
		{"zero duration", "detector-fast", "detector", 99, 0, false},
		{"negative duration", "detector-fast", "detector", 99, -time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := valid
			spec.ModelRef.Name = tc.modelName
			spec.SLO.Percentile = tc.percentile
			spec.SLO.TargetLatency.Duration = tc.latency
			if err := edgev1alpha1.ValidateWorkload(tc.objectName, spec); (err == nil) != tc.valid {
				t.Fatalf("ValidateWorkload() = %v, valid = %v", err, tc.valid)
			}
		})
	}
}

func TestWorkloadDurationDecodingRejectsOverflowAndMissingUnits(t *testing.T) {
	for _, duration := range []string{"9223372036854775808ns", "100", "invalid"} {
		var spec edgev1alpha1.WorkloadSpec
		if err := json.Unmarshal([]byte(`{"modelRef":{"name":"detector"},"slo":{"percentile":99,"targetLatency":"`+duration+`"}}`), &spec); err == nil {
			t.Fatalf("accepted invalid duration %q", duration)
		}
	}
	var spec edgev1alpha1.WorkloadSpec
	if err := json.Unmarshal([]byte(`{"modelRef":{"name":"detector"},"slo":{"percentile":99,"targetLatency":"100ms"}}`), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.SLO.TargetLatency.Duration != 100*time.Millisecond {
		t.Fatalf("duration = %v", spec.SLO.TargetLatency.Duration)
	}
}
