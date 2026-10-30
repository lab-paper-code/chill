package serving

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"slices"
	"testing"
	"time"
)

func TestCapacityLeximinAC40(t *testing.T) {
	keep := capacityResult{worst: math.Inf(1), excesses: []float64{math.Inf(1), math.Inf(1), math.Inf(1)}, keep: true}
	improved := capacityResult{worst: math.Inf(1), excesses: []float64{math.Inf(1), 2, -0.5}, joules: 100}
	if !improved.better(&keep) || keep.better(&improved) {
		t.Fatal("one impossible Workload hid the other Workloads' improvement")
	}
	second := improved
	second.excesses = []float64{math.Inf(1), 2, -0.6}
	second.joules = 200
	if !second.better(&improved) {
		t.Fatal("third exceedance did not break the worst/second-worst tie")
	}
	second.excesses = slices.Clone(improved.excesses)
	if second.better(&improved) {
		t.Fatal("equal exceedances did not prefer lower energy")
	}
}

func TestBootstrapWaitSettings(t *testing.T) {
	s, _ := schedulerFixture()
	margin := 0.0
	input := SchedulerSettingsInput{SchedulerSettings: s, CompletionMarginMS: &margin}
	got, err := input.Settings()
	if err != nil || got.BootstrapCapacityWait != 10*time.Minute {
		t.Fatalf("default wait: %v, %v", got.BootstrapCapacityWait, err)
	}
	for _, wait := range []time.Duration{0, time.Second, 3 * time.Minute, time.Hour, -1, time.Hour + 1} {
		input.BootstrapCapacityWait = &wait
		got, err = input.Settings()
		valid := wait >= 0 && wait <= time.Hour
		if (err == nil) != valid || valid && got.BootstrapCapacityWait != wait {
			t.Fatalf("wait %v: got %v, %v", wait, got.BootstrapCapacityWait, err)
		}
	}
	data, err := json.Marshal(map[string]any{"completionMarginMS": 0, "bootstrapCapacityWaitNanos": 0})
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &input); err != nil || input.BootstrapCapacityWait == nil || *input.BootstrapCapacityWait != 0 {
		t.Fatalf("explicit JSON zero lost: %+v, %v", input, err)
	}
}

type rev76Fixture struct {
	Settings SchedulerSettings `json:"settings"`
	Cold     SchedulerSnapshot `json:"cold"`
	Post     SchedulerSnapshot `json:"post"`
	Profiles []*Profile        `json:"profiles"`
	Rows     []CapacityRow     `json:"rows"`
}

func loadRev76Fixture(t *testing.T) rev76Fixture {
	t.Helper()
	data, err := os.ReadFile("testdata/rev76-bootstrap.json")
	if err != nil {
		t.Fatal(err)
	}
	var f rev76Fixture
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	// Preserve the captured raw document; construct current-contract replay
	// inputs explicitly rather than treating old row keys as current keys.
	margin := f.Settings.CompletionMarginMS
	f.Settings, err = (SchedulerSettingsInput{SchedulerSettings: f.Settings, CompletionMarginMS: &margin, BootstrapCapacityWait: &f.Settings.BootstrapCapacityWait, ScaleDownStabilization: &f.Settings.ScaleDownStabilization, ReplacementDwell: &f.Settings.ReplacementDwell}).Settings()
	if err != nil {
		t.Fatal(err)
	}
	for index := range f.Rows {
		row := &f.Rows[index]
		row.Input.GridSteps, row.Input.BisectionWidth, row.Input.TailSamples = f.Settings.CapacityGridSteps, f.Settings.CapacityBisectionWidth, f.Settings.CapacityTailSamples
		row.Key = row.Input.Key()
	}
	profiles := map[string]*Profile{}
	for _, profile := range f.Profiles {
		profiles[profile.ID] = profile
	}
	f.Post.Workloads, f.Post.Baselines, f.Post.Costs = f.Cold.Workloads, f.Cold.Baselines, f.Cold.Costs
	for _, snapshot := range []*SchedulerSnapshot{&f.Cold, &f.Post} {
		for k := range snapshot.Options {
			i := &snapshot.Options[k].Instance
			i.Profile = profiles[i.ProfileID]
			if i.Profile == nil {
				t.Fatalf("missing raw profile %s", i.ProfileID)
			}
		}
		for k := range snapshot.Instances {
			i := &snapshot.Instances[k]
			i.Profile = profiles[i.ProfileID]
		}
	}
	return f
}

func rev76RowsAt(rows []CapacityRow, at time.Time) []CapacityRow {
	return slices.DeleteFunc(slices.Clone(rows), func(row CapacityRow) bool { return row.BuiltAt.After(at) })
}

// The recorded snapshot/cost/catalog bodies and terminal rows are preserved;
// only the stored references are hydrated, exactly as a journal reader does.
//
//nolint:gocyclo // One preserved trace covers the successive partial and joint decisions.
func TestRev76BootstrapRawReplayAC41(t *testing.T) {
	f := loadRev76Fixture(t)
	s, snapshot := f.Settings, f.Cold
	s.ScaleDownStabilization = 300 * time.Second
	firstRows := rev76RowsAt(f.Rows, snapshot.At)
	// With waiting disabled, S:861 still reproduces the original premature
	// genesis2 FP32 allocation; leximin is not a bootstrap barrier.
	original := capacityDecideForTest(t, s, snapshot, firstRows)
	if original.Target == nil || len(original.Target.Instances) != 1 || original.Target.Instances[0].Node.Name != "genesis2" {
		t.Fatalf("S:861 input no longer reproduces the first Smol allocation: %+v", original)
	}
	s.BootstrapCapacityWait = 10 * time.Minute
	snapshot.BootstrapStartedAt = snapshot.At
	for _, cutoff := range []string{"2026-10-25T20:07:20.716185712Z", "2026-10-25T20:08:50Z", "2026-10-25T20:09:24.365Z"} {
		at, err := time.Parse(time.RFC3339Nano, cutoff)
		if err != nil {
			t.Fatal(err)
		}
		// Advance decision time, not capture time: refresh observed timestamps
		// below only when replaying a later capture.
		snapshot.At = at
		refreshRev76Times(&snapshot)
		got := capacityDecideForTest(t, s, snapshot, rev76RowsAt(f.Rows, at))
		if got.Status != CapacityPending || got.Target != nil || got.Bootstrap == nil || len(got.Bootstrap.PendingRows) == 0 || len(got.Keep.Actions) != 0 {
			t.Fatalf("partial row order %s committed a target: status=%s reasons=%v bootstrap=%+v", cutoff, got.Status, got.Reasons, got.Bootstrap)
		}
		t.Logf("%s: no target/reservation; pending rows=%d deadline=%s", cutoff, len(got.Bootstrap.PendingRows), got.Bootstrap.Deadline)
	}
	// Counterfactual empty-pool capture at post-state time: no S:861 action
	// has been launched. All measured terminal rows are now available.
	snapshot.At = f.Post.At
	refreshRev76Times(&snapshot)
	got := capacityDecideForTest(t, s, snapshot, f.Rows)
	if got.Target == nil || got.Target.Keep || got.Bootstrap == nil || len(got.Bootstrap.PendingRows) != 0 {
		t.Fatalf("completed rows did not release bootstrap: status=%s reasons=%v bootstrap=%+v", got.Status, got.Reasons, got.Bootstrap)
	}
	for _, uid := range s.AutonomousWorkloads {
		if !slices.ContainsFunc(got.Capacity.Workloads, func(w SchedulerCapacityWorkload) bool { return w.UID == uid && w.KappaNew > 0 && len(w.Weights) > 0 }) {
			t.Fatalf("no positive capacity/route for %s: %+v", uid, got.Capacity.Workloads)
		}
	}
	prepares := 0
	for _, action := range got.Target.Actions {
		if action.Kind == schedulerActionPrepare {
			prepares++
		}
	}
	if prepares < 3 {
		t.Fatalf("three Workloads did not receive prepare actions: %d", prepares)
	}
	t.Logf("joint placement: prepares=%d workloads=%+v", prepares, got.Capacity.Workloads)
}

func refreshRev76Times(snapshot *SchedulerSnapshot) {
	snapshot.ObservationEnd = snapshot.At
	for k := range snapshot.Nodes {
		snapshot.Nodes[k].ObservedAt = snapshot.At
	}
	for k := range snapshot.Options {
		snapshot.Options[k].Instance.ObservedAt = snapshot.At
	}
}

func TestRev76PostStateRawReplayAC40(t *testing.T) {
	f := loadRev76Fixture(t)
	f.Settings.BootstrapCapacityWait = 10 * time.Minute
	got := capacityDecideForTest(t, f.Settings, f.Post, f.Rows)
	if got.Bootstrap != nil || got.Target == nil || got.Target.Keep {
		t.Fatalf("post-state independent expansion suppressed: status=%s reasons=%v", got.Status, got.Reasons)
	}
	const mnv = "9f7dca68-71a8-4493-bc87-413c6a98f11a"
	if !slices.ContainsFunc(got.Capacity.Workloads, func(w SchedulerCapacityWorkload) bool { return w.UID == mnv && w.KappaNew > 0 }) {
		t.Fatalf("impossible PLC hid MNV improvement: %+v", got.Capacity.Workloads)
	}
	if !slices.ContainsFunc(got.Target.Actions, func(a Action) bool { return a.Kind == schedulerActionPrepare && a.Node.Name != "genesis2" }) {
		t.Fatal("no independent MNV prepare")
	}
	t.Logf("post-state: reserved genesis2 unchanged; MNV expansion selected: %+v", got.Capacity.Workloads)
}

func TestBootstrapDeadlineAndTerminalRows(t *testing.T) {
	s, snapshot := schedulerFixture()
	snapshot.Instances = nil
	snapshot.Routing = map[string]map[string]int32{}
	snapshot.BootstrapStartedAt = snapshot.At.Add(-time.Minute)
	s.BootstrapCapacityWait = time.Minute
	got := capacityDecideForTest(t, s, snapshot, nil)
	if got.Bootstrap == nil || !got.Bootstrap.TimedOut || got.Status == CapacityPending {
		t.Fatalf("deadline did not release wait: status=%s bootstrap=%+v", got.Status, got.Bootstrap)
	}
	snapshot.BootstrapStartedAt = snapshot.At
	rows := capacityRowsForTest(s, snapshot, nil)
	for k := range rows {
		rows[k].Status, rows[k].Kappa = CapacityRejected, nil
	}
	got = capacityDecideForTest(t, s, snapshot, rows)
	if got.Bootstrap == nil || len(got.Bootstrap.PendingRows) != 0 || got.Status == CapacityPending {
		t.Fatalf("terminal rejected rows waited forever: %+v", got.Bootstrap)
	}
	// Unusable Nodes do not extend the empty-start barrier.
	for k := range snapshot.Nodes {
		snapshot.Nodes[k].Measurement = true
	}
	got = capacityDecideForTest(t, s, snapshot, nil)
	if got.Bootstrap == nil || len(got.Bootstrap.PendingRows) != 0 {
		t.Fatalf("measurement Node created pending candidates: %+v", got.Bootstrap)
	}
	// Directly exercise cancellation as well as the normal EvaluateScheduler path.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EvaluateScheduler(ctx, s, snapshot, nil); err == nil {
		t.Fatal("canceled evaluation succeeded")
	}
}
