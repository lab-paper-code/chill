package powerwindow

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestIntegrateConstantTraceSortsCopyAndUsesExactWindow(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	samples := []Sample{
		{ObservedAt: start.Add(9 * time.Second), Watts: 10},
		{ObservedAt: start.Add(-time.Second), Watts: 1000},
		{ObservedAt: start.Add(5 * time.Second), Watts: 10},
		{ObservedAt: start.Add(11 * time.Second), Watts: 1000},
		{ObservedAt: start.Add(time.Second), Watts: 10},
	}
	originalFirst := samples[0]

	joules, err := Integrate(
		Window{Start: start, End: start.Add(10 * time.Second), Samples: samples},
		Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 0.8, MaximumGap: 5 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(joules-100) > 1e-9 {
		t.Fatalf("joules = %v, want 100", joules)
	}
	if samples[0] != originalFirst {
		t.Fatalf("Integrate reordered caller samples: %#v", samples)
	}
}

func TestIntegrateHoldsBoundaryValuesForTrapezoids(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	joules, err := Integrate(
		Window{
			Start: start,
			End:   start.Add(10 * time.Second),
			Samples: []Sample{
				{ObservedAt: start.Add(time.Second), Watts: 10},
				{ObservedAt: start.Add(5 * time.Second), Watts: 20},
				{ObservedAt: start.Add(9 * time.Second), Watts: 30},
			},
		},
		Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 0.8, MaximumGap: 4 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(joules-200) > 1e-9 {
		t.Fatalf("joules = %v, want 200", joules)
	}
}

func TestIntegrateRejectsInvalidWatts(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	for _, test := range []struct {
		name  string
		watts float64
	}{
		{name: "negative", watts: -1},
		{name: "nan", watts: math.NaN()},
		{name: "positive infinity", watts: math.Inf(1)},
		{name: "negative infinity", watts: math.Inf(-1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Integrate(
				Window{
					Start: start,
					End:   start.Add(10 * time.Second),
					Samples: []Sample{
						{ObservedAt: start.Add(-time.Second), Watts: test.watts},
						{ObservedAt: start.Add(time.Second), Watts: 10},
						{ObservedAt: start.Add(9 * time.Second), Watts: 10},
					},
				},
				Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 0.8, MaximumGap: 8 * time.Second},
			)
			assertErrorContains(t, err, "invalid power sample")
		})
	}
}

func TestIntegrateAcceptsFiniteIntegralWithoutIntermediateAverageOverflow(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	joules, err := Integrate(
		Window{
			Start: start,
			End:   start.Add(100 * time.Millisecond),
			Samples: []Sample{
				{ObservedAt: start.Add(5 * time.Millisecond), Watts: math.MaxFloat64},
				{ObservedAt: start.Add(95 * time.Millisecond), Watts: math.MaxFloat64},
			},
		},
		Policy{MaximumBoundaryDistance: 5 * time.Millisecond, MinimumCoverage: 0.8, MaximumGap: 90 * time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	if math.IsNaN(joules) || math.IsInf(joules, 0) {
		t.Fatalf("joules = %v, want finite", joules)
	}
}

func TestIntegrateRejectsNonFiniteAccumulatedIntegralFromFiniteSamples(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	_, err := Integrate(
		Window{
			Start: start,
			End:   start.Add(2 * time.Second),
			Samples: []Sample{
				{ObservedAt: start.Add(100 * time.Millisecond), Watts: math.MaxFloat64},
				{ObservedAt: start.Add(time.Second), Watts: math.MaxFloat64},
				{ObservedAt: start.Add(1900 * time.Millisecond), Watts: math.MaxFloat64},
			},
		},
		Policy{MaximumBoundaryDistance: 100 * time.Millisecond, MinimumCoverage: 0.9, MaximumGap: time.Second},
	)
	assertErrorContains(t, err, "accumulated power integral is non-finite")
}

func TestIntegrateRejectsInsufficientWindowQuality(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	for _, test := range []struct {
		name    string
		samples []Sample
		policy  Policy
		want    string
	}{
		{
			name:    "fewer than two samples",
			samples: []Sample{{ObservedAt: start.Add(time.Second), Watts: 10}},
			policy:  Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 0.8, MaximumGap: 10 * time.Second},
			want:    "fewer than two samples overlap the runtime window",
		},
		{
			name: "boundary distance",
			samples: []Sample{
				{ObservedAt: start.Add(2 * time.Second), Watts: 10},
				{ObservedAt: start.Add(8 * time.Second), Watts: 10},
			},
			policy: Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 0.6, MaximumGap: 10 * time.Second},
			want:   "sample boundary distance exceeds policy",
		},
		{
			name: "coverage",
			samples: []Sample{
				{ObservedAt: start.Add(time.Second), Watts: 10},
				{ObservedAt: start.Add(8 * time.Second), Watts: 10},
			},
			policy: Policy{MaximumBoundaryDistance: 2 * time.Second, MinimumCoverage: 0.8, MaximumGap: 10 * time.Second},
			want:   "sample coverage is below policy",
		},
		{
			name: "maximum gap",
			samples: []Sample{
				{ObservedAt: start.Add(time.Second), Watts: 10},
				{ObservedAt: start.Add(5 * time.Second), Watts: 10},
				{ObservedAt: start.Add(9 * time.Second), Watts: 10},
			},
			policy: Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 0.8, MaximumGap: 3 * time.Second},
			want:   "maximum sample gap exceeds policy",
		},
		{
			name: "duplicate timestamp",
			samples: []Sample{
				{ObservedAt: start.Add(time.Second), Watts: 10},
				{ObservedAt: start.Add(time.Second), Watts: 20},
				{ObservedAt: start.Add(9 * time.Second), Watts: 10},
			},
			policy: Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 0.8, MaximumGap: 10 * time.Second},
			want:   "duplicate or non-monotonic sample timestamp",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Integrate(
				Window{Start: start, End: start.Add(10 * time.Second), Samples: test.samples},
				test.policy,
			)
			assertErrorContains(t, err, test.want)
		})
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want containing %q", err, want)
	}
}
