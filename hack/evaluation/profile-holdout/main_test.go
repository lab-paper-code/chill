package main

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

func latencyFixture(t *testing.T) (profile, []serving.BatchProfile, heldoutPhase, serving.SchedulerSettings) {
	t.Helper()
	training := profile{Batch: 2, IdleWatts: 10, EnergyPerInputJoules: 1}
	var batches []serving.BatchProfile
	for batch := 1; batch <= 2; batch++ {
		service := int64(time.Millisecond)
		if batch == 2 {
			service = int64(10 * time.Millisecond)
		}
		gap := int64(0)
		samples := []firstfloor.ServiceSample{
			{ServiceDurationNanos: service, TrialID: "training"},
			{ServiceDurationNanos: service, PrecedingIdleNanos: &gap, TrialID: "training"},
		}
		distribution, err := firstfloor.NewServiceDistribution(samples)
		if err != nil {
			t.Fatal(err)
		}
		batches = append(batches, serving.BatchProfile{Batch: batch, MemoryBytes: 1, EnergyPerInputJoules: 1,
			ServiceSamples: samples, ServiceDistribution: distribution})
	}
	start := time.Unix(1000, 0).UTC()
	heldout := heldoutPhase{TrialID: "heldout", PhaseID: "sparse", NativeSlots: 2,
		Arrivals:      []time.Time{start, start.Add(100 * time.Millisecond)},
		SLOPercentile: 97, SLOTargetLatency: time.Second, RequestLifetimeSeconds: 1}
	settings := serving.SchedulerSettings{
		ObservationWindow: 200 * time.Millisecond, ControlInterval: time.Second, ComparisonPeriod: 200 * time.Millisecond,
		MaxReplicas: 1, MaxInstances: 1, CapacityWindow: time.Millisecond, CapacityWindows: 1,
		CapacityPassingWindows: 1, CapacityMinimumRequests: 1, CapacityGridSteps: 1,
		CapacityBisectionWidth: .1, CapacityTailSamples: 1, PlanSearchWorkers: 1,
		PlanSearchStrategy: "v1", PlanSearchEvaluations: 1, PlanExactFinalists: 1,
		DefaultOperationCost: map[string]serving.OperationCostValue{
			"prepare": {Seconds: 1}, "remove": {Seconds: 1}, "power": {Seconds: 1},
		},
		DefaultIdlePower: &serving.IdlePowerDefault{NodeWatts: 10, PerInstanceWatts: 10},
	}
	return training, batches, heldout, settings
}

func TestLatencyFixedBatchSparseArrivals(t *testing.T) {
	training, batches, heldout, settings := latencyFixture(t)
	frozen := settings.Clone()
	out, err := predictLatency(training, batches, heldout, settings)
	if err != nil {
		t.Fatal(err)
	}
	if out.ExpectedNanos == nil || *out.ExpectedNanos != 110e6 || out.FixedBatchSize != 2 || out.NativeSlots != 2 {
		t.Fatalf("sparse fixed B=2 must wait 100ms then run measured 10ms, not fall back to B=1: %+v", out)
	}
	if len(out.Windows) != 1 || out.FeedbackScore == nil || out.FeedbackScore.Population != 2 || out.FeedbackScore.OnTime != 2 {
		t.Fatalf("recorded native slots must admit and drain one full fixed batch: %+v", out)
	}
	if len(out.Quantiles) != 5 || out.Quantiles[0].Percentile != 50 || out.Quantiles[0].ExpectedNanos == nil || *out.Quantiles[0].ExpectedNanos != 10e6 {
		t.Fatalf("scheduler permits additional SLO percentiles under unchanged settings: %+v", out.Quantiles)
	}
	if out.Quantiles[1].Percentile != 90 || out.Quantiles[1].ExpectedNanos != nil ||
		!strings.Contains(strings.Join(out.Quantiles[1].Reasons, ";"), "capacity replay: NoReadyCapacityOrTransitionPrerequisite") {
		t.Fatalf("unsupported frozen p90 rate diagnostic must stay local and retain its exact reason: %+v", out.Quantiles[1])
	}
	if !reflect.DeepEqual(settings, frozen) {
		t.Fatal("replay changed frozen scheduler settings")
	}
}

func TestLatencyConsecutiveFrozenWindows(t *testing.T) {
	training, batches, heldout, settings := latencyFixture(t)
	start := heldout.Arrivals[0]
	heldout.Arrivals = nil
	for _, offset := range []time.Duration{800, 0, 210, 100, 801, 200} {
		heldout.Arrivals = append(heldout.Arrivals, start.Add(offset*time.Millisecond))
	}
	original := append([]time.Time(nil), heldout.Arrivals...)
	out, err := predictLatency(training, batches, heldout, settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Windows) != 5 || out.ObservationWindow != settings.ObservationWindow || out.ComparisonPeriod != settings.ComparisonPeriod {
		t.Fatalf("long phase must retain consecutive frozen 200ms windows: %+v", out)
	}
	for j, window := range out.Windows {
		if !window.WindowStart.Equal(start.Add(time.Duration(j)*settings.ObservationWindow)) || window.WindowEnd.Sub(window.WindowStart) != settings.ObservationWindow {
			t.Fatalf("window %d changed frozen bounds: %+v", j, window)
		}
		want := 2
		if j == 2 || j == 3 {
			want = 0
		}
		if window.Arrivals != want {
			t.Fatalf("half-open window %d lost or repeated arrivals: %+v", j, window)
		}
	}
	if out.ExpectedNanos == nil || math.Abs(*out.ExpectedNanos-47e6) > 1e-6 || out.FeedbackScore != nil {
		t.Fatalf("aggregate means of independent 110/20/11ms window estimates, without inventing pooled feedback: %+v", out)
	}
	if out.Aggregation == "" || len(out.Limitations) < 3 || !reflect.DeepEqual(heldout.Arrivals, original) {
		t.Fatalf("aggregation limitations and immutable client arrivals required: %+v", out)
	}
}

func TestLatencyUnavailableWindowRemainsUnavailable(t *testing.T) {
	training, batches, heldout, settings := latencyFixture(t)
	start := heldout.Arrivals[0]
	// A final partial fixed batch cannot borrow arrivals from another window.
	heldout.Arrivals = append(heldout.Arrivals, start.Add(settings.ObservationWindow))
	out, err := predictLatency(training, batches, heldout, settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Windows) != 2 || out.ExpectedNanos != nil || out.Windows[1].Arrivals != 1 {
		t.Fatalf("one unavailable nonempty window must not become a finite phase estimate: %+v", out)
	}
	if len(out.Windows[1].Quantiles) == 0 || out.Windows[1].Quantiles[0].FeedbackScore == nil || out.Windows[1].Quantiles[0].FeedbackScore.Timeout != 1 {
		t.Fatalf("partial fixed batch must retain its lifetime timeout: %+v", out.Windows[1])
	}
}

func TestLatencyWindowAggregationUsesArrivalWeights(t *testing.T) {
	training, batches, heldout, settings := latencyFixture(t)
	training.Batch = 1
	start := heldout.Arrivals[0]
	heldout.Arrivals = []time.Time{start, start, start.Add(settings.ObservationWindow)}
	out, err := predictLatency(training, batches, heldout, settings)
	if err != nil {
		t.Fatal(err)
	}
	want := (2*2e6 + 1e6) / 3
	if out.ExpectedNanos == nil || math.Abs(*out.ExpectedNanos-want) > 1e-6 ||
		out.Windows[0].Arrivals != 2 || out.Windows[1].Arrivals != 1 {
		t.Fatalf("phase estimate must weight window estimates by recorded arrivals: %+v", out)
	}
}

func TestLatencyRecordedNativeCapacity(t *testing.T) {
	training, batches, heldout, settings := latencyFixture(t)
	// A single physical slot cannot fill fixed B=2, even with two scheduled arrivals.
	heldout.NativeSlots = 1
	out, err := predictLatency(training, batches, heldout, settings)
	if err != nil {
		t.Fatal(err)
	}
	if out.ExpectedNanos != nil || out.NativeSlots != 1 ||
		!strings.Contains(strings.Join(out.Reasons, ";"), "NoPlanMeetsSLO") {
		t.Fatalf("recorded one-slot fixed B=2 cannot meet SLO: %+v", out)
	}
	for _, slots := range []int{0, -1} {
		heldout.NativeSlots = slots
		if _, err := predictLatency(training, batches, heldout, settings); err == nil || !strings.Contains(err.Error(), "nativeSlots") {
			t.Fatalf("invalid recorded native capacity %d accepted: %v", slots, err)
		}
	}
}

func TestLatencyPercentileUpperEndpoint(t *testing.T) {
	for _, minimumRequests := range []int{1, 2} {
		training, batches, heldout, settings := latencyFixture(t)
		heldout.SLOPercentile = 100
		settings.CapacityMinimumRequests = minimumRequests
		out, err := predictLatency(training, batches, heldout, settings)
		if err != nil || out.Quantiles[len(out.Quantiles)-1].Percentile != 100 {
			t.Fatalf("exported scheduler validation permits p100: %+v %v", out, err)
		}
		if minimumRequests == 1 {
			if out.ExpectedNanos != nil || !strings.Contains(strings.Join(out.Reasons, ";"), "capacity replay: NoReadyCapacityOrTransitionPrerequisite") {
				t.Fatalf("a frozen one-arrival synthetic diagnostic cannot drain fixed B=2: %+v", out)
			}
		} else if out.ExpectedNanos == nil || *out.ExpectedNanos != 110e6 {
			t.Fatalf("a supported frozen two-arrival diagnostic must retain the actual p100 estimate: %+v", out)
		}
	}
}

func TestLatencyBridgeJSON(t *testing.T) {
	training, batches, heldout, settings := latencyFixture(t)
	out, err := predictLatency(training, batches, heldout, settings)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"expectedNanos", "quantiles", "windows", "aggregation", "limitations", "fixedBatchSize", "nativeSlots", "observationWindowNanos", "comparisonPeriodNanos"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("Python bridge field %q missing: %s", key, encoded)
		}
	}
	var invalid heldoutPhase
	if err := json.Unmarshal([]byte(`{"nativeSlots":1.5}`), &invalid); err == nil {
		t.Fatal("nativeSlots must be a JSON integer")
	}
}
