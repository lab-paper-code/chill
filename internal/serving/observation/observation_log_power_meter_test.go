package observation

import (
	"reflect"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
)

func TestOperatingMeterFactsIndependentOfScheduler(t *testing.T) {
	from := observationTestHour
	to := from.Add(10 * time.Second)
	for _, gap := range []bool{false, true} {
		result := OperatingPowerWindow{From: from, To: to}
		meter := OperatingPowerMeter{Node: serving.NodeReference{Name: "inactive", UID: "inactive"}, Meter: "wall", Scope: "node-exclusive-wall", Source: operatingTestSource}
		_, history := configuredOperatingMeters(&result, []OperatingPowerMeter{meter})
		raw := &history[0]
		raw.hasPolicy = true
		raw.policy = powerwindow.Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 1, MaximumGap: 10 * time.Second}
		if gap {
			raw.policy.MaximumGap = time.Second
		}
		mergeOperatingPower(&result.Nodes[0], raw, &SchedulerObservation{Power: &metricobserver.Result{Source: operatingTestSource, Samples: []metricobserver.Sample{{ObservedAt: from, Watts: 20}, {ObservedAt: to, Watts: 20}}}, PowerCoverage: &raw.policy}, from, to)
		node := &result.Nodes[0]
		integrateOperatingNode(node, raw, from, to, false)
		if node.Joules != nil || len(node.Reasons) == 0 {
			t.Fatalf("legacy incomplete history changed: %+v", node)
		}
		if gap {
			if node.MeterComplete || node.MeterJoules == nil || *node.MeterJoules != 200 || node.GapCount != 1 || node.GapSeconds != 10 || node.GapFraction != 1 || node.MaximumSampleGapSeconds != 10 {
				t.Fatalf("missing gap facts: %+v", node)
			}
		} else if !node.MeterComplete || node.MeterJoules == nil || *node.MeterJoules != 200 || node.GapSeconds != 0 || node.GapFraction != 0 || !reflect.DeepEqual(node.SourceIdentity, meter.Source) {
			t.Fatalf("scheduler-free inactive meter lost: %+v", node)
		}
	}
}

func TestOperatingMeterGapUsesOriginalBracketingTrapezoid(t *testing.T) {
	from := observationTestHour
	to := from.Add(10 * time.Second)
	points := []powerwindow.Sample{{ObservedAt: from, Watts: 0}, {ObservedAt: to, Watts: 100}}
	policy := powerwindow.Policy{MaximumBoundaryDistance: 5 * time.Second, MinimumCoverage: 1, MaximumGap: time.Second}
	if _, err := powerwindow.Integrate(powerwindow.Window{Start: from, End: to, Samples: points}, policy); err == nil {
		t.Fatal("profiling must still reject the original gap")
	}
	start, end := from.Add(4*time.Second), from.Add(6*time.Second)
	meter := OperatingPowerMeter{Node: serving.NodeReference{Name: "ramp", UID: "ramp"}, Meter: "wall", Scope: "node-exclusive-wall", Source: operatingTestSource}
	power := metricobserver.Result{Source: operatingTestSource, StartedAt: from, EndedAt: to, Samples: []metricobserver.Sample{{ObservedAt: from, Watts: 0}, {ObservedAt: to, Watts: 100}}}
	result, err := ReduceOperatingPower(start, end, []OperatingPowerMeter{meter}, 10, func(yield func(*SchedulerObservation) error) error {
		return yield(&SchedulerObservation{Node: &meter.Node, Meter: meter.Meter, PowerScope: meter.Scope, Power: &power, PowerCoverage: &policy})
	})
	if err != nil {
		t.Fatal(err)
	}
	node := result.Nodes[0]
	if node.MeterJoules == nil || *node.MeterJoules != 100 || node.MeterComplete || node.GapCount != 1 || node.GapSeconds != 2 || node.GapFraction != 1 || node.MaximumSampleGapSeconds != 10 || node.Samples != 2 {
		t.Fatalf("original gap or trapezoid lost during clipping: %+v", node)
	}
	if policy.MaximumGap != time.Second {
		t.Fatal("serving changed the recorded policy")
	}
}

func TestOperatingMeterRejectsTruncatedRecordsAndUnverifiedSources(t *testing.T) {
	from := observationTestHour
	to := from.Add(10 * time.Second)
	meter := OperatingPowerMeter{Node: serving.NodeReference{Name: "node", UID: "node"}, Meter: "wall", Scope: "node-exclusive-wall", Source: operatingTestSource}
	policy := powerwindow.Policy{MaximumBoundaryDistance: time.Second, MaximumGap: 10 * time.Second, MinimumCoverage: 1}
	for _, name := range []string{"limit", "historical-source", "mixed-source", "missing-source"} {
		t.Run(name, func(t *testing.T) {
			limit := 10
			if name == "limit" {
				limit = 1
			}
			r := newOperatingPowerReducer(from, to, []OperatingPowerMeter{meter}, limit)
			source := operatingTestSource
			source.Endpoint = "http://historical/metrics"
			if name == "missing-source" {
				source = metricobserver.SourceIdentity{}
			}
			event := SchedulerObservation{Node: &meter.Node, Meter: meter.Meter, PowerScope: meter.Scope, PowerCoverage: &policy, Power: &metricobserver.Result{Source: source, StartedAt: from, EndedAt: to, Samples: []metricobserver.Sample{{ObservedAt: from, Watts: 20}, {ObservedAt: to, Watts: 20}}}}
			if err := r.add(&event); err != nil {
				t.Fatal(err)
			}
			if name == "limit" || name == "mixed-source" {
				other := event
				power := *event.Power
				power.Source = operatingTestSource
				other.Power = &power
				if err := r.add(&other); err != nil {
					t.Fatal(err)
				}
			}
			node := r.finish().Nodes[0]
			if name == "historical-source" {
				if !node.MeterComplete || node.MeterJoules == nil || !reflect.DeepEqual(node.SourceIdentity, source) {
					t.Fatalf("observed source replaced with configuration: %+v", node)
				}
				return
			}
			if node.MeterComplete || node.MeterJoules != nil || len(node.Reasons) == 0 {
				t.Fatalf("unverified meter admitted: %+v", node)
			}
			if name == "missing-source" && !reflect.DeepEqual(node.SourceIdentity, metricobserver.SourceIdentity{}) {
				t.Fatalf("missing source fabricated: %+v", node.SourceIdentity)
			}
		})
	}
}
