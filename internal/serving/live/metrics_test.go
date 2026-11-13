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
	metrics.SetWorkloads([]Workload{{UID: "one"}, {UID: "idle"}}, at)
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

func TestMetricsP90DoesNotReuseP95(t *testing.T) {
	metrics, sub, at := metricFixture(t)
	metrics.SetWorkloads([]Workload{{UID: "one"}}, at)
	for index := 1; index <= 20; index++ {
		latency := time.Duration(index) * time.Millisecond
		metrics.Finished("one", at.Add(time.Duration(index)*time.Millisecond), true, &latency, time.Second)
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
	metrics.SetWorkloads([]Workload{{UID: "one"}}, at)
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
	metrics.SetWorkloads([]Workload{{UID: "one"}, {UID: "observable"}}, at)
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

func sloWorkload(percentile float64, target time.Duration) Workload {
	return Workload{UID: "one", SLO: SLO{Percentile: percentile, TargetLatencyNanos: int64(target)}}
}

func sloWorkloads(metrics *Metrics, at time.Time, rows ...Workload) { metrics.SetWorkloads(rows, at) }

// finish records a response_finished event of "one" at finishedAt.
func finish(metrics *Metrics, finishedAt time.Time, completed bool, latency, target time.Duration) {
	metrics.Finished("one", finishedAt, completed, &latency, target)
}

func sloFixture(t *testing.T, percentile float64, target, window time.Duration) (*Metrics, *Subscription, time.Time) {
	t.Helper()
	metrics, sub, at := metricFixture(t)
	sloWorkloads(metrics, at, sloWorkload(percentile, target))
	metrics.SetSLOWindow(window)
	return metrics, sub, at
}

// flushSLO flushes at now and returns the single emitted bucket.
func flushSLO(t *testing.T, metrics *Metrics, sub *Subscription, now time.Time) (string, *SLOWindow) {
	t.Helper()
	if err := metrics.Flush(now); err != nil {
		t.Fatal(err)
	}
	frame := nextMetrics(t, sub)
	return frame.BucketAt, frame.Workloads["one"].SLO
}

const noVerdict = -1.0

func assertSLO(t *testing.T, label string, got *SLOWindow, samples, within uint64, quantileMS float64) {
	t.Helper()
	if got == nil || got.Samples != samples || got.WithinTarget != within {
		t.Fatalf("%s: samples/within want %d/%d: %+v", label, samples, within, got)
	}
	if quantileMS == noVerdict {
		if got.QuantileMS != nil || got.Met != nil {
			t.Fatalf("%s: verdict without a covered nonempty window: %+v", label, got)
		}
		return
	}
	if got.QuantileMS == nil || *got.QuantileMS != quantileMS || got.Met == nil || *got.Met != (quantileMS <= got.TargetMS) {
		t.Fatalf("%s: quantile want %v: %+v", label, quantileMS, got)
	}
}

func TestMetricsSLOWindowMembershipIsFinishTime(t *testing.T) {
	metrics, sub, at := sloFixture(t, 100, time.Second, time.Second)
	finish(metrics, at.Add(1100*time.Millisecond), true, 200*time.Millisecond, time.Second)
	finish(metrics, at.Add(1500*time.Millisecond), false, 5*time.Millisecond, time.Second)
	finish(metrics, at.Add(1600*time.Millisecond), true, 0, time.Second)
	_, before := flushSLO(t, metrics, sub, at.Add(time.Second))
	assertSLO(t, "receipt second", before, 0, 0, noVerdict)
	_, after := flushSLO(t, metrics, sub, at.Add(2*time.Second))
	assertSLO(t, "finish second", after, 1, 1, 200)
}

func TestMetricsSLOWindowCountsSlowResponseAtItsFinish(t *testing.T) {
	metrics, sub, at := sloFixture(t, 95, time.Second, 2*time.Second)
	finish(metrics, at.Add(500*time.Millisecond), true, 100*time.Millisecond, time.Second)
	_, _ = flushSLO(t, metrics, sub, at.Add(time.Second))
	_, b1 := flushSLO(t, metrics, sub, at.Add(2*time.Second))
	assertSLO(t, "in-flight slow response is not yet a sample", b1, 1, 1, 100)
	_, b2 := flushSLO(t, metrics, sub, at.Add(3*time.Second))
	assertSLO(t, "empty window", b2, 0, 0, noVerdict)
	finish(metrics, at.Add(3200*time.Millisecond), true, 3*time.Second, time.Second)
	_, b3 := flushSLO(t, metrics, sub, at.Add(4*time.Second))
	assertSLO(t, "slow response at its finish", b3, 1, 0, 3000)
}

func TestMetricsSLOChangeDiscardsSamplesUntilCovered(t *testing.T) {
	metrics, sub, at := sloFixture(t, 50, 100*time.Millisecond, 2*time.Second)
	finish(metrics, at.Add(500*time.Millisecond), true, 50*time.Millisecond, 100*time.Millisecond)
	_, _ = flushSLO(t, metrics, sub, at.Add(time.Second))
	finish(metrics, at.Add(1500*time.Millisecond), true, 60*time.Millisecond, 100*time.Millisecond)
	_, b1 := flushSLO(t, metrics, sub, at.Add(2*time.Second))
	assertSLO(t, "old SLO", b1, 2, 2, 50)
	sloWorkloads(metrics, at.Add(2200*time.Millisecond), sloWorkload(50, 200*time.Millisecond))
	finish(metrics, at.Add(2500*time.Millisecond), true, 150*time.Millisecond, 200*time.Millisecond)
	finish(metrics, at.Add(2600*time.Millisecond), true, 70*time.Millisecond, 100*time.Millisecond)
	_, b2 := flushSLO(t, metrics, sub, at.Add(3*time.Second))
	assertSLO(t, "old samples and old-target response discarded", b2, 1, 1, noVerdict)
	if b2.TargetMS != 200 {
		t.Fatalf("current target not used: %+v", b2)
	}
	_, b3 := flushSLO(t, metrics, sub, at.Add(4*time.Second))
	assertSLO(t, "window starts before the change", b3, 1, 1, noVerdict)
	finish(metrics, at.Add(4500*time.Millisecond), true, 180*time.Millisecond, 200*time.Millisecond)
	_, b4 := flushSLO(t, metrics, sub, at.Add(5*time.Second))
	assertSLO(t, "covered after the change", b4, 1, 1, 180)
}

func TestMetricsSLOWindowWithoutCoverageHasNoVerdict(t *testing.T) {
	metrics, sub, at := sloFixture(t, 95, time.Second, time.Minute)
	finish(metrics, at.Add(500*time.Millisecond), true, 100*time.Millisecond, time.Second)
	_, slo := flushSLO(t, metrics, sub, at.Add(time.Second))
	assertSLO(t, "one second of a minute", slo, 1, 1, noVerdict)
	if slo.WindowSeconds != 60 || slo.Percentile != 95 || slo.TargetMS != 1000 {
		t.Fatalf("partial window lost its configuration: %+v", slo)
	}
}

func TestMetricsSLOWindowGrowthWaitsForRetainedCoverage(t *testing.T) {
	metrics, sub, at := sloFixture(t, 100, time.Second, 2*time.Second)
	step := func(second int) *SLOWindow {
		finish(metrics, at.Add(time.Duration(second)*time.Second+500*time.Millisecond), true, time.Duration(second+1)*time.Millisecond, time.Second)
		_, slo := flushSLO(t, metrics, sub, at.Add(time.Duration(second+1)*time.Second))
		return slo
	}
	for second := range 15 {
		_ = step(second)
	}
	// Retention covered the 2 s window plus the 10 s correction span: from 3 s.
	metrics.SetSLOWindow(20 * time.Second)
	grown := step(15)
	assertSLO(t, "grown window older than retention", grown, 13, 13, noVerdict)
	if grown.WindowSeconds != 20 {
		t.Fatalf("window length not followed: %+v", grown)
	}
	for second := 16; second < 21; second++ {
		_ = step(second)
	}
	assertSLO(t, "window still starts before retention", step(21), 19, 19, noVerdict)
	assertSLO(t, "window covered by retention", step(22), 20, 20, 23)
}

func TestMetricsSLOWindowWithoutSamplesOmitsQuantile(t *testing.T) {
	metrics, sub, at := metricFixture(t)
	sloWorkloads(metrics, at, sloWorkload(95, time.Second), Workload{UID: "untargeted"})
	metrics.SetSLOWindow(time.Second)
	if err := metrics.Flush(at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	frame := nextMetrics(t, sub)
	assertSLO(t, "covered empty window", frame.Workloads["one"].SLO, 0, 0, noVerdict)
	if frame.Workloads["untargeted"].SLO != nil {
		t.Fatalf("Workload without a target was judged: %+v", frame.Workloads["untargeted"].SLO)
	}
}

func TestMetricsSLOWindowHonoursConfiguredPercentile(t *testing.T) {
	metrics, sub, at := sloFixture(t, 99.5, time.Second, time.Second)
	for index := 1; index <= 200; index++ {
		finish(metrics, at.Add(500*time.Millisecond), true, time.Duration(index)*time.Millisecond, time.Second)
	}
	_, slo := flushSLO(t, metrics, sub, at.Add(time.Second))
	assertSLO(t, "p99.5 rank", slo, 200, 200, 199)
	if slo.Percentile != 99.5 {
		t.Fatalf("percentile: %+v", slo)
	}
}

func TestMetricsSLOWindowLateResponseRecomputesOnlyItsBucket(t *testing.T) {
	metrics, sub, at := sloFixture(t, 100, time.Second, 2*time.Second)
	var b2 string
	for second := range 4 {
		finish(metrics, at.Add(time.Duration(second)*time.Second+500*time.Millisecond), true, time.Duration(second+1)*10*time.Millisecond, time.Second)
		bucket, slo := flushSLO(t, metrics, sub, at.Add(time.Duration(second+1)*time.Second))
		if second == 2 {
			b2 = bucket
		}
		if second == 3 {
			assertSLO(t, "before correction", slo, 2, 2, 40)
		}
	}
	finish(metrics, at.Add(2700*time.Millisecond), true, 50*time.Millisecond, time.Second)
	bucket, corrected := flushSLO(t, metrics, sub, at.Add(4*time.Second))
	if bucket != b2 {
		t.Fatalf("late response re-emitted %s, want its bucket %s", bucket, b2)
	}
	assertSLO(t, "recomputed bucket", corrected, 3, 3, 50)
	finish(metrics, at.Add(4500*time.Millisecond), true, 5*time.Millisecond, time.Second)
	bucket, next := flushSLO(t, metrics, sub, at.Add(5*time.Second))
	if bucket != timestamp(at.Add(4*time.Second)) {
		t.Fatalf("a later bucket was re-emitted: %s", bucket)
	}
	assertSLO(t, "next window", next, 2, 2, 40)
}
