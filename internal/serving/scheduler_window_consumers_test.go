package serving

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDecodeSchedulerLineOmitsHistoricalTimerEvidence(t *testing.T) {
	for _, evidence := range []string{
		`{"baselineRate":110,"previousRate":20,"rate":20,"downSince":"2026-10-26T00:00:00Z","elapsedNanos":300000000000,"allowed":true,"reason":"ScaleDownStabilized"}`,
		`{"rate":20,"allowed":true}`,
		`null`,
	} {
		raw := []byte(`{"decision":{"capacity":{"workloads":[{"uid":"w","scaleDown":` + evidence + `}]}},"snapshot":{"scaleDown":{"w":` + evidence + `},"emptyNodeScaleDownAllowed":true}}`)
		original := bytes.Clone(raw)
		event, err := DecodeSchedulerLine(raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if event.Decision.Capacity.Workloads[0].ScaleDown != nil || len(event.Snapshot.ScaleDown) != 0 || event.Snapshot.EmptyNodeScaleDownAllowed {
			t.Fatalf("historical timer became window evidence: %+v %+v", event.Decision.Capacity.Workloads[0], event.Snapshot)
		}
		encoded, err := json.Marshal(event)
		if err != nil || bytes.Contains(encoded, []byte(`"scaleDown"`)) || !bytes.Equal(raw, original) {
			t.Fatalf("typed omission changed raw evidence or retained timer: %s %v", encoded, err)
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
