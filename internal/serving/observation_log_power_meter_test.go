package serving

import (
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
)

func TestOperatingMeterFactsIndependentOfScheduler(t *testing.T) {
	from := observationTestHour
	to := from.Add(10 * time.Second)
	for _, gap := range []bool{false, true} {
		result := OperatingPowerWindow{From: from, To: to}
		meter := OperatingPowerMeter{Node: NodeReference{Name: "inactive", UID: "inactive"}, Meter: "wall", Scope: "node-exclusive-wall", Source: operatingTestSource}
		_, history := configuredOperatingMeters(&result, []OperatingPowerMeter{meter})
		raw := &history[0]
		raw.hasPolicy = true
		raw.policy = powerwindow.Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 1, MaximumGap: 10 * time.Second}
		if gap {
			raw.policy.MaximumGap = time.Second
		}
		raw.samples[from], raw.samples[to] = 20, 20
		node := &result.Nodes[0]
		integrateOperatingNode(node, raw, from, to, false)
		if node.Joules != nil || len(node.Reasons) == 0 {
			t.Fatalf("legacy incomplete history changed: %+v", node)
		}
		if gap {
			if node.MeterComplete || node.MeterJoules != nil || node.GapSeconds != 10 || node.GapFraction != 1 || node.MaximumSampleGapSeconds != 10 {
				t.Fatalf("missing gap facts: %+v", node)
			}
		} else if !node.MeterComplete || node.MeterJoules == nil || *node.MeterJoules != 200 || node.GapSeconds != 0 || node.GapFraction != 0 || !reflect.DeepEqual(node.SourceIdentity, meter.Source) {
			t.Fatalf("scheduler-free inactive meter lost: %+v", node)
		}
	}
}
