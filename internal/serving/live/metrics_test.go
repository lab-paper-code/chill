package live

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func nextMetrics(t *testing.T, sub *Subscription) MetricsFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	data, err := sub.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var message struct {
		Type string `json:"type"`
		MetricsFrame
	}
	if err := json.Unmarshal(data, &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "metrics" {
		t.Fatalf("wanted metrics: %s", data)
	}
	return message.MetricsFrame
}

func metricFixture(t *testing.T) (*Metrics, *Subscription, time.Time) {
	t.Helper()
	at := testAt.Truncate(time.Second)
	hub := New(at)
	metrics := NewMetrics(hub, at)
	sub, err := hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = nextEnvelope(t, sub)
	t.Cleanup(func() { sub.Close(); hub.Close() })
	return metrics, sub, at
}

func TestMetricsUsesResponseFinishedCohortAndIdleZeros(t *testing.T) {
	metrics, sub, at := metricFixture(t)
	metrics.SetWorkloads([]string{"one", "idle"})
	metrics.Received("one", at.Add(100*time.Millisecond))
	if err := metrics.Flush(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	first := nextMetrics(t, sub)
	if first.Workloads["one"].Received != 1 || first.Workloads["one"].Completed != 0 || first.Workloads["idle"].P50MS != nil {
		t.Fatalf("received cohort: %+v", first)
	}
	fast, slow := 20*time.Millisecond, 100*time.Millisecond
	metrics.Finished("one", at.Add(1100*time.Millisecond), true, &fast, 50*time.Millisecond)
	metrics.Finished("one", at.Add(1200*time.Millisecond), false, &slow, 50*time.Millisecond)
	metrics.Finished("one", at.Add(1300*time.Millisecond), false, nil, 50*time.Millisecond)
	if err := metrics.Flush(at.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	second := nextMetrics(t, sub)
	row := second.Workloads["one"]
	if row.Received != 0 || row.Completed != 1 || row.Failed != 2 || row.WithinSLO != 1 || row.LatencySamples != 2 || row.P50MS == nil || *row.P50MS != 20 || row.P90MS == nil || *row.P90MS != 100 || *row.P95MS != 100 || *row.P99MS != 100 {
		t.Fatalf("terminal cohort/failed latency: %+v", row)
	}
	if idle, ok := second.Workloads["idle"]; !ok || idle.Received != 0 || idle.Completed != 0 || idle.Failed != 0 || idle.P99MS != nil {
		t.Fatalf("observable idle UID: %+v/%v", idle, ok)
	}
}

func TestMetricsTenSecondCorrectionAndOlderGap(t *testing.T) {
	metrics, sub, at := metricFixture(t)
	metrics.SetWorkloads([]string{"one"})
	metrics.Received("one", at.Add(time.Millisecond))
	if err := metrics.Flush(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	first := nextMetrics(t, sub)
	for second := 2; second <= 10; second++ {
		if err := metrics.Flush(at.Add(time.Duration(second) * time.Second)); err != nil {
			t.Fatal(err)
		}
		_ = nextMetrics(t, sub)
	}
	latency := 10 * time.Millisecond
	metrics.Finished("one", at.Add(time.Millisecond), true, &latency, time.Second)
	if err := metrics.Flush(at.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	correction := nextMetrics(t, sub)
	if correction.BucketAt != first.BucketAt || correction.Workloads["one"].Received != 1 || correction.Workloads["one"].Completed != 1 {
		t.Fatalf("correction not whole original bucket: %+v", correction)
	}
	if err := metrics.Flush(at.Add(11 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_ = nextMetrics(t, sub)
	metrics.Finished("one", at.Add(time.Millisecond), true, &latency, time.Second)
	if err := metrics.Flush(at.Add(12 * time.Second)); err != nil {
		t.Fatal(err)
	}
	late := nextMetrics(t, sub)
	if late.Workloads["one"].Completed != 0 || len(late.Gaps) != 1 || late.Gaps[0].From != timestamp(at) || late.Gaps[0].To != timestamp(at.Add(time.Second)) || late.Gaps[0].Scope != "workload:one" {
		t.Fatalf("uncorrectable old callback did not preserve original interval: %+v", late)
	}
}

func TestPowerMeansRequireCompleteUniqueScopeAndNeverCarryForward(t *testing.T) {
	metrics, sub, at := metricFixture(t)
	metrics.SetPowerScope([]string{"a", "a", "b"}, []string{"a", "a"}, true)
	wa, wb, wa2 := 10.0, 30.0, 20.0
	metrics.Power("a", at.Add(100*time.Millisecond), &wa)
	metrics.Power("a", at.Add(200*time.Millisecond), &wa2)
	if err := metrics.Flush(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	partial := nextMetrics(t, sub)
	if partial.Nodes["b"].Watts != nil || partial.Pool.WholeWatts != nil || partial.Pool.ActiveWatts == nil || *partial.Pool.ActiveWatts != 15 {
		t.Fatalf("partial scope falsely complete: %+v", partial)
	}
	metrics.Power("b", at.Add(300*time.Millisecond), &wb)
	if err := metrics.Flush(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	complete := nextMetrics(t, sub)
	if complete.Pool.WholeWatts == nil || *complete.Pool.WholeWatts != 45 || complete.Pool.ActiveWatts == nil || *complete.Pool.ActiveWatts != 15 || *complete.Nodes["a"].ObservedAt != timestamp(at.Add(200*time.Millisecond)) {
		t.Fatalf("sample mean/unique sums: %+v", complete)
	}
	if err := metrics.Flush(at.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	empty := nextMetrics(t, sub)
	if len(empty.Nodes) != 2 || empty.Nodes["a"].Watts != nil || empty.Nodes["a"].ObservedAt != nil || empty.Pool.WholeWatts != nil || empty.Pool.ActiveWatts != nil {
		t.Fatalf("power was carried forward: %+v", empty)
	}
	metrics.SetPowerScope([]string{"a"}, []string{"a"}, false)
	metrics.Power("a", at.Add(2100*time.Millisecond), &wa)
	if err := metrics.Flush(at.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	unknown := nextMetrics(t, sub)
	if unknown.Pool.WholeWatts != nil || unknown.Pool.ActiveWatts != nil {
		t.Fatalf("unknown membership produced sums: %+v", unknown)
	}
}

func TestUnobservableIntervalsAreGapsNotZeroTraffic(t *testing.T) {
	metrics, sub, at := metricFixture(t)
	metrics.SetWorkloads([]string{"one", "observable"})
	metrics.SetPowerScope([]string{"node"}, []string{"node"}, true)
	metrics.Gap(at, at.Add(time.Second), "workload:one", "source unavailable")
	metrics.Power("node", at.Add(time.Millisecond), nil)
	if err := metrics.Flush(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	message := nextMetrics(t, sub)
	if _, ok := message.Workloads["one"]; ok {
		t.Fatal("unobservable workload synthesized as zero")
	}
	if _, ok := message.Workloads["observable"]; !ok {
		t.Fatal("unrelated observable UID lost")
	}
	if len(message.Gaps) != 2 || message.Nodes["node"].Watts != nil || message.Pool.WholeWatts != nil {
		t.Fatalf("missing coverage hidden: %+v", message)
	}
}

func TestMembershipChangesWithinBucketDoNotInventCompletePool(t *testing.T) {
	metrics, sub, at := metricFixture(t)
	metrics.SetPowerScope([]string{"a", "b"}, []string{"a"}, true)
	watts := 10.0
	metrics.Power("a", at.Add(time.Millisecond), &watts)
	metrics.SetPowerScope([]string{"a", "b"}, []string{"b"}, true)
	metrics.Power("b", at.Add(2*time.Millisecond), &watts)
	if err := metrics.Flush(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	message := nextMetrics(t, sub)
	if message.Pool.WholeWatts != nil || message.Pool.ActiveWatts != nil {
		t.Fatalf("mid-bucket membership treated as known: %+v", message)
	}
}
