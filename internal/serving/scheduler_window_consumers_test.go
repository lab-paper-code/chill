package serving

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDecodeSchedulerLineRequiresMaximumDemandFields(t *testing.T) {
	for _, field := range []string{"windowMax", "windowNanos", "supply"} {
		evidence := map[string]any{"windowMax": 0, "windowNanos": 0, "supply": 0}
		delete(evidence, field)
		raw, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range []string{
			`{"decision":{"capacity":{"workloads":[{"uid":"w","scaleDown":` + string(raw) + `}]}}}`,
			`{"snapshot":{"scaleDown":{"w":` + string(raw) + `}}}`,
		} {
			if _, err := DecodeSchedulerLine([]byte(line), nil); err == nil {
				t.Fatalf("scaleDown without %s accepted: %s", field, line)
			}
		}
	}
}

func TestDecodeSchedulerLinePreservesMaximumDemandEvidence(t *testing.T) {
	for _, evidence := range []string{
		`{"rate":20,"windowMax":110,"windowNanos":300000000000,"supply":160,"allowed":true,"reason":"ScaleDownStabilized","resetReason":"FirstObservation"}`,
		`{"rate":0,"windowMax":0,"windowNanos":0,"supply":0,"allowed":false,"reason":"NoSpareCapacity"}`,
	} {
		raw := []byte(`{"decision":{"capacity":{"workloads":[{"uid":"w","scaleDown":` + evidence + `}]}},"snapshot":{"scaleDown":{"w":` + evidence + `},"emptyNodeScaleDownAllowed":true}}`)
		event, err := DecodeSchedulerLine(raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		var want SchedulerDownState
		if err := json.Unmarshal([]byte(evidence), &want); err != nil {
			t.Fatal(err)
		}
		got := event.Decision.Capacity.Workloads[0].ScaleDown
		if got == nil || *got != want || event.Snapshot.ScaleDown["w"] != want || !event.Snapshot.EmptyNodeScaleDownAllowed {
			t.Fatalf("window evidence changed: %+v %+v", got, event.Snapshot)
		}
	}
}

func TestCapacitySlowCandidateServesWindowMaximumWithoutChangingEnergyLoad(t *testing.T) {
	uid := "w"
	old := SchedulerInstance{ID: "old", WorkloadUID: uid}
	removed := SchedulerInstance{ID: "removed", WorkloadUID: uid}
	p := &capacityPlanner{
		ctx: context.Background(), period: 600,
		settings: SchedulerSettings{Controls: SchedulerControls{Routing: true}},
		snapshot: SchedulerSnapshot{
			Nodes: []SchedulerNode{{}}, Instances: []SchedulerInstance{old, removed},
			ScaleDown: map[string]SchedulerDownState{uid: {Rate: 20, WindowMax: 110, WindowNanos: time.Minute, Supply: 160, Allowed: true}},
		},
		ws:       []capacityWorkload{{d: &Definition{UID: uid}, rate: 20, slack: 1}},
		compared: []bool{false},
	}
	keep := &capacityPlan{keep: true, instances: []SchedulerInstance{old, removed}, items: [][]capacityItem{{{id: "old", kappa: 160, e: 1}}}, retained: [][]capacityItem{{{id: "old", kappa: 160, e: 1}}}}
	slow := &capacityPlan{record: SchedulerNodePlan{Tempo: schedulerTempoSlow}, instances: []SchedulerInstance{old}, items: [][]capacityItem{{{id: "old", kappa: 60, e: 1}}}, retained: [][]capacityItem{{{id: "old", kappa: 20, e: 1}}}}
	p.plans = [][]*capacityPlan{{keep, slow}}
	if got := p.evaluate([]int{1}, 0); p.admits(&got) || got.exclusion != schedulerWindowCapacityInsufficient {
		t.Fatalf("60 capacity admitted for maximum demand 110: %+v", got)
	}
	slow.items[0][0].kappa = 120
	if got := p.evaluate([]int{1}, 0); !p.admits(&got) || !got.feasible || got.joules != 20*600 || p.ws[0].rate != 20 {
		t.Fatalf("120 capacity rejected or D charged as energy load: %+v", got)
	}
	if got := p.evaluate([]int{1}, 2); p.admits(&got) || got.exclusion != schedulerWindowCapacityInsufficient {
		t.Fatalf("transition SLO used current rate instead of maximum: %+v", got)
	}
	// The history guard does not turn safe fast expansion into a window wait.
	slow.record.Tempo = schedulerTempoFast
	p.snapshot.ScaleDown[uid] = SchedulerDownState{WindowMax: 1000}
	if got := p.evaluate([]int{1}, 0); !p.admits(&got) || !got.feasible {
		t.Fatalf("fast candidate waited for unrelated maximum: %+v", got)
	}
}

func TestEvaluateSchedulerZeroWindowObservesCurrentDemandAndSupply(t *testing.T) {
	for _, rate := range []float64{20, 100, 120} {
		s, snap := schedulerFixture()
		s.ScaleDownStabilization = 0
		snap.ObservedSince = snap.At.Add(-s.ObservationWindow)
		snap.Routing = map[string]map[string]int32{"w1": {"i1": schedulerRouteUnits}}
		snap.Instances[0].Routable = map[string]bool{"w1": true}
		capacityLoad(s, &snap, snap.Workloads[0], rate)
		d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
		if d.Capacity == nil || len(d.Capacity.Workloads) != 1 || d.Capacity.Workloads[0].ScaleDown == nil {
			t.Fatalf("direct entry omitted current-demand evidence: %+v", d.Capacity)
		}
		state := d.Capacity.Workloads[0].ScaleDown
		if state.WindowMax != rate || state.Supply != 100 || state.Allowed != (rate < 100) || state.WindowNanos != 0 {
			t.Fatalf("zero-window demand %g bypassed current supply: %+v", rate, state)
		}
	}
}
