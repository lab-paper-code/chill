package serving

import (
	"slices"
	"testing"
	"time"
)

const commitUnrelatedUID = "unrelated-uid"
const commitTerminalRecovery = "terminal-recovery"

// SCH-55/56, AC-42: E5-5-7 observed 95.1 while the frozen destination
// supplied 56.543181. The 34*1.25 low-load protection is not a future envelope.
func TestReductionCommitUsesLatestDemandAndFrozenIntegerRoute(t *testing.T) {
	for _, scenario := range []string{"low", "raw-95.1", "missing", "stale", "integer-share", "mixed-uid", commitUnrelatedUID, "not-ready-recovery", commitTerminalRecovery} {
		t.Run(scenario, func(t *testing.T) {
			s, snap := schedulerFixture()
			s.ObservationWindow, s.ControlInterval = time.Second, time.Second
			s.MaxObservationAge = time.Minute
			s.ScaleDownHeadroomFraction = .25
			snap.ObservedSince = snap.At.Add(-time.Hour)
			snap.Outstanding = nil
			capacityLoad(s, &snap, snap.Workloads[0], 34)
			snap.ScaleDown = map[string]SchedulerDownState{"w1": {Rate: 34, WindowMax: 34, Allowed: true}}
			snap.Routing = map[string]map[string]int32{"w1": {"i1": 1000}}
			old := snap.Instances[0]
			node := poolsAddNode(&snap, "new", map[string]float64{"fixed": 10})
			next := poolsInstance(old, "prepared", node, "fixed", "w1")
			snap.Options = append(snap.Options, poolsOption(next))
			target := SchedulerCandidate{Instances: []SchedulerInstance{next}, Routing: map[string]map[string]int32{"w1": {next.ID: 1000}}, Actions: []Action{{Kind: schedulerActionStop, Instance: old.ID}}}
			rows := capacityRowsForTest(s, snap, func(SchedulerOption) float64 { return 56.543181 })
			recovery := scenario == "not-ready-recovery" || scenario == commitTerminalRecovery
			want := scenario != "low" && scenario != commitUnrelatedUID && !recovery
			switch scenario {
			case "raw-95.1":
				snap.Arrivals = nil
				capacityLoad(s, &snap, snap.Workloads[0], 95.1)
				// Supply insufficiency caused by the burst is NOT a new recovery.
				snap.ScaleDown["w1"] = SchedulerDownState{Rate: 95.1, WindowMax: 95.1, Supply: 47.78107637511205, Allowed: false}
			case "missing":
				snap.ScaleDown = nil
			case "stale":
				snap.ObservationEnd = snap.At.Add(-2 * s.MaxObservationAge)
			case "integer-share":
				target.Instances = append(target.Instances, old)
				target.Routing["w1"] = map[string]int32{old.ID: 1, next.ID: 999}
				snap.ScaleDown["w1"] = SchedulerDownState{WindowMax: 50}
			case "mixed-uid":
				other := poolsAddWorkload(&snap, "other")
				snap.ScaleDown[other.UID] = SchedulerDownState{WindowMax: 100}
				target.Routing[other.UID] = map[string]int32{next.ID: 1000}
			case commitUnrelatedUID:
				other := poolsAddWorkload(&snap, "other")
				independent := old
				independent.ID, independent.WorkloadUID = "unrelated-instance", other.UID
				snap.Instances = append(snap.Instances, independent)
			case "not-ready-recovery", commitTerminalRecovery:
				snap.Instances[0].Ready = false
				snap.Instances[0].Terminated = scenario == commitTerminalRecovery
			}
			uids := SchedulerReductionWorkloads(s, snap, rows, target)
			if scenario == "mixed-uid" {
				uids = append(uids, "other")
			}
			if recovery && len(uids) != 0 || !recovery && !slices.Contains(uids, "w1") {
				t.Fatalf("wrong frozen reduction scope: %v", uids)
			}
			if scenario == commitUnrelatedUID && len(uids) != 1 {
				t.Fatalf("independent UID borrowed reduction ownership: %v", uids)
			}
			if got := SchedulerReductionCommit(s, snap, rows, target, uids); (got != "") != want {
				t.Fatalf("guard=%q want denied=%v", got, want)
			}
		})
	}
}

func TestReductionScopeDoesNotGateFastDistribution(t *testing.T) {
	settings, snapshot := schedulerFixture()
	old := snapshot.Instances[0]
	node := poolsAddNode(&snapshot, "fast-added", map[string]float64{"fixed": 10})
	added := poolsInstance(old, "fast-added-instance", node, "fixed", old.WorkloadUID)
	snapshot.Routing = map[string]map[string]int32{old.WorkloadUID: {old.ID: 1000}}
	target := SchedulerCandidate{Instances: []SchedulerInstance{old, added},
		Routing: map[string]map[string]int32{old.WorkloadUID: {old.ID: 0, added.ID: 1000}},
		Actions: []Action{{Kind: schedulerActionRoute}}}
	if uids := SchedulerReductionWorkloads(settings, snapshot, nil, target); len(uids) != 0 {
		t.Fatalf("fast distribution became nonrecovery retirement: %v", uids)
	}
}
