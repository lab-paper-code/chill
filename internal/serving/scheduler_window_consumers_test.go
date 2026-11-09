package serving

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestSchedulerCurrentDemandReductionV1V2(t *testing.T) {
	for _, strategy := range []string{"v1", "v2"} {
		for _, rate := range []float64{20, 150} {
			t.Run(fmt.Sprintf("%s/rate-%g", strategy, rate), func(t *testing.T) {
				s, snapshot := schedulerFixture()
				s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod = time.Minute, 10*time.Second, time.Minute
				s.PlanSearchStrategy, s.ScaleDownHeadroomFraction = strategy, .25
				s.Controls.Power = false
				snapshot.ObservedSince = snapshot.At.Add(-time.Hour)
				old := snapshot.Instances[0]
				node := poolsAddNode(&snapshot, "n2", map[string]float64{"fixed": 10})
				other := poolsInstance(old, "i2", node, "fixed", "w1")
				snapshot.Instances = append(snapshot.Instances, other)
				snapshot.Options = append(snapshot.Options, poolsOption(other))
				snapshot.Routing = map[string]map[string]int32{"w1": {old.ID: 500, other.ID: 500}}
				for _, instance := range snapshot.Instances {
					snapshot.Costs = append(snapshot.Costs, poolsCost(schedulerActionRemove, instance.Node, schedulerState(instance), "unloaded", "fixed"))
				}
				capacityLoad(s, &snapshot, snapshot.Workloads[0], rate)
				decision, err := EvaluateScheduler(context.Background(), s, snapshot)
				if err != nil {
					t.Fatal(err)
				}
				want := 1
				if rate == 150 {
					want = 2
				}
				if decision.Target == nil || len(decision.Target.Instances) != want || len(decision.Target.Routing["w1"]) != want {
					t.Fatalf("rate=%g expected %d physical instances and routes: status=%s target=%+v reasons=%v", rate, want, decision.Status, decision.Target, decision.Reasons)
				}
				removes := 0
				for _, action := range decision.Target.Actions {
					if action.Kind == schedulerActionRemove {
						removes++
					}
				}
				if removes != 2-want {
					t.Fatalf("rate=%g unexpected removal effects: %+v", rate, decision.Target.Actions)
				}
			})
		}
	}
}
