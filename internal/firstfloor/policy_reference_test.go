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

func TestConditionalNativeQuantilePreservesTrialWeightsAndTiedRecords(t *testing.T) {
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
		{nil, OutcomeDeadlineDrop},
		{new(int64), "available_energy"},
		{func() *int64 { g := int64(10 * time.Millisecond); return &g }(), OutcomeDeadlineDrop},
	} {
		choice := ChooseBatch(p, []RequestState{q}, tc.gap)
		if choice.Reason != tc.want {
			t.Fatalf("conditional deadline decision: got %+v, want %s", choice, tc.want)
		}
	}
	gap := int64(10 * time.Millisecond)
	for _, tc := range []struct {
		percentile float64
		want       int64
	}{{0, 2}, {37.5, 2}, {50, 80}, {87.5, 80}, {95, 200}, {100, 200}} {
		got, _, ok := batch.Distribution.QuantileNanos(&gap, tc.percentile)
		if !ok || got != tc.want*int64(time.Millisecond) {
			t.Fatalf("p%g got %d, want %dms", tc.percentile, got, tc.want)
		}
	}
	rng := rand.New(rand.NewPCG(19, 40))
	long, medium, short := 0, 0, 0
	for range 8000 {
		s, _, ok := batch.Distribution.Sample(&gap, rng)
		if !ok {
			t.Fatal("known-gap support disappeared")
		}
		switch s.ServiceDurationNanos / int64(time.Millisecond) {
		case 2:
			short++
		case 80:
			medium++
		case 200:
			long++
		default:
			t.Fatalf("sampling invented a service duration: %+v", s)
		}
	}
	if short < 2700 || short > 3300 || medium < 3700 || medium > 4300 || long < 800 || long > 1200 {
		t.Fatalf("uniform physical trials then all nearest actual records: short=%d medium=%d long=%d", short, medium, long)
	}
	long = 0
	for range 4000 {
		s, _, ok := batch.Distribution.SampleResidual(&gap, int64(50*time.Millisecond), rng)
		if !ok {
			t.Fatal("surviving conditional support disappeared")
		}
		if s.ServiceDurationNanos == int64(200*time.Millisecond) {
			long++
		} else if s.ServiceDurationNanos != int64(80*time.Millisecond) {
			t.Fatalf("invalid surviving service: %+v", s)
		}
	}
	if long < 650 || long > 950 {
		t.Fatalf("residual draw must retain posterior trial weights: long=%d", long)
	}
}

func TestEnergyFillUsesFutureIdleAndResetsCompetingServiceAnchor(t *testing.T) {
	p := testPolicy()
	p.Batches = []BatchProfile{
		pairedBatch(t, 1, 2, pairedSample("a", 0, 1), pairedSample("a", 10, 100)),
		pairedBatch(t, 2, 1, pairedSample("a", 0, 2), pairedSample("a", 10, 60)),
	}
	gap := int64(0)
	q := testState("a", 50, 100)
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
	q.Context.SenderElapsedMS = 176 // make a the EDF head with 24ms remaining
	if choice := ChooseBatch(p, []RequestState{q, other}, &gap); choice.WaitMS != 10 || choice.Reason != "measured_energy_gain_fill" {
		t.Fatalf("competing queue must use post-service zero-age approximation: %+v", choice)
	}
}
