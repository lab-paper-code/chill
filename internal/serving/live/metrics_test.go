package live

import (
	"context"
	"encoding/json"
	"sync/atomic"
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
	metrics := NewMetrics(hub, at, nil, nil)
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
	metrics.SetWorkloads([]Workload{{UID: "one"}, {UID: "idle"}})
	metrics.Received("one", at.Add(100*time.Millisecond))
	if err := metrics.Flush(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	first := nextMetrics(t, sub)
	if first.Workloads["one"].Received != 1 || first.Workloads["one"].Completed != 0 || first.Workloads["idle"].P50MS != nil {
		t.Fatalf("received cohort: %+v", first)
	}
	fast, slow := 20*time.Millisecond, 100*time.Millisecond
	metrics.Finished("one", at.Add(1100*time.Millisecond), at, true, &fast, 50*time.Millisecond)
	metrics.Finished("one", at.Add(1200*time.Millisecond), at, false, &slow, 50*time.Millisecond)
	metrics.Finished("one", at.Add(1300*time.Millisecond), at, false, nil, 50*time.Millisecond)
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

func TestMetricsP90DoesNotReuseP95(t *testing.T) {
	metrics, sub, at := metricFixture(t)
	metrics.SetWorkloads([]Workload{{UID: "one"}})
	for index := 1; index <= 20; index++ {
		latency := time.Duration(index) * time.Millisecond
		metrics.Finished("one", at.Add(time.Duration(index)*time.Millisecond), at, true, &latency, time.Second)
	}
	if err := metrics.Flush(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	row := nextMetrics(t, sub).Workloads["one"]
	if row.LatencySamples != 20 || row.P90MS == nil || row.P95MS == nil || *row.P90MS != 18 || *row.P95MS != 19 {
		t.Fatalf("distinct percentile ranks were lost: %+v", row)
	}
}

func TestMetricsTenSecondCorrectionAndOlderGap(t *testing.T) {
	metrics, sub, at := metricFixture(t)
	metrics.SetWorkloads([]Workload{{UID: "one"}})
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
	metrics.Finished("one", at.Add(time.Millisecond), at, true, &latency, time.Second)
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
	metrics.Finished("one", at.Add(time.Millisecond), at, true, &latency, time.Second)
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
	metrics.SetWorkloads([]Workload{{UID: "one"}, {UID: "observable"}})
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

func TestMetricsCurrentQueueGauges(t *testing.T) {
	at := testAt.Truncate(time.Second)
	hub := New(at)
	var length, oldest atomic.Int64
	metrics := NewMetrics(hub, at, &length, &oldest)
	sub, err := hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sub.Close(); hub.Close() })
	_ = nextEnvelope(t, sub)
	length.Store(3)
	oldest.Store(at.Add(-500 * time.Millisecond).UnixNano())
	if err := metrics.Flush(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	first := nextMetrics(t, sub)
	if first.ObslogQueueLength != 3 || first.ObslogOldestWaitSeconds != 1.5 {
		t.Fatalf("current queue sample: %+v", first)
	}
	length.Store(1)
	oldest.Store(at.Add(750 * time.Millisecond).UnixNano())
	metrics.Received("one", at)
	if err := metrics.Flush(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	correction := nextMetrics(t, sub)
	if correction.BucketAt != first.BucketAt || correction.ObslogQueueLength != 1 || correction.ObslogOldestWaitSeconds != .25 {
		t.Fatalf("corrected bucket used stale queue gauges: %+v", correction)
	}
	oldest.Store(at.Add(3 * time.Second).UnixNano())
	if err := metrics.Flush(at.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	backward := nextMetrics(t, sub)
	if backward.ObslogQueueLength != 1 || backward.ObslogOldestWaitSeconds != 0 {
		t.Fatalf("backward clock produced negative age: %+v", backward)
	}
	length.Store(0)
	oldest.Store(at.UnixNano())
	if err := metrics.Flush(at.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	empty := nextMetrics(t, sub)
	if empty.ObslogQueueLength != 0 || empty.ObslogOldestWaitSeconds != 0 {
		t.Fatalf("empty queue retained age: %+v", empty)
	}
}

// finishedAfter records a response_finished event received at receivedAt.
func finishedAfter(metrics *Metrics, receivedAt time.Time, completed bool, latency time.Duration) {
	metrics.Finished("one", receivedAt.Add(latency), receivedAt, completed, &latency, time.Second)
}

func TestMetricsSLOWindowSlidesOverReceivedCompletedRequests(t *testing.T) {
	metrics, sub, at := metricFixture(t)
	metrics.SetWorkloads([]Workload{{UID: "one", SLO: SLO{Percentile: 50, TargetLatencyNanos: int64(25 * time.Millisecond)}}})
	metrics.SetSLOWindow(3 * time.Second)
	finishedAfter(metrics, at.Add(500*time.Millisecond), true, 100*time.Millisecond)
	finishedAfter(metrics, at.Add(1200*time.Millisecond), true, 10*time.Millisecond)
	finishedAfter(metrics, at.Add(2300*time.Millisecond), true, 20*time.Millisecond)
	finishedAfter(metrics, at.Add(3100*time.Millisecond), false, 5*time.Millisecond)
	finishedAfter(metrics, at.Add(3200*time.Millisecond), true, 0)
	finishedAfter(metrics, at.Add(3400*time.Millisecond), true, 30*time.Millisecond)
	finishedAfter(metrics, at.Add(3600*time.Millisecond), true, 40*time.Millisecond)
	if err := metrics.Flush(at.Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	first := nextMetrics(t, sub).Workloads["one"].SLO
	if first == nil || first.WindowSeconds != 3 || first.Percentile != 50 || first.TargetMS != 25 || first.Samples != 1 || first.WithinTarget != 0 ||
		first.QuantileMS == nil || *first.QuantileMS != 100 || first.Met == nil || *first.Met {
		t.Fatalf("first window: %+v", first)
	}
	_, _ = nextMetrics(t, sub), nextMetrics(t, sub)
	last := nextMetrics(t, sub).Workloads["one"].SLO
	if last == nil || last.Samples != 4 || last.WithinTarget != 2 || last.QuantileMS == nil || *last.QuantileMS != 20 || last.Met == nil || !*last.Met {
		t.Fatalf("sliding window kept an old, failed or zero-latency sample: %+v", last)
	}
}

func TestMetricsSLOWindowWithoutSamplesOmitsQuantile(t *testing.T) {
	metrics, sub, at := metricFixture(t)
	metrics.SetWorkloads([]Workload{{UID: "one", SLO: SLO{Percentile: 95, TargetLatencyNanos: int64(time.Second)}}, {UID: "untargeted"}})
	metrics.SetSLOWindow(time.Minute)
	if err := metrics.Flush(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	frame := nextMetrics(t, sub)
	slo := frame.Workloads["one"].SLO
	if slo == nil || slo.WindowSeconds != 60 || slo.TargetMS != 1000 || slo.Samples != 0 || slo.WithinTarget != 0 || slo.QuantileMS != nil || slo.Met != nil {
		t.Fatalf("empty window: %+v", slo)
	}
	if frame.Workloads["untargeted"].SLO != nil {
		t.Fatalf("Workload without a target was judged: %+v", frame.Workloads["untargeted"].SLO)
	}
}

func TestMetricsSLOWindowHonoursConfiguredPercentile(t *testing.T) {
	metrics, sub, at := metricFixture(t)
	metrics.SetWorkloads([]Workload{{UID: "one", SLO: SLO{Percentile: 99.5, TargetLatencyNanos: int64(time.Second)}}})
	metrics.SetSLOWindow(time.Second)
	for index := 1; index <= 200; index++ {
		finishedAfter(metrics, at, true, time.Duration(index)*time.Millisecond)
	}
	if err := metrics.Flush(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	slo := nextMetrics(t, sub).Workloads["one"].SLO
	if slo == nil || slo.Percentile != 99.5 || slo.Samples != 200 || slo.QuantileMS == nil || *slo.QuantileMS != 199 {
		t.Fatalf("p99.5 rank: %+v", slo)
	}
}

func TestMetricsSLOWindowFollowsConfiguredLengthOnCorrection(t *testing.T) {
	metrics, sub, at := metricFixture(t)
	metrics.SetWorkloads([]Workload{{UID: "one", SLO: SLO{Percentile: 100, TargetLatencyNanos: int64(time.Second)}}})
	metrics.SetSLOWindow(2 * time.Second)
	for second := range 4 {
		finishedAfter(metrics, at.Add(time.Duration(second)*time.Second+500*time.Millisecond), true, time.Duration(second+1)*10*time.Millisecond)
	}
	if err := metrics.Flush(at.Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		_ = nextMetrics(t, sub)
	}
	short := nextMetrics(t, sub)
	metrics.SetSLOWindow(4 * time.Second)
	metrics.Received("one", at.Add(3900*time.Millisecond))
	if err := metrics.Flush(at.Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	long := nextMetrics(t, sub)
	if long.BucketAt != short.BucketAt || short.Workloads["one"].SLO == nil || short.Workloads["one"].SLO.Samples != 2 ||
		long.Workloads["one"].SLO == nil || long.Workloads["one"].SLO.Samples != 4 || long.Workloads["one"].SLO.WindowSeconds != 4 {
		t.Fatalf("window length change: short=%+v long=%+v", short.Workloads["one"].SLO, long.Workloads["one"].SLO)
	}
}
