package firstfloor

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"gonum.org/v1/gonum/stat/distuv"
)

type gapRegime struct{ gapLow, gapHigh, serviceLow, serviceHigh time.Duration }

// Gaps in [0.1 ms, 1 ms) serve in U[10 ms, 20 ms), gaps in [100 ms, 1 s) in U[40 ms, 80 ms).
var gapRegimes = []gapRegime{
	{100 * time.Microsecond, time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond},
	{100 * time.Millisecond, time.Second, 40 * time.Millisecond, 80 * time.Millisecond},
}

// regimeSamples draws count records per regime over three physical trials.
func regimeSamples(count int, rng *rand.Rand) []ServiceSample {
	samples := make([]ServiceSample, 0, 2*count)
	for _, r := range gapRegimes {
		for i := range count {
			gap := int64(r.gapLow) + rng.Int64N(int64(r.gapHigh-r.gapLow))
			service := int64(r.serviceLow) + rng.Int64N(int64(r.serviceHigh-r.serviceLow))
			samples = append(samples, ServiceSample{TrialID: fmt.Sprint("trial-", i%3), PrecedingIdleNanos: &gap, ServiceDurationNanos: service})
		}
	}
	return samples
}

// Inside each regime the 59 nearest records are uniform draws, so the p95 is
// their 57th smallest: a Beta(57, 3) fraction of the regime's service range.
// The estimate must stay inside that band at every record count.
func TestQuantileReturnsEachGapRegimeQuantileAtEveryRecordCount(t *testing.T) {
	band := distuv.Beta{Alpha: 57, Beta: 3}
	low, high := band.Quantile(1e-4), band.Quantile(1-1e-4)
	rng := rand.New(rand.NewPCG(7, 11))
	for _, count := range []int{1_000, 10_000, 100_000} {
		d, err := NewServiceDistribution(regimeSamples(count, rng))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range gapRegimes {
			for step := range 20 {
				gap := int64(r.gapLow) + int64(r.gapHigh-r.gapLow)*int64(step)/20
				got, support, ok := d.QuantileNanos(&gap, 95)
				fraction := float64(got-int64(r.serviceLow)) / float64(r.serviceHigh-r.serviceLow)
				if !ok || support.Records != 59 || fraction < low || fraction > high {
					t.Fatalf("records=%d gap=%v: p95=%v (%+v) is fraction %.3f of the regime's service range, want [%.3f, %.3f]",
						count, time.Duration(gap), time.Duration(got), support, fraction, low, high)
				}
			}
		}
	}
}

func TestMinimumServiceSamplesIsThePercentileSampleFloor(t *testing.T) {
	for _, test := range []struct {
		percentile float64
		want       int
	}{{95, 59}, {99, 459}, {90, 22}, {50, 1}} {
		if got, err := MinimumServiceSamples(test.percentile); err != nil || got != test.want {
			t.Fatalf("percentile %g: count=%d err=%v, want %d", test.percentile, got, err, test.want)
		}
	}
	for _, percentile := range []float64{0, -1, 100, 101, math.NaN(), math.Inf(1)} {
		if _, err := MinimumServiceSamples(percentile); err == nil {
			t.Fatalf("accepted percentile %g", percentile)
		}
	}
}

func TestQuantileBelowTheFloorIsNotIdentifiedAndFirstExecutionsAreDropped(t *testing.T) {
	var samples []ServiceSample
	for i := range 10 {
		samples = append(samples, pairedSample("a", int64(i), int64(10+i)))
	}
	samples = append(samples, startupSample("a", 500), startupSample("b", 400))
	d, err := NewServiceDistribution(samples)
	if err != nil {
		t.Fatal(err)
	}
	gap := int64(3 * time.Millisecond)
	if got, support, ok := d.QuantileNanos(&gap, 95); ok || support != (ConditionalSupport{Records: 10}) {
		t.Fatalf("10 of 59 records answered: %v %+v %v", time.Duration(got), support, ok)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	if _, support, ok := d.Sample(&gap, 95, rng); ok || support.Records != 10 {
		t.Fatal("a draw padded an unidentified percentile")
	}
	// Ten records identify the 75th percentile, whose floor is five.
	if _, support, ok := d.QuantileNanos(&gap, 75); !ok || support.Records != 5 {
		t.Fatalf("identified percentile unknown: %+v", support)
	}
	if d.DroppedFirstExecutions() != 2 {
		t.Fatalf("dropped first executions: %d", d.DroppedFirstExecutions())
	}
	if _, _, ok := d.QuantileNanos(nil, 50); ok || d.SupportsGap(nil) {
		t.Fatal("a query without an execution anchor answered")
	}
}

// On the logarithmic scale 0 and 8 ns are equally far from 2 ns:
// (1+2)^2 = (1+0)(1+8). The smaller gap wins the tie.
func TestQuantileLogDistanceTieTakesTheSmallerGap(t *testing.T) {
	zero, eight := int64(0), int64(8)
	d, err := NewServiceDistribution([]ServiceSample{
		{TrialID: "a", PrecedingIdleNanos: &zero, ServiceDurationNanos: 1},
		{TrialID: "b", PrecedingIdleNanos: &eight, ServiceDurationNanos: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	for gap, want := range map[int64]int64{0: 1, 1: 1, 2: 1, 3: 2, 1_000_000: 2} {
		got, support, ok := d.QuantileNanos(&gap, 50)
		if !ok || got != want || support.Records != 1 {
			t.Fatalf("gap %dns: got %d %+v %v, want %d", gap, got, support, ok, want)
		}
	}
}

func TestQuantileEdgesStayExact(t *testing.T) {
	left, right := int64(math.MaxInt64-2), int64(math.MaxInt64)
	d, err := NewServiceDistribution([]ServiceSample{
		{TrialID: "a", PrecedingIdleNanos: &left, ServiceDurationNanos: 2},
		{TrialID: "a", PrecedingIdleNanos: &right, ServiceDurationNanos: 200},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The geometric midpoint lies below the arithmetic one: left+1 is nearer right.
	gap := left + 1
	if got, support, ok := d.QuantileNanos(&gap, 50); !ok || got != 200 || support != (ConditionalSupport{Records: 1, DistanceNanos: 1}) {
		t.Fatalf("midpoint at the int64 limit: %d %+v %v", got, support, ok)
	}
	if got, support, ok := d.MaximumQuantileNanos(left, right, 50); !ok || got != 200 || support != (ConditionalSupport{Records: 1, DistanceNanos: 2}) {
		t.Fatalf("interval to the int64 limit: %d %+v %v", got, support, ok)
	}
	for _, percentile := range []float64{math.NaN(), -1, 0, 100, 101} {
		if _, _, ok := d.QuantileNanos(&gap, percentile); ok {
			t.Fatalf("percentile %g answered", percentile)
		}
	}
	var absent *ServiceDistribution
	for _, d := range []*ServiceDistribution{absent, {}} {
		if _, _, ok := d.QuantileNanos(&gap, 50); ok {
			t.Fatal("empty distribution answered")
		}
	}
}

// The residual conditions the window on S > elapsed; it never widens it.
func TestResidualConditionsTheWindow(t *testing.T) {
	// The 70th percentile's floor is four records.
	batch := pairedBatch(t, 1, 1, pairedSample("a", 1, 5), pairedSample("a", 2, 80), pairedSample("b", 3, 200), pairedSample("b", 4, 300), pairedSample("a", 1000, 400))
	gap := int64(2 * time.Millisecond)
	elapsed := int64(10 * time.Millisecond)
	got, support, ok := batch.Distribution.ResidualQuantileNanos(&gap, elapsed, 70)
	if want := (ConditionalSupport{Records: 4, DistanceNanos: int64(2 * time.Millisecond)}); !ok || got != int64(290*time.Millisecond) || support != want {
		t.Fatalf("residual: %v %+v %v", time.Duration(got), support, ok)
	}
	rng := rand.New(rand.NewPCG(19, 40))
	counts := map[int64]int{}
	for range 3000 {
		service, _, ok := batch.Distribution.SampleResidual(&gap, elapsed, 70, rng)
		if !ok {
			t.Fatal("residual draw unknown")
		}
		counts[service/int64(time.Millisecond)]++
	}
	for _, service := range []int64{80, 200, 300} {
		if counts[service] < 850 || counts[service] > 1150 {
			t.Fatalf("residual draws are not uniform over the window's survivors: %v", counts)
		}
	}
	if _, _, ok := batch.Distribution.ResidualQuantileNanos(&gap, int64(350*time.Millisecond), 70); ok {
		t.Fatal("a window without survivors borrowed a record outside it")
	}
}

// Sample draws every window record with equal probability and nothing else.
func TestSampleDrawsUniformlyFromTheWindow(t *testing.T) {
	var samples []ServiceSample
	for i := range 8 {
		samples = append(samples, pairedSample("a", int64(10*i), int64(1+i)))
	}
	batch := pairedBatch(t, 1, 1, samples...)
	gap := int64(5 * time.Millisecond)
	rng := rand.New(rand.NewPCG(19, 40))
	counts := map[int64]int{}
	for range 8000 {
		// Four records at 70 %: on the log scale 0 ms is the farthest gap from
		// 5 ms, so the window is 10, 20, 30 and 40 ms.
		service, support, ok := batch.Distribution.Sample(&gap, 70, rng)
		if !ok || support.Records != 4 {
			t.Fatalf("sample support: %+v %v", support, ok)
		}
		counts[service/int64(time.Millisecond)]++
	}
	for service := int64(2); service <= 5; service++ {
		if counts[service] < 1800 || counts[service] > 2200 {
			t.Fatalf("draws are not uniform over the window: %v", counts)
		}
	}
	if len(counts) != 4 {
		t.Fatalf("draw left the window: %v", counts)
	}
}

func TestConditionalQueriesDoNotAllocate(t *testing.T) {
	d := fastModelProfile(t)
	rng := rand.New(rand.NewPCG(1, 2))
	gap, elapsed := int64(5*time.Millisecond), int64(12*time.Millisecond)
	for _, percentile := range []float64{95, 99} {
		for name, query := range map[string]func(){
			"quantile": func() { d.QuantileNanos(&gap, percentile) },
			"maximum":  func() { d.MaximumQuantileNanos(gap, gap+int64(time.Second), percentile) },
			"residual": func() { d.ResidualQuantileNanos(&gap, elapsed, percentile) },
			"sample":   func() { d.Sample(&gap, percentile, rng) },
			"survivor": func() { d.SampleResidual(&gap, elapsed, percentile, rng) },
			"envelope": func() { d.SampleMaximum(gap, gap+int64(time.Millisecond), percentile, rng) },
		} {
			// The first run builds the percentile's window table.
			if allocs := testing.AllocsPerRun(100, query); allocs != 0 {
				t.Fatalf("p%g %s: %g allocations per query", percentile, name, allocs)
			}
		}
	}
}
