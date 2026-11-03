package observation

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
)

func TestPowerSeriesAlignedBucketsMatchPeriodEnergy(t *testing.T) {
	for _, zero := range []bool{false, true} {
		t.Run(map[bool]string{false: "ramp", true: "measured-zero"}[zero], func(t *testing.T) {
			base := observationTestHour.Add(10 * time.Minute)
			log, clock := clockedObservationLog(t, ObservationLogConfig{}, base.Add(-time.Minute))
			clock.set(base)
			nodes := []serving.NodeReference{{Name: "a", UID: "a-uid"}, {Name: "b", UID: "b-uid"}}
			meters := make([]OperatingPowerMeter, len(nodes))
			appendRecord := func(event SchedulerObservation) {
				t.Helper()
				if err := log.SchedulerRecord(event); err != nil {
					t.Fatal(err)
				}
			}
			for _, offset := range []time.Duration{0, 7 * time.Second} {
				snapshot := serving.SchedulerSnapshot{At: base.Add(offset), Instances: []serving.SchedulerInstance{{Node: nodes[0], WorkloadUID: "w"}}}
				if offset != 0 {
					snapshot.Instances[0].Node = nodes[1]
				}
				appendRecord(SchedulerObservation{At: snapshot.At, Snapshot: &snapshot})
			}
			policy := powerwindow.Policy{MaximumBoundaryDistance: time.Second, MaximumGap: time.Second, MinimumCoverage: 1}
			for n, node := range nodes {
				meters[n] = OperatingPowerMeter{Node: node, Meter: node.Name + "-wall", Scope: "node-exclusive-wall", Source: operatingTestSource}
				for _, start := range []int{0, 10} {
					power := metricobserver.Result{Source: operatingTestSource, StartedAt: base.Add(time.Duration(start) * time.Second), EndedAt: base.Add(time.Duration(start+10) * time.Second)}
					for i := start; i <= start+10; i++ {
						watts := float64(i + n*10)
						if zero {
							watts = 0
						}
						power.Samples = append(power.Samples, metricobserver.Sample{ObservedAt: base.Add(time.Duration(i) * time.Second), Watts: watts})
					}
					appendRecord(SchedulerObservation{At: power.EndedAt, Node: &node, Meter: meters[n].Meter, PowerScope: meters[n].Scope, Power: &power, PowerCoverage: &policy})
				}
			}
			from, to := base.Add(1500*time.Millisecond), base.Add(14500*time.Millisecond)
			period, err := log.OperatingPower(context.Background(), from, to, meters, 100)
			if err != nil {
				t.Fatal(err)
			}
			series, err := log.OperatingPowerSeries(context.Background(), from, to, 5*time.Second, meters, 100)
			if err != nil || len(series.Buckets) != 3 {
				t.Fatalf("series=%+v err=%v", series, err)
			}
			var whole, active float64
			for i, window := range series.Buckets {
				assertPowerSeriesBucket(t, window, base, from, to, i, nodes)
				whole += *window.WholePoolJoules
				active += *window.ActivePoolJoules
			}
			if period.WholePoolJoules == nil || period.ActivePoolJoules == nil || math.Abs(whole-*period.WholePoolJoules) > 1e-9 || math.Abs(active-*period.ActivePoolJoules) > 1e-9 {
				t.Fatalf("bucket sum whole=%g active=%g differs from period %+v", whole, active, period)
			}
			raw, err := json.Marshal(period)
			if err != nil || strings.Contains(string(raw), "observedSamples") || strings.Contains(string(raw), "firstObservedAt") {
				t.Fatalf("legacy period response acquired bucket fields: %s %v", raw, err)
			}
			if _, err := log.OperatingPowerSeries(context.Background(), from, to, 5*time.Second, meters, 1); rawErrorCode(err) != powerSeriesInvalidQuery {
				t.Fatalf("unbounded raw query: %v", err)
			}
		})
	}
}

func assertPowerSeriesBucket(t *testing.T, window OperatingPowerWindow, base, from, to time.Time, i int, nodes []serving.NodeReference) {
	t.Helper()
	wantFrom := serving.LaterTime(from, base.Add(time.Duration(i)*5*time.Second))
	wantTo := minTime(to, base.Add(time.Duration(i+1)*5*time.Second))
	if !window.From.Equal(wantFrom) || !window.To.Equal(wantTo) || !window.Complete || window.WholePoolJoules == nil || window.ActivePoolJoules == nil {
		t.Fatalf("bucket %d lost aligned/clipped energy: %+v", i, window)
	}
	if math.Abs(*window.WholePoolMeanWatts*window.To.Sub(window.From).Seconds()-*window.WholePoolJoules) > 1e-9 {
		t.Fatalf("bucket mean W does not use actual duration: %+v", window)
	}
	for n, node := range window.Nodes {
		wantSamples := []int{3, 5, 5}[i]
		if node.Node != nodes[n] || node.ObservedSamples == nil || *node.ObservedSamples != wantSamples || node.FirstObservedAt == nil || node.LastObservedAt == nil || !node.FirstObservedAt.Equal(base.Add(time.Duration([]int{2, 5, 10}[i])*time.Second)) || !node.LastObservedAt.Equal(base.Add(time.Duration([]int{4, 9, 14}[i])*time.Second)) {
			t.Fatalf("bucket %d real timestamps/counts (including dedup boundary): %+v", i, node)
		}
	}
}

func TestPowerSeriesEmptyBucketsAreNotInterpolatedMeasurements(t *testing.T) {
	base := observationTestHour.Add(time.Minute)
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, base)
	clock.set(base)
	node := serving.NodeReference{Name: "a", UID: "a-uid"}
	policy := powerwindow.Policy{MaximumBoundaryDistance: 10 * time.Second, MaximumGap: 10 * time.Second, MinimumCoverage: 1}
	power := metricobserver.Result{Source: operatingTestSource, StartedAt: base, EndedAt: base.Add(10 * time.Second), Samples: []metricobserver.Sample{{ObservedAt: base, Watts: 0}, {ObservedAt: base.Add(10 * time.Second), Watts: 0}}}
	for _, event := range []SchedulerObservation{{At: base, Snapshot: &serving.SchedulerSnapshot{At: base}}, {At: power.EndedAt, Node: &node, Meter: "wall", PowerScope: "node-exclusive-wall", Power: &power, PowerCoverage: &policy}} {
		if err := log.SchedulerRecord(event); err != nil {
			t.Fatal(err)
		}
	}
	meters := []OperatingPowerMeter{{Node: node, Meter: "wall", Scope: "node-exclusive-wall", Source: operatingTestSource}}
	series, err := log.OperatingPowerSeries(context.Background(), base, power.EndedAt, 2*time.Second, meters, 100)
	if err != nil || len(series.Buckets) != 5 {
		t.Fatalf("series=%+v err=%v", series, err)
	}
	first := series.Buckets[0]
	if first.WholePoolJoules == nil || *first.WholePoolJoules != 0 || !first.Complete || *first.Nodes[0].ObservedSamples != 1 {
		t.Fatalf("real zero disappeared: %+v", first)
	}
	for _, window := range series.Buckets[1:] {
		node := window.Nodes[0]
		if node.MeterComplete || node.MeterJoules != nil {
			t.Fatalf("empty bucket fabricated meter energy: %+v", node)
		}
		if window.Complete || window.WholePoolJoules != nil || window.WholePoolMeanWatts != nil || window.ActivePoolJoules != nil || node.Joules != nil || node.MeanWatts != nil || node.ObservedSamples == nil || *node.ObservedSamples != 0 || node.FirstObservedAt != nil || node.LastObservedAt != nil {
			t.Fatalf("empty bucket fabricated a measurement: %+v %+v", window, node)
		}
	}
	columns := series.Columns()
	for i := 1; i < len(series.Buckets); i++ {
		if columns.Nodes.MeterComplete[i][0] || columns.Nodes.MeterJoules[i][0] != nil {
			t.Fatalf("empty bucket columns fabricated meter energy: bucket %d", i)
		}
	}
}

func TestPowerBucketCountLimitsAndOffsetAlignment(t *testing.T) {
	base := observationTestHour
	for _, test := range []struct {
		name     string
		from, to time.Time
		bucket   time.Duration
		count    int
	}{
		{"max-range", base, base.Add(time.Hour), 4 * time.Second, 900},
		{"max-count", base, base.Add(900 * time.Second), time.Second, 900},
		{"offset-extra-bucket", base.Add(time.Millisecond), base.Add(900*time.Second + time.Millisecond), time.Second, 0},
		{"too-long", base, base.Add(time.Hour + time.Nanosecond), time.Hour, 0},
		{"too-many", base, base.Add(901 * time.Second), time.Second, 0},
		{"too-small", base, base.Add(time.Second), time.Millisecond, 0},
		{"too-large", base, base.Add(time.Second), time.Hour + time.Second, 0},
		{"zero", base, base.Add(time.Second), 0, 0},
		{"reversed", base.Add(time.Second), base, time.Second, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			count, err := PowerBucketCount(test.from, test.to, test.bucket)
			if test.count == 0 {
				if rawErrorCode(err) != powerSeriesInvalidQuery {
					t.Fatalf("invalid range accepted: %d %v", count, err)
				}
			} else if err != nil || count != test.count {
				t.Fatalf("count=%d want=%d err=%v", count, test.count, err)
			}
		})
	}
}
