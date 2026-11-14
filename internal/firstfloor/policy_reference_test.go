package firstfloor

import (
	"math/rand/v2"
	"testing"
	"time"
)

func pairedBatch(t *testing.T, size int, energy float64, samples ...ServiceSample) BatchProfile {
	t.Helper()
	d, err := NewServiceDistribution(samples)
	if err != nil {
		t.Fatal(err)
	}
	return BatchProfile{Size: size, EnergyPerItemJ: energy, ServiceSamples: samples, Distribution: d}
}
func pairedSample(trial string, gapMS, serviceMS int64) ServiceSample {
	gap := gapMS * int64(time.Millisecond)
	return ServiceSample{TrialID: trial, PrecedingIdleNanos: &gap, ServiceDurationNanos: serviceMS * int64(time.Millisecond)}
}

func TestConditionalNativeQuantilePoolsTrialsAndUsesTheSampleFloor(t *testing.T) {
	batch := pairedBatch(t, 1, 1,
		pairedSample("a", 0, 2), pairedSample("a", 0, 2), pairedSample("a", 0, 2), pairedSample("a", 20, 200),
		pairedSample("b", 10, 80),
		ServiceSample{TrialID: "a", ServiceDurationNanos: int64(120 * time.Millisecond)},
	)
	p := testPolicy()
	p.DeadlinePolicy = DeadlineDrop
	p.Batches = []BatchProfile{batch}
	q := testState("w", 50, 0)
	q.Context.SLO.Percentile = 50
	for _, tc := range []struct {
		gap  *int64
		want string
	}{
		{nil, ReasonMissingConditionalSupport},
		{new(int64), ReasonAvailableEnergy},
		{func() *int64 { g := int64(10 * time.Millisecond); return &g }(), OutcomeDeadlineDrop},
	} {
		choice := ChooseBatch(p, []RequestState{q}, tc.gap)
		if choice.Reason != tc.want {
			t.Fatalf("conditional deadline decision: got %+v, want %s", choice, tc.want)
		}
	}
	gap := int64(10 * time.Millisecond)
	// Floors: one record at p50, four at p70: on the log scale the window of
	// four at 10 ms is gaps 0, 0, 10 and 20 ms. Five records leave p87.5
	// (floor 16) unidentified.
	for _, tc := range []struct {
		percentile float64
		want       int64
	}{{50, 80}, {70, 80}, {87.5, 0}, {100, 0}} {
		got, _, ok := batch.Distribution.QuantileNanos(&gap, tc.percentile)
		if ok != (tc.want > 0) || got != tc.want*int64(time.Millisecond) {
			t.Fatalf("p%g got %d %v, want %dms", tc.percentile, got, ok, tc.want)
		}
	}
	rng := rand.New(rand.NewPCG(19, 40))
	short := 0
	for range 8000 {
		service, _, ok := batch.Distribution.Sample(&gap, 70, rng)
		if !ok {
			t.Fatal("known-gap support disappeared")
		}
		switch service / int64(time.Millisecond) {
		case 2:
			short++
		case 80, 200:
		default:
			t.Fatalf("sampling left the selection: %d", service)
		}
	}
	if short < 3700 || short > 4300 {
		t.Fatalf("draws must be uniform over the four window records: short=%d", short)
	}
	long := 0
	for range 4000 {
		service, support, ok := batch.Distribution.SampleResidual(&gap, int64(50*time.Millisecond), 70, rng)
		if !ok || support.Records != 4 {
			t.Fatalf("surviving conditional support: %+v %v", support, ok)
		}
		if service == int64(200*time.Millisecond) {
			long++
		} else if service != int64(80*time.Millisecond) {
			t.Fatalf("invalid surviving service: %d", service)
		}
	}
	if long < 1800 || long > 2200 {
		t.Fatalf("residual draw must be uniform over both survivors: long=%d", long)
	}
}

func TestEnergyFillUsesFutureIdleAndResetsCompetingServiceAnchor(t *testing.T) {
	p := testPolicy()
	p.Batches = []BatchProfile{
		pairedBatch(t, 1, 2, pairedSample("a", 0, 1), pairedSample("a", 10, 100)),
		pairedBatch(t, 2, 1, pairedSample("a", 0, 2), pairedSample("a", 10, 60)),
	}
	gap := int64(0)
	// Two records per batch are below the p95 floor and would pool both gaps;
	// the 50th percentile's single-record floor keeps the answer gap-local.
	q := testState("a", 50, 100)
	q.Context.SLO.Percentile = 50
	if choice := ChooseBatch(p, []RequestState{q}, &gap); choice.WaitMS != 0 || choice.Size != 1 {
		t.Fatalf("long-idle service after planned fill cannot borrow short-gap slack: %+v", choice)
	}
	// Here the waiting batch takes 2ms at future age20ms. The competing batch
	// begins after that service at age0 (1ms), not at the original idle age10ms.
	p.Batches[1] = pairedBatch(t, 2, 1, pairedSample("a", 0, 2), pairedSample("a", 20, 2))
	p.Batches[0] = pairedBatch(t, 1, 2, pairedSample("a", 0, 1), pairedSample("a", 10, 20))
	gap = int64(10 * time.Millisecond)
	q = testState("a", 200, 100)
	other := testState("b", 25, 0)
	q.Context.SLO.Percentile, other.Context.SLO.Percentile = 50, 50
	q.Context.SenderElapsedMS = 176 // make a the EDF head with 24ms remaining
	if choice := ChooseBatch(p, []RequestState{q, other}, &gap); choice.WaitMS != 10 || choice.Reason != "measured_energy_gain_fill" {
		t.Fatalf("competing queue must use post-service zero-age approximation: %+v", choice)
	}
}
