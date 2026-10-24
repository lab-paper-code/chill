package serving

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
)

func TestLoadWindowKeepsInflowSeparateFromCompletionAndFilters(t *testing.T) {
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Writer: &observationArchive{directory: t.TempDir()}, Definitions: []string{"unrelated"}, Outcomes: []string{"completed"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := observationLog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	from := time.Now().UTC().Add(-time.Second)
	to := from.Add(time.Second)
	d := &Definition{Name: "workload", UID: "uid", Generation: 1, SLO: SLO{Percentile: 95, TargetLatency: time.Second}}
	if err = observationLog.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	appendEvent := func(id, uid, kind, outcome string, at time.Time) {
		t.Helper()
		e := RequestEvent{RequestID: id, WorkloadUID: uid, Kind: kind, Outcome: outcome, At: at}
		if kind == "validated" || kind == "accepted" {
			e.Definition = d.Identity()
		}
		if err := observationLog.RequestEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent("old", "uid", "received", "", from.Add(-time.Nanosecond))
	appendEvent("old", "uid", "response_finished", "completed", from)
	appendEvent("valid", "uid", "received", "", from)
	appendEvent("valid", "uid", "validated", "", from)
	appendEvent("valid", "uid", "accepted", "", from)
	appendEvent("valid", "uid", "assigned", "", from)
	appendEvent("valid", "uid", "response_finished", "completed", to)
	appendEvent("bad", "uid", "received", "", from.Add(time.Millisecond))
	appendEvent("bad", "uid", "response_finished", "malformed", from.Add(time.Millisecond))
	appendEvent("missing", "", "received", "", from.Add(2*time.Millisecond))
	appendEvent("at-end", "uid", "received", "", to)
	window, err := observationLog.LoadWindow(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Arrivals) != 3 {
		t.Fatalf("received population: %+v", window)
	}
	byUID := map[string]WorkloadLoad{}
	for _, load := range window.Workloads {
		byUID[load.UID] = load
	}
	if load := byUID["uid"]; load.Received != 2 || load.Accepted != 1 || load.Assigned != 1 || load.Completed != 1 || load.ReceivedRate != 2 || load.CompletionRate != 1 {
		t.Fatalf("event boundary or cohort mixed: %+v", load)
	}
	if byUID[""].Received != 1 {
		t.Fatalf("unknown UID inflow lost: %+v", window)
	}
	if a := window.Arrivals[0]; a.WorkloadUID != d.UID || a.Definition == nil || !a.Accepted || !a.Compute {
		t.Fatalf("frozen compute context lost: %+v", a)
	}
	if window.Arrivals[1].Compute || window.Arrivals[1].Outcome != "malformed" || window.Arrivals[2].Compute {
		t.Fatalf("non-compute demand included: %+v", window.Arrivals)
	}
	empty, err := observationLog.LoadWindow(to.Add(time.Second), to.Add(2*time.Second))
	if err != nil || len(empty.Arrivals) != 0 || len(empty.Workloads) != 0 {
		t.Fatalf("empty event window: %+v %v", empty, err)
	}
}

func repeated[T any](items []T, n int) []T {
	out := make([]T, 0, len(items)*n)
	for range n {
		out = append(out, items...)
	}
	return out
}

// appendObservationLine writes a raw scheduler line at clock time.
func appendObservationLine(t *testing.T, observationLog *ObservationLog, clock *observationClock, line string) {
	t.Helper()
	observationLog.mu.Lock()
	defer observationLog.mu.Unlock()
	if err := observationLog.writeLocked(observationSchedulerStream, clock.now(), []byte(line+"\n")); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerRawCursorBoundsAppendOrder(t *testing.T) {
	const laterActionID = "second"
	config := ObservationLogConfig{Directory: t.TempDir()}
	from := observationTestHour.Add(10 * time.Minute)
	to := from.Add(time.Second)
	observationLog, clock := clockedObservationLog(t, config, observationTestHour)
	clock.set(from.Add(30 * time.Second))
	for _, event := range []SchedulerObservation{
		{At: from.Add(-time.Nanosecond), Action: &SchedulerActionObservation{ID: "outside"}},
		{At: from.Add(-24 * time.Hour), Action: &SchedulerActionObservation{ID: "distant"}},
		{At: from.Add(time.Millisecond), Action: &SchedulerActionObservation{ID: "first"}},
		{At: from, Action: &SchedulerActionObservation{ID: laterActionID}},
		{At: to, Action: &SchedulerActionObservation{ID: "boundary"}},
	} {
		if err := observationLog.SchedulerRecord(event); err != nil {
			t.Fatal(err)
		}
	}
	check := func(observationLog *ObservationLog) {
		t.Helper()
		first, next, _, err := observationLog.SchedulerObservationPage(from, to, 1, 0)
		if err != nil || len(first) != 1 || first[0].Action.ID != "first" || next != 1 {
			t.Fatalf("first bounded append page: %+v %d %v", first, next, err)
		}
		second, next, _, err := observationLog.SchedulerObservationPage(from, to, 1, next)
		if err != nil || len(second) != 1 || second[0].Action.ID != laterActionID || next != -1 {
			t.Fatalf("raw cursor reordered/backfilled events: %+v %d %v", second, next, err)
		}
		end, next, _, err := observationLog.SchedulerObservationPage(from, to, 1, 2)
		if err != nil || len(end) != 0 || next != -1 {
			t.Fatalf("cursor at selected count must return an empty terminal page: %+v %d %v", end, next, err)
		}
		if _, _, _, err = observationLog.SchedulerObservationPage(from, to, 1, 3); err == nil {
			t.Fatal("out-of-range cursor accepted")
		}
	}
	check(observationLog)
	if err := observationLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored, _ := clockedObservationLog(t, observationLog.config, observationTestHour)
	check(restored)
}

func TestSchedulerRawPageDecodesOnlySelectedPayloads(t *testing.T) {
	from := observationTestHour.Add(10 * time.Minute)
	to := from.Add(time.Minute)
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	clock.set(from)
	appendSentinel := func(at time.Time) {
		t.Helper()
		// Valid JSON, but full decoding must reject action.id.
		appendObservationLine(t, observationLog, clock, fmt.Sprintf(`{"at":%q,"action":{"id":42}}`, at.Format(time.RFC3339Nano)))
	}
	appendSentinel(from.Add(-time.Nanosecond))
	appendSentinel(from)
	settings, snapshot := schedulerFixture()
	node := snapshot.Nodes[0].Node
	completed := from.Add(time.Second)
	joules := 12.5
	power := &metricobserver.Result{
		// Power rows keep their end as the record time.
		Source: operatingTestSource, StartedAt: from, EndedAt: from.Add(time.Millisecond),
		Samples:  []metricobserver.Sample{{ObservedAt: from, Watts: 12.5, RequestLatencySeconds: 0.1}},
		Failures: []metricobserver.Failure{{ObservedAt: completed, Message: "retained failure"}},
	}
	power.Summary = metricobserver.Summarize(*power)
	selected := SchedulerObservation{
		At:       from.Add(time.Millisecond),
		Decision: &SchedulerDecision{At: from, Status: "retained", Keep: SchedulerCandidate{Status: "keep"}},
		Snapshot: &snapshot,
		Settings: &SchedulerSettingsChange{ActorUsername: "operator", ActorUID: "actor", Before: settings, After: settings, ValidatedAt: from, AppliedAt: &completed, Affected: []NodeReference{node}, Error: "retained settings detail"},
		Action: &SchedulerActionObservation{
			ID: "selected", Kind: "resize", Node: node, Instance: "instance", Phase: "finished",
			StartedAt: from, CompletedAt: &completed,
			Expected: json.RawMessage(`{"resources":{"cpus":[1,2],"memory":512}}`),
			Actual:   json.RawMessage(`{"resources":{"cpus":[1],"memory":256}}`),
			Error:    "retained action detail", Evidence: []evidencestore.ObjectIdentity{{Store: "raw", Key: "action", Size: 123}},
		},
		Power: power, Node: &node, Meter: "meter", PowerScope: "node",
		PowerSampling: &metricobserver.Request{Interval: time.Second, Duration: time.Minute, RequestTimeout: time.Second, MaxEvents: 100},
		PowerCoverage: &powerwindow.Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 0.9, MaximumGap: 2 * time.Second},
		PowerJoules:   &joules, PowerError: "retained power detail",
	}
	if err := observationLog.SchedulerRecord(selected); err != nil {
		t.Fatal(err)
	}
	appendSentinel(from.Add(2 * time.Millisecond))
	// A bounded page must stop after its lookahead, not scan the remaining file.
	appendObservationLine(t, observationLog, clock, "{not-json}")
	page, next, _, err := observationLog.SchedulerObservationPage(from, to, 1, 1)
	if err != nil || len(page) != 1 || next != 2 {
		t.Fatalf("unselected payload was fully decoded or scan was not bounded: %+v %d %v", page, next, err)
	}
	// Stored records keep only the profile ID of each instance.
	stored := snapshot
	stored.Instances = slices.Clone(snapshot.Instances)
	for i := range stored.Instances {
		stored.Instances[i].ProfileID, stored.Instances[i].Profile = stored.Instances[i].Profile.ID, nil
	}
	stored.Options = slices.Clone(snapshot.Options)
	for i := range stored.Options {
		stored.Options[i].Instance.ProfileID, stored.Options[i].Instance.Profile = stored.Options[i].Instance.Profile.ID, nil
	}
	selected.Snapshot = &stored
	want, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(page[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("selected raw details lost:\n got %s\nwant %s", got, want)
	}
	for _, offset := range []int{0, next} {
		if _, _, _, err := observationLog.SchedulerObservationPage(from, to, 1, offset); err == nil {
			t.Fatalf("selected type-invalid payload accepted at offset %d", offset)
		}
	}
	if _, _, _, err := observationLog.SchedulerObservationPage(from, to, 1, 3); err == nil {
		t.Fatal("syntax error after skipped payloads was ignored")
	}
}

func TestSchedulerRawPageRejectsInvalidUnselectedLines(t *testing.T) {
	for corruption, line := range map[string]string{"syntax": "{not-json}", "time": `{"at":"not-a-time"}`, "ignored-subtree": `{"at":"2026-10-20T10:09:00Z","snapshot":{"options":-01}}`} {
		t.Run(corruption, func(t *testing.T) {
			from := observationTestHour.Add(10 * time.Minute)
			observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
			clock.set(from)
			appendObservationLine(t, observationLog, clock, line)
			if err := observationLog.SchedulerRecord(SchedulerObservation{At: from}); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := observationLog.SchedulerObservationPage(from, from.Add(time.Minute), 1, 0); err == nil {
				t.Fatal("unselected invalid record was ignored")
			}
			if observationLog.Health().Healthy {
				t.Fatal("read error was not published to observation log health")
			}
		})
	}
}

func TestSchedulerRawPagePreservesReorderedEscapedEnvelope(t *testing.T) {
	at := time.Date(2026, 10, 20, 19, 7, 1, 478738123, time.FixedZone("record-zone", 9*60*60))
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	clock.set(at)
	appendObservationLine(t, observationLog, clock, fmt.Sprintf(
		`{"unknown":{"number":9007199254740993,"text":"at"},"snapshot":{"at":%q,"instances":[]},"action":{"id":"reordered-source"},"\u0061t":%q}`,
		at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano)))
	page, next, _, err := observationLog.SchedulerObservationPage(at.Add(-time.Nanosecond), at.Add(time.Nanosecond), 1, 0)
	if err != nil || len(page) != 1 || next != -1 {
		t.Fatalf("reordered envelope rejected: %+v %d %v", page, next, err)
	}
	if !page[0].At.Equal(at) || page[0].Snapshot == nil || !page[0].Snapshot.At.Equal(at) || page[0].Action.ID != "reordered-source" {
		t.Fatalf("iterator changed event time or consumed payload: %+v", page[0])
	}
}

func TestWorkloadObservationReadsOnlyRequestedRange(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	d := &Definition{UID: "workload", Generation: 1, SLO: SLO{Percentile: 99, TargetLatency: time.Second}}
	if err := observationLog.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	request := func(id string, received time.Time, close bool) {
		t.Helper()
		clock.set(received)
		for _, e := range []RequestEvent{
			{Kind: phaseReceived, At: received},
			{Kind: phaseAccepted, At: received},
			{Kind: phaseAssigned, At: received, Assignment: &Assignment{Instance: "i1", DispatchedAt: received}},
			{Kind: eventResponseFinished, At: received.Add(time.Millisecond), ElapsedNanos: int64(time.Millisecond), Outcome: outcomeCompleted},
		} {
			e.RequestID, e.WorkloadUID, e.Definition, e.ReceivedAt = id, d.UID, d.Identity(), received
			if err := observationLog.RequestEvent(e); err != nil {
				t.Fatal(err)
			}
		}
		if close {
			if err := observationLog.RequestClosed(id); err != nil {
				t.Fatal(err)
			}
		}
	}
	for minute := range 120 {
		for second := 0; second < 60; second += 15 {
			received := observationTestHour.Add(time.Duration(minute)*time.Minute + time.Duration(second)*time.Second)
			request(fmt.Sprintf("%d-%d", minute, second), received, true)
		}
	}
	from := observationTestHour.Add(50 * time.Minute)
	to := from.Add(2 * time.Minute)
	request("late", from.Add(time.Minute), false)
	clock.set(observationTestHour.Add(119 * time.Minute))
	if err := observationLog.RequestClosed("late"); err != nil {
		t.Fatal(err)
	}
	// Request files before from are unreadable: any read fails the query.
	names, err := filepath.Glob(filepath.Join(observationLog.root, "*", "requests.*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		minute, _ := time.Parse("20060102T15/requests.04.jsonl", filepath.Base(filepath.Dir(name))+"/"+filepath.Base(name))
		if minute.Before(from) {
			if err := os.WriteFile(name, []byte("not json\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	result, err := observationLog.Query(d.UID, from, to, nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	const want = 2*time.Minute/(15*time.Second) + 1 // receipts every 15 s plus the late request
	if result.Total != int(want) || result.Outcomes[outcomeCompleted] != int(want) || len(result.Cohorts) != 1 ||
		result.Cohorts[0].SampleCount != int(want) || len(result.Requests) != int(want) {
		t.Fatalf("narrow range result: total %d outcomes %v cohorts %+v requests %d", result.Total, result.Outcomes, result.Cohorts, len(result.Requests))
	}
}

// Scale of docs/design/serving/observability-split.md §1: a 185 KB E1 decision.
//
//nolint:gocyclo // One E1-scale record checked field by field.
func TestSchedulerRecordStoresCompactDecision(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	_, snapshot := schedulerFixture()
	at := observationTestHour.Add(10 * time.Minute)
	clock.set(at)
	instance := snapshot.Instances[0]
	profile := *instance.Profile
	profile.Batches = make([]BatchProfile, 6)
	for b := range profile.Batches {
		profile.Batches[b] = BatchProfile{Batch: b + 1, DurationNanos: make([]float64, 200)}
		for s := range profile.Batches[b].DurationNanos {
			profile.Batches[b].DurationNanos[s] = 12345678.9 + float64(s)
		}
	}
	instance.Profile = &profile
	instance.WorkloadUID = "w1"
	instances := repeated([]SchedulerInstance{instance}, 9)
	nodes := repeated(snapshot.Nodes, 8)
	snapshot.Nodes, snapshot.Instances = nodes, instances
	definition := snapshot.Workloads[0]
	snapshot.Workloads = make([]*Definition, 16)
	for i := range snapshot.Workloads {
		d := *definition
		d.Generation = int64(i + 1)
		snapshot.Workloads[i] = &d
	}
	snapshot.Options = repeated(snapshot.Options[:1], 30)
	for i := range snapshot.Options {
		snapshot.Options[i].Instance = instance
	}
	baseline := snapshot.Baselines[0]
	baseline.WattsUncertainty = 0.75
	snapshot.Baselines = make([]NodeBaseline, 43)
	for i := range snapshot.Baselines {
		snapshot.Baselines[i] = baseline
		snapshot.Baselines[i].ID = fmt.Sprintf("baseline-%02d", i)
		snapshot.Baselines[i].Reference = evidencestore.ObjectIdentity{Store: "evidence", Key: fmt.Sprintf("profiling/baselines/node-%02d/idle.json", i), Size: 4096}
	}
	snapshot.Costs = make([]OperationCost, 42)
	for i := range snapshot.Costs {
		subtracted := 3.5
		snapshot.Costs[i] = OperationCost{
			ID: fmt.Sprintf("cost-%02d", i), Node: nodes[0].Node, Kind: "prepare", From: "absent", To: "ready",
			Trials:      repeated([]OperationTrial{{Duration: 1234567890, Joules: 12.345678, EnergyUncertaintyJoules: 4.5}}, 5),
			EnergyScope: "target-baseline-delta", SubtractedWatts: &subtracted, Meter: "wall", Scope: "node-exclusive-wall",
			Reference:   evidencestore.ObjectIdentity{Store: "evidence", Key: fmt.Sprintf("profiling/costs/node-a/%02d.json", i), Size: 8192},
			BeforeState: []string{"profile-a/cpu-0-3"}, TargetState: []string{"profile-a/cpu-0-3", "profile-b/cpu-4-7"}, MeasuredAt: at,
		}
	}
	snapshot.Arrivals = repeated([]SchedulerArrival{{ID: "request", At: at, Definition: definition, Accepted: true, Compute: true, WorkloadUID: "w1"}}, 3000)
	snapshot.Outstanding = repeated([]SchedulerRequest{{ID: "request", Definition: definition, ReceivedAt: at, Phase: "queued", Instance: "i1"}}, 20)
	actions := []Action{{ID: "prepare", Kind: "prepare", Node: nodes[0].Node, Instance: "i1", DependsOn: []string{}, Status: "planned"}}
	joules := 42.5
	candidate := SchedulerCandidate{
		ID: "candidate", Nodes: nodes, Instances: instances, Routing: map[string]map[string]int32{"w1": {"i1": 1}}, Actions: actions,
		Status: "evaluated", Reasons: []string{"reason"}, ExpectedJoules: &joules, WorstExceedance: 0.25,
		SLO: []SchedulerEstimate{{UID: "w1", Definition: definition.Identity(), Samples: 100, Percentile: 95}}, Assumptions: []string{"assumption"}, Sources: []string{"source"},
	}
	plans := repeated([]SchedulerNodePlan{{Node: nodes[0].Node, Key: "profile/b4/w1", Eligible: true, Kappa: map[string]float64{"w1": 34.3}, KappaRetained: map[string]float64{"w1": 0}, Pre: 20 * time.Second, Joules: 140, EnergyUncertaintyJoules: 7}}, 60)
	wait := 0.42
	capacity := &SchedulerCapacity{Period: 197 * time.Second, Route: 20 * time.Second, Search: &SchedulerPlanSearch{Neighborhood: 3, Evaluated: 25272, Duration: time.Second}, Plans: plans, GainUncertaintyJoules: 12,
		Workloads: []SchedulerCapacityWorkload{{UID: "w1", Rate: 40, KappaNew: 144, Backlog: 61, WaitSeconds: &wait, Weights: map[string]int32{"i1": 1000}, Rows: []string{"row"}}}}
	decision := SchedulerDecision{At: at, Status: "target", Keep: candidate, Target: &candidate, Capacity: capacity, EvaluationElapsed: 3 * time.Millisecond}
	event := SchedulerObservation{At: at, Decision: &decision, Snapshot: &snapshot}
	if err := observationLog.SchedulerRecord(event); err != nil {
		t.Fatal(err)
	}
	info, err := observationLog.files[observationSchedulerStream].Stat()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := observationLog.files[observationCatalogStream].Stat()
	if err != nil {
		t.Fatal(err)
	}
	var line schedulerLine
	data, err := os.ReadFile(observationLog.files[observationSchedulerStream].Name())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &line); err != nil {
		t.Fatal(err)
	}
	if target := line.Decision.Target; target == nil || target.ID != candidate.ID || !target.Keep || target.Nodes != nil || target.Status != "" {
		t.Fatalf("target equal to keep was stored in full: %+v", target)
	}
	if line.Snapshot.Workloads != nil || line.Snapshot.Costs != nil || line.Snapshot.Baselines != nil || len(line.Snapshot.CostRefs) != 42 {
		t.Fatalf("snapshot stored bodies instead of references: %+v", line.Snapshot)
	}
	// The Dashboard history route reads this page with every body filled in.
	page, _, _, err := observationLog.SchedulerObservationPage(at, at.Add(time.Second), 1, 0)
	if err != nil || len(page) != 1 {
		t.Fatalf("stored decision not readable: %+v %v", page, err)
	}
	stored, storedSnapshot := page[0].Decision, page[0].Snapshot
	// The page holds every body inline, as the v1alpha4 record did.
	before, err := json.Marshal(page[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("decision record bytes: v1alpha4 %d v1alpha5 %d (catalog rows %d once per hour)", len(before), info.Size(), catalog.Size())
	if info.Size() > 135<<10 {
		t.Fatalf("stored decision record %d bytes exceeds 135 KiB", info.Size())
	}
	if stored.EvaluationElapsed != 3*time.Millisecond || !reflect.DeepEqual(stored.Capacity, capacity) {
		t.Fatalf("decision timing or capacity: %+v", stored)
	}
	if stored.Keep.Instances[0].ProfileID != profile.ID || stored.Keep.Instances[0].Profile != nil || stored.Target.SLO == nil || len(stored.Target.Nodes) != 8 {
		t.Fatalf("keep or target lost its body: %+v", stored.Keep)
	}
	if storedSnapshot.Arrivals != nil || storedSnapshot.Outstanding[0].Definition != nil || storedSnapshot.Outstanding[0].DefinitionID != definition.Identity() || storedSnapshot.Instances[0].WorkloadUID != "w1" {
		t.Fatalf("snapshot inputs: %+v", storedSnapshot)
	}
	for name, pair := range map[string][2]any{
		"workloads": {storedSnapshot.Workloads, snapshot.Workloads}, "costs": {storedSnapshot.Costs, snapshot.Costs},
		"baselines": {storedSnapshot.Baselines, snapshot.Baselines}, "target": {stored.Target, stored.Keep},
	} {
		got, _ := json.Marshal(pair[0])
		want, _ := json.Marshal(pair[1])
		if !bytes.Equal(got, want) {
			t.Fatalf("page did not restore %s", name)
		}
	}
}

func TestSchedulerCatalogFollowsMeterChangeRestartAndHour(t *testing.T) {
	config := ObservationLogConfig{Directory: t.TempDir()}
	observationLog, clock := clockedObservationLog(t, config, observationTestHour)
	node := NodeReference{Name: "node", UID: "node-uid"}
	_, snapshot := schedulerFixture()
	record := func(observationLog *ObservationLog, at time.Time, gap time.Duration) {
		t.Helper()
		clock.set(at)
		power := &metricobserver.Result{Source: operatingTestSource, StartedAt: at.Add(-time.Second), EndedAt: at, Samples: []metricobserver.Sample{{ObservedAt: at, Watts: 10}}}
		policy := powerwindow.Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 1, MaximumGap: gap}
		if err := observationLog.SchedulerRecord(SchedulerObservation{At: at, Power: power, Node: &node, Meter: "wall", PowerScope: "node-exclusive-wall", PowerCoverage: &policy}); err != nil {
			t.Fatal(err)
		}
		s := snapshot
		s.At = at
		if err := observationLog.SchedulerRecord(SchedulerObservation{At: at, Snapshot: &s}); err != nil {
			t.Fatal(err)
		}
	}
	first, second := observationTestHour.Add(10*time.Minute), observationTestHour.Add(20*time.Minute)
	record(observationLog, first, time.Second)
	record(observationLog, second, 2*time.Second) // The meter configuration changed.
	page := func(observationLog *ObservationLog) []SchedulerObservation {
		t.Helper()
		records, _, _, err := observationLog.SchedulerObservationPage(observationTestHour, observationTestHour.Add(time.Hour), 10, 0)
		if err != nil || len(records) < 4 {
			t.Fatalf("page: %+v %v", records, err)
		}
		return records
	}
	want := page(observationLog)
	if want[0].PowerCoverage.MaximumGap != time.Second || want[2].PowerCoverage.MaximumGap != 2*time.Second || want[0].Power.Source.Endpoint != operatingTestSource.Endpoint {
		t.Fatalf("meter body did not follow its change: %+v %+v", want[0], want[2])
	}
	if err := observationLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored, restoredClock := clockedObservationLog(t, observationLog.config, observationTestHour)
	clock = restoredClock
	record(restored, observationTestHour.Add(30*time.Minute), 2*time.Second) // Rewrites the same body.
	if got := page(restored)[:4]; !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog rewrite after restart changed earlier records")
	}
	// Each hour directory's catalog alone resolves that hour's references.
	record(restored, observationTestHour.Add(time.Hour+time.Minute), 2*time.Second)
	keys := map[string]bool{}
	names, err := filepath.Glob(filepath.Join(restored.root, "20261020T11", "catalog.*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
			var row CatalogRow
			if err := json.Unmarshal(line, &row); err != nil {
				t.Fatal(err)
			}
			keys[row.Kind+"/"+row.Key] = true
		}
	}
	for _, key := range []string{"meter/wall", "definition/" + snapshot.Workloads[0].Identity(), "profile/" + snapshot.Instances[0].Profile.ID, "baseline/" + baselineRef(snapshot.Baselines[0])} {
		if !keys[key] {
			t.Fatalf("hour catalog lacks %s: %v", key, keys)
		}
	}
}

func TestOperatingPowerAcrossMinuteBoundaryKeepsJoules(t *testing.T) {
	node := NodeReference{Name: "node", UID: "node-uid"}
	meters := []OperatingPowerMeter{{Node: node, Meter: "wall", Scope: "node-exclusive-wall", Source: operatingTestSource}}
	from := observationTestHour.Add(10*time.Minute - 5*time.Second)
	to := from.Add(10 * time.Second)
	measure := func(split bool) OperatingPowerWindow {
		t.Helper()
		observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
		clock.set(from.Add(-2 * time.Second))
		snapshot := SchedulerSnapshot{At: from.Add(-time.Second), Instances: []SchedulerInstance{{ID: "ready", Node: node, Ready: true, WorkloadUID: "w"}}}
		if err := observationLog.SchedulerRecord(SchedulerObservation{At: snapshot.At, Snapshot: &snapshot}); err != nil {
			t.Fatal(err)
		}
		policy := powerwindow.Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 1, MaximumGap: 2 * time.Second}
		for chunk := range 4 {
			start := from.Add(time.Duration(chunk*3-1) * time.Second)
			power := &metricobserver.Result{Source: operatingTestSource, StartedAt: start, EndedAt: start.Add(3 * time.Second)}
			for s := range 3 {
				at := start.Add(time.Duration(s) * time.Second)
				power.Samples = append(power.Samples, metricobserver.Sample{ObservedAt: at, Watts: 10 + float64(at.Sub(from)/time.Second)})
			}
			if split {
				clock.set(power.EndedAt)
			}
			if err := observationLog.SchedulerRecord(SchedulerObservation{At: power.EndedAt, Power: power, Node: &node, Meter: "wall", PowerScope: "node-exclusive-wall", PowerCoverage: &policy}); err != nil {
				t.Fatal(err)
			}
		}
		result, err := observationLog.OperatingPower(from, to, meters, 100)
		if err != nil || result.WholePoolJoules == nil {
			t.Fatalf("power: %+v %v", result, err)
		}
		return result
	}
	whole, split := measure(false), measure(true)
	if *whole.WholePoolJoules != *split.WholePoolJoules || *whole.ActivePoolJoules != *split.ActivePoolJoules {
		t.Fatalf("minute files changed energy: %v %v", *whole.WholePoolJoules, *split.WholePoolJoules)
	}
}
