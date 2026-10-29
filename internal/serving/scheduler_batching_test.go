package serving

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestBatchingOffMeasuredPolicySet(t *testing.T) {
	s, snap := schedulerFixture()
	s.Controls.Batching = false
	original := snap.Options[0]
	snap.Options[0].Instance.MaxBatch = 2
	// A measured cap1 must remain available even when the source option has cap2.
	options := schedulerMeasuredOptions(context.Background(), s, snap)
	if len(options) != 1 || options[0].Instance.MaxBatch != 1 {
		t.Fatalf("measured variants: %+v", options)
	}
	p := capacityPlannerForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	if !p.controlAllows(0, options[0].Instance, snap.Instances) {
		t.Fatal("current cap1 excluded")
	}
	other := original.Instance
	other.MaxBatch = 2
	if p.controlAllows(0, other, snap.Instances) {
		t.Fatal("unconfigured cap2 allowed")
	}
	p.snapshot.Instances = append(p.snapshot.Instances, other)
	if !p.controlAllows(0, other, snap.Instances) {
		t.Fatal("second current policy excluded")
	}
	other.MaxWait++
	if p.controlAllows(0, other, snap.Instances) {
		t.Fatal("unconfigured MaxWait allowed")
	}
	snap.Options[0].Instance.MaxBatch = 1
	snap.Options[0].Instance.Profile = nil
	if got := schedulerMeasuredOptions(context.Background(), s, snap); len(got) != 0 {
		t.Fatal("invented unmeasured cap1")
	}
}

// TestBatchingOffSnapshotReplay uses the existing switch-replay inputs and rows.
// Keep the large captured production evidence outside the source tree.
func TestBatchingOffSnapshotReplay(t *testing.T) {
	root := os.Getenv("SWITCH_REPLAY_DIR")
	if root == "" {
		t.Skip("set SWITCH_REPLAY_DIR to captured switch-replay evidence")
	}
	load := func(name string, v any) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	var cases []struct {
		ID       string            `json:"id"`
		Snapshot SchedulerSnapshot `json:"snapshot"`
		Settings SchedulerSettings `json:"settings"`
	}
	var rows []CapacityRow
	load("inputs.json", &cases)
	load("capacity.json", &rows)
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			s, snap := c.Settings, c.Snapshot
			s.Controls = SchedulerControls{true, true, true, true, false, true, true}
			p := capacityPlannerForTest(t, s, snap, rows)
			before, after, compatible := 0, 0, 0
			caps := map[int]bool{}
			count := func(options []SchedulerOption, old bool) {
				for _, o := range options {
					for w, state := range p.ws {
						if !slices.Contains(o.CompatibleUIDs, state.d.UID) {
							continue
						}
						if old {
							compatible++
						}
						if p.controlAllows(w, o.Instance, snap.Instances) {
							if old {
								before++
							} else {
								after++
								caps[o.Instance.MaxBatch] = true
							}
						}
					}
				}
			}
			count(snap.Options, true)
			count(p.options, false)
			if after <= before {
				t.Fatalf("fixed policy variants not restored: before=%d after=%d", before, after)
			}
			d, err := EvaluateScheduler(context.Background(), s, snap, rows)
			if err != nil {
				t.Fatal(err)
			}
			if d.Target == nil {
				t.Fatalf("no evaluated target: %s %v", d.Status, d.Reasons)
			}
			if d.Capacity == nil || d.Capacity.Search == nil || d.Capacity.Search.Evaluated == 0 {
				t.Fatal("fixed-cap non-keep candidates were not evaluated")
			}
			if c.ID == "E5-2" && (compatible != 78 || before != 8) {
				t.Fatalf("factors-01 baseline changed: compatible=%d allowed=%d", compatible, before)
			}
			if c.ID == "E5-5" && (!caps[1] || !caps[2]) {
				t.Fatalf("current mixed cap set lost: %v", caps)
			}
			t.Logf("compatible=%d legacyAllowed=%d fixedPolicyAllowed=%d caps=%v status=%s examined=%d evaluated=%d", compatible, before, after, caps, d.Status, d.Search.Examined, d.Capacity.Search.Evaluated)
		})
	}
}
