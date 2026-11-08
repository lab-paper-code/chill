package serving

import (
	"slices"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

func TestPathIsolationPreservesGlobalFailureDiagnostic(t *testing.T) {
	for _, strategy := range []string{protocol.V1, protocol.V2} {
		for _, total := range []float64{90, 110} {
			t.Run(strategy+"/"+map[bool]string{true: "path-alone", false: "path-and-service"}[total > 100], func(t *testing.T) {
				s, snap, _ := pathIsolationFixture()
				s.PlanSearchStrategy = strategy
				snap.PathBudgets[0].TotalMS = total
				rows := capacityRowsForTest(s, snap, nil)
				pathIsolationProof(rows, "w1")
				d := capacityDecideForTest(t, s, snap, rows)
				reason := schedulerPathServiceExceedsSLO + ":w1"
				if total > 100 { reason = schedulerPathBudgetExceedsSLO + ":w1" }
				state := slices.IndexFunc(d.Workloads, func(w SchedulerWorkloadState) bool { return w.UID == "w1" })
				if state < 0 || d.Workloads[state].State != schedulerWorkloadFixed || d.Workloads[state].Reason != reason || !slices.Contains(d.Reasons, reason) {
					t.Fatalf("global model failure disappeared: %+v", d)
				}
				if d.Target == nil || d.Target.Status != schedulerStatusFeasible || d.Capacity == nil || len(d.Capacity.Workloads) != 1 || d.Capacity.Workloads[0].UID != "w2" {
					t.Fatalf("independent controlled plan must remain feasible: %+v", d)
				}
				// A feasible controlled subset must not claim verified whole-service energy or SLO.
				if d.Keep.ExpectedJoules != nil || d.Target.ExpectedJoules != nil || !slices.ContainsFunc(d.Target.Assumptions, func(s string) bool {
					return strings.HasPrefix(s, "FixedScopeUnverified:")
				}) {
					t.Fatalf("fixed scope presented as a verified whole-service estimate: %+v", d.Target)
				}
			})
		}
	}
}

func TestPathIsolationRechecksRecoveryEachDecision(t *testing.T) {
	for _, strategy := range []string{protocol.V1, protocol.V2} {
		for _, tc := range []struct {
			name   string
			state  string
			reason string
		}{
			{name: "positive capacity", state: WorkloadControlled},
			{name: "grid zero", state: WorkloadControlled},
			{name: "missing proof", state: schedulerWorkloadFixed, reason: CapacityPending + ":w1"},
			{name: "rejected proof", state: schedulerWorkloadFixed, reason: CapacityRejected + ":w1"},
		} {
			t.Run(strategy+"/"+tc.name, func(t *testing.T) {
				s, snap, rows := pathIsolationFixture()
				s.PlanSearchStrategy = strategy
				isolated := capacityDecideForTest(t, s, snap, rows)
				if !slices.Contains(isolated.Reasons, schedulerPathServiceExceedsSLO+":w1") {
					t.Fatalf("fixture did not isolate: %+v", isolated)
				}
				// Publish a coherent replacement budget and row set, as Inputs does.
				snap.PathBudgets[0].TotalMS = 10
				rows = capacityRowsForTest(s, snap, nil)
				switch tc.name {
				case "grid zero":
					for j := range rows {
						if rows[j].Input.WorkloadUID == "w1" { zero := 0.0; rows[j].Kappa = &zero }
					}
				case "missing proof":
					rows = slices.DeleteFunc(rows, func(row CapacityRow) bool { return row.Input.WorkloadUID == "w1" })
				case "rejected proof":
					for j := range rows {
						if rows[j].Input.WorkloadUID == "w1" {
							rows[j].Status, rows[j].Reason, rows[j].Kappa = CapacityRejected, CapacityNonMonotone, nil
						}
					}
				}
				d := capacityDecideForTest(t, s, snap, rows)
				state := slices.IndexFunc(d.Workloads, func(w SchedulerWorkloadState) bool { return w.UID == "w1" })
				if state < 0 || d.Workloads[state].State != tc.state || d.Workloads[state].Reason != tc.reason || slices.ContainsFunc(d.Reasons, func(reason string) bool {
					return reason == schedulerPathServiceExceedsSLO+":w1" || reason == schedulerPathBudgetExceedsSLO+":w1"
				}) {
					t.Fatalf("old isolation survived replacement evidence: %+v", d)
				}
				if tc.name == "positive capacity" {
					if d.Capacity == nil || len(d.Capacity.Workloads) != 2 || d.Target == nil || d.Target.Status != schedulerStatusFeasible || d.Target.ExpectedJoules == nil || len(d.Target.Routing["w1"]) != 1 {
						t.Fatalf("positive capacity did not restore normal control: %+v", d)
					}
				}
			})
		}
	}
}
