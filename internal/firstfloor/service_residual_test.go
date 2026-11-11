package firstfloor

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestResidualRetriesNearestAmongSurvivors(t *testing.T) {
	batch := pairedBatch(t, 1, 1,
		pairedSample("a", 0, 5),
		pairedSample("a", 20, 80),
		pairedSample("a", 50, 200),
		pairedSample("dead", 0, 5),
		ServiceSample{TrialID: "startup", ServiceDurationNanos: int64(300 * time.Millisecond)},
	)
	rng := rand.New(rand.NewPCG(19, 40))
	gap := int64(0)
	for range 100 {
		sample, support, ok := batch.Distribution.SampleResidual(&gap, int64(10*time.Millisecond), rng)
		if !ok || sample.TrialID != "a" || sample.ServiceDurationNanos != int64(80*time.Millisecond) {
			t.Fatalf("nearest short group must yield to nearest surviving group: %+v %v", sample, ok)
		}
		want := ConditionalSupport{DistanceNanos: int64(20 * time.Millisecond), TrialCount: 1, OutsideSupport: true}
		if support != want {
			t.Fatalf("retry support: got %+v, want %+v", support, want)
		}
	}
	if sample, support, ok := batch.Distribution.SampleResidual(&gap, int64(400*time.Millisecond), rng); ok || support != (ConditionalSupport{}) || sample.ServiceDurationNanos != 0 {
		t.Fatalf("exhausted records must leave replay invalid: %+v %+v %v", sample, support, ok)
	}
}

func TestResidualRetryPreservesTrialAndTiedRecordWeights(t *testing.T) {
	batch := pairedBatch(t, 1, 1,
		pairedSample("a", 10, 5),
		pairedSample("a", 0, 70), pairedSample("a", 0, 70), pairedSample("a", 0, 70),
		pairedSample("a", 20, 90), pairedSample("a", 30, 200),
		pairedSample("b", 10, 5), pairedSample("b", 20, 110),
		pairedSample("dead", 10, 5),
	)
	rng := rand.New(rand.NewPCG(19, 40))
	gap := int64(10 * time.Millisecond)
	counts := map[int64]int{}
	want := ConditionalSupport{DistanceNanos: int64(10 * time.Millisecond), TrialCount: 2, OutsideSupport: true}
	for range 8000 {
		sample, support, ok := batch.Distribution.SampleResidual(&gap, int64(10*time.Millisecond), rng)
		if !ok || support != want {
			t.Fatalf("surviving trial support: %+v %v", support, ok)
		}
		switch service := sample.ServiceDurationNanos / int64(time.Millisecond); service {
		case 70, 90, 110:
			counts[service]++
		default:
			t.Fatalf("retry selected a non-nearest survivor: %+v", sample)
		}
	}
	if counts[70] < 2700 || counts[70] > 3300 || counts[90] < 800 || counts[90] > 1200 || counts[110] < 3700 || counts[110] > 4300 {
		t.Fatalf("retry must use uniform surviving trials then uniform tied records: %v", counts)
	}
}

func TestResidualKeepsOriginalNearestWhenAnyTrialSurvives(t *testing.T) {
	batch := pairedBatch(t, 1, 1,
		pairedSample("a", 0, 5), pairedSample("a", 0, 80), pairedSample("a", 20, 200),
		pairedSample("b", 0, 5), pairedSample("b", 20, 300),
	)
	rng := rand.New(rand.NewPCG(19, 40))
	gap := int64(0)
	for range 100 {
		sample, support, ok := batch.Distribution.SampleResidual(&gap, int64(50*time.Millisecond), rng)
		if !ok || sample.TrialID != "a" || sample.ServiceDurationNanos != int64(80*time.Millisecond) || support != (ConditionalSupport{TrialCount: 2}) {
			t.Fatalf("existing nearest survivors must not widen any trial: %+v %+v %v", sample, support, ok)
		}
	}
}

func TestResidualQuantileUsesSurvivalConditioning(t *testing.T) {
	batch := pairedBatch(t, 1, 1,
		pairedSample("a", 0, 5), pairedSample("a", 0, 80),
		pairedSample("b", 0, 5), pairedSample("b", 0, 100), pairedSample("b", 0, 100), pairedSample("b", 0, 100),
	)
	gap := int64(0)
	elapsed := int64(50 * time.Millisecond)
	for _, tc := range []struct {
		percentile float64
		want       time.Duration
	}{{0, 30 * time.Millisecond}, {39, 30 * time.Millisecond}, {41, 50 * time.Millisecond}, {100, 50 * time.Millisecond}} {
		got, support, ok := batch.Distribution.ResidualQuantileNanos(&gap, elapsed, tc.percentile)
		if !ok || got != int64(tc.want) || support.TrialCount != 2 {
			t.Fatalf("p%v survival quantile = %v %+v %v, want %v", tc.percentile, time.Duration(got), support, ok, tc.want)
		}
	}
	retry := pairedBatch(t, 1, 1,
		pairedSample("a", 0, 5), pairedSample("a", 20, 80),
		pairedSample("b", 0, 5), pairedSample("b", 20, 110),
	)
	got, _, ok := retry.Distribution.ResidualQuantileNanos(&gap, elapsed, 100)
	if !ok || got != int64(60*time.Millisecond) {
		t.Fatalf("nearest surviving retry = %v %v", got, ok)
	}
	if _, _, ok := retry.Distribution.ResidualQuantileNanos(&gap, int64(110*time.Millisecond), 99); ok {
		t.Fatal("elapsed beyond all empirical service support acquired a residual")
	}
}
