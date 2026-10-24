package powerwindow

import (
	"math"
	"testing"
	"time"
)

func TestMeasureConstantPowerAndZeroHaveNoPhaseSensitivity(t *testing.T) {
	start := time.Unix(100, 0).UTC()
	for _, watts := range []float64{0, 20} {
		measurement, err := Measure(Window{Start: start, End: start.Add(2 * time.Second), Samples: []Sample{
			{ObservedAt: start.Add(-time.Second), Watts: watts},
			{ObservedAt: start.Add(3 * time.Second), Watts: watts},
		}}, time.Second, nil)
		if err != nil || measurement.Joules != 2*watts || measurement.SensitivityJoules != 0 || measurement.UncertaintyJoules != 0 {
			t.Fatalf("constant %v W: %+v, %v", watts, measurement, err)
		}
	}
}

func TestMeasureNominalPlateauIsNotLostAgainstLargeOuterPower(t *testing.T) {
	start := time.Unix(100, 0).UTC()
	for _, watts := range []float64{0, 0.001} {
		measurement, err := Measure(Window{Start: start, End: start.Add(time.Second), Samples: []Sample{
			{ObservedAt: start.Add(-time.Second), Watts: 1e20},
			{ObservedAt: start, Watts: watts},
			{ObservedAt: start.Add(time.Second), Watts: watts},
			{ObservedAt: start.Add(2 * time.Second), Watts: 1e20},
		}}, time.Second, nil)
		if err != nil || measurement.Joules != watts {
			t.Fatalf("nominal plateau %v W: %+v, %v", watts, measurement, err)
		}
	}
	window := Window{Start: start, End: start.Add(time.Second)}
	for i, watts := range []float64{1e20, 1, 1, 2, 1, 2} {
		window.Samples = append(window.Samples, Sample{ObservedAt: start.Add(time.Duration(i-3) * time.Second), Watts: watts})
	}
	measurement, err := Measure(window, time.Second, nil)
	if err != nil || measurement.Joules != 1.5 || measurement.SensitivityJoules != 0.25 || measurement.UncertaintyJoules != 0.25 {
		t.Fatalf("unrelated outer power erased supported variations: %+v, %v", measurement, err)
	}
	measurement, err = Measure(window, time.Second, []Gap{{Start: start.Add(-2 * time.Second), End: start}})
	if err != nil || measurement.MissingGapJoules != 1e20 || measurement.SensitivityJoules != 0.25 {
		t.Fatalf("support clipping changed original record maximum/gaps: %+v, %v", measurement, err)
	}
	window.Samples[0].Watts = math.NaN()
	if _, err := Measure(window, time.Second, nil); err == nil {
		t.Fatal("support clipping hid an invalid original outer sample")
	}
}

func TestMeasureFindsInteriorCriticalPhaseAndIsOffsetInvariant(t *testing.T) {
	start := time.Unix(100, 0).UTC()
	// Both extreme phases have exactly the nominal energy. The true extrema
	// occur strictly inside knot intervals at delta=-0.5 and +0.5 seconds.
	for _, offset := range []float64{0, 1000} {
		window := Window{Start: start, End: start.Add(time.Second)}
		for i, watts := range []float64{0, 2, 0, 2} {
			window.Samples = append(window.Samples, Sample{ObservedAt: start.Add(time.Duration(i-1) * time.Second), Watts: watts + offset})
		}
		measurement, err := Measure(window, time.Second, nil)
		if err != nil || measurement.Joules != 1+offset || measurement.SensitivityJoules != 0.5 || measurement.UncertaintyJoules != 0.5 {
			t.Fatalf("offset %v: %+v, %v", offset, measurement, err)
		}
	}
}

func TestMeasureCriticalPhaseDoesNotOverflowDerivativeDifference(t *testing.T) {
	start := time.Unix(100, 0).UTC()
	window := Window{Start: start, End: start.Add(time.Second)}
	for i, watts := range []float64{0, 1e308, 0, 1e308} {
		window.Samples = append(window.Samples, Sample{ObservedAt: start.Add(time.Duration(i-1) * time.Second), Watts: watts})
	}
	measurement, err := Measure(window, time.Second, nil)
	if err != nil || measurement.Joules != 5e307 || math.Abs(measurement.SensitivityJoules/2.5e307-1) > 1e-14 {
		t.Fatalf("finite energy with opposite large derivatives: %+v, %v", measurement, err)
	}
}

func TestMeasureMissingGapUnionUsesOriginalWidthsAndPhysicalCap(t *testing.T) {
	start := time.Unix(100, 0).UTC()
	at := func(seconds int) time.Time { return start.Add(time.Duration(seconds) * time.Second) }
	window := Window{Start: start, End: at(10), Samples: []Sample{{ObservedAt: at(-6), Watts: 3}, {ObservedAt: at(14), Watts: 3}}}
	gaps := []Gap{{Start: at(-5), End: at(0)}, {Start: at(-2), End: at(2)}, {Start: at(-5), End: at(0)}, {Start: at(12), End: at(14)}}
	measurement, err := Measure(window, time.Second, gaps)
	if err != nil || measurement.SensitivityJoules != 0 || measurement.MissingGapJoules != 21 || measurement.UncertaintyJoules != 21 {
		t.Fatalf("original union [-5,2], outside gap ignored: %+v, %v", measurement, err)
	}
	gaps = append(gaps, Gap{Start: at(8), End: at(13)})
	measurement, err = Measure(window, time.Second, gaps)
	if err != nil || measurement.MissingGapJoules != 30 {
		t.Fatalf("union capped at physical ten seconds: %+v, %v", measurement, err)
	}
}

func TestMeasureRequiresRealExpandedBracketsAndOriginalValidity(t *testing.T) {
	start := time.Unix(100, 0).UTC()
	for _, test := range []struct {
		name    string
		samples []Sample
	}{
		{"missing support", []Sample{{ObservedAt: start, Watts: 1}, {ObservedAt: start.Add(time.Second), Watts: 1}}},
		{"duplicate", []Sample{{ObservedAt: start.Add(-time.Second), Watts: 1}, {ObservedAt: start.Add(-time.Second), Watts: 2}, {ObservedAt: start.Add(2 * time.Second), Watts: 1}}},
		{"original order", []Sample{{ObservedAt: start.Add(-time.Second), Watts: 1}, {ObservedAt: start.Add(time.Second), Watts: 1}, {ObservedAt: start, Watts: 1}, {ObservedAt: start.Add(2 * time.Second), Watts: 1}}},
		{"invalid outside action", []Sample{{ObservedAt: start.Add(-time.Second), Watts: math.NaN()}, {ObservedAt: start.Add(2 * time.Second), Watts: 1}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Measure(Window{Start: start, End: start.Add(time.Second), Samples: test.samples}, time.Second, nil); err == nil {
				t.Fatal("invalid original support was accepted")
			}
		})
	}
}

func TestRelativeUncertaintyReportsSignedAndZeroEnergySafely(t *testing.T) {
	for _, joules := range []float64{-10, 10} {
		if ratio := RelativeUncertainty(joules, 2); ratio == nil || *ratio != 0.2 {
			t.Fatalf("ratio for %v J: %v", joules, ratio)
		}
	}
	if RelativeUncertainty(0, 0) != nil || RelativeUncertainty(0, 2) != nil || RelativeUncertainty(math.SmallestNonzeroFloat64, math.MaxFloat64) != nil {
		t.Fatal("undefined ratio should not become an infinite JSON number or reject energy")
	}
}
