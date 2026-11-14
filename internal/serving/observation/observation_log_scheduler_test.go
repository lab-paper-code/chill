package observation

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/test/fixtures"
)

func TestLoadWindowKeepsInflowSeparateFromCompletionAndFilters(t *testing.T) {
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: &observationArchive{directory: t.TempDir()}, Definitions: []string{"unrelated"}, Outcomes: []string{"completed"}})
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
	d := &serving.Definition{Name: "workload", UID: "uid", Generation: 1, SLO: serving.SLO{Percentile: 95, TargetLatency: time.Second}}
	if err = observationLog.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	appendEvent := func(id, uid, kind, outcome string, at time.Time) {
		t.Helper()
		e := serving.RequestEvent{RequestID: id, WorkloadUID: uid, Kind: kind, Outcome: outcome, At: at}
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
	window, err := observationLog.LoadWindow(context.Background(), from, to)
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
	empty, err := observationLog.LoadWindow(context.Background(), to.Add(time.Second), to.Add(2*time.Second))
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
	if err := observationLog.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	observationLog.writerMu.Lock()
	defer observationLog.writerMu.Unlock()
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
		first, next, _, _, err := observationLog.SchedulerObservationPage(context.Background(), from, to, 1, 0, SchedulerObservationSelection{})
		if err != nil || len(first) != 1 || first[0].Action.ID != "first" || next != 1 {
			t.Fatalf("first bounded append page: %+v %d %v", first, next, err)
		}
		second, next, _, _, err := observationLog.SchedulerObservationPage(context.Background(), from, to, 1, next, SchedulerObservationSelection{})
		if err != nil || len(second) != 1 || second[0].Action.ID != laterActionID || next != -1 {
			t.Fatalf("raw cursor reordered/backfilled events: %+v %d %v", second, next, err)
		}
		end, next, _, _, err := observationLog.SchedulerObservationPage(context.Background(), from, to, 1, 2, SchedulerObservationSelection{})
		if err != nil || len(end) != 0 || next != -1 {
			t.Fatalf("cursor at selected count must return an empty terminal page: %+v %d %v", end, next, err)
		}
		if _, _, _, _, err = observationLog.SchedulerObservationPage(context.Background(), from, to, 1, 3, SchedulerObservationSelection{}); err == nil {
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

func TestSchedulerDefaultSelectionsKeepBoundedReadAndOffsets(t *testing.T) {
	from := observationTestHour.Add(10 * time.Minute)
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	clock.set(from)
	for i := range 3 {
		if err := log.SchedulerRecord(SchedulerObservation{At: from, Action: &SchedulerActionObservation{ID: fmt.Sprint(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	appendObservationLine(t, log, clock, "{not-json}")
	for _, selection := range []SchedulerObservationSelection{{}, {Filter: "all"}, {Order: "asc"}, {Order: "asc", Filter: "all"}} {
		page, next, _, _, err := log.SchedulerObservationPage(context.Background(), from, from.Add(time.Minute), 1, 1, selection)
		if err != nil || len(page) != 1 || next != 2 {
			t.Fatalf("default selection %+v was not bounded: %+v %d %v", selection, page, next, err)
		}
		if page[0].Offset == nil || *page[0].Offset != 1 {
			t.Fatalf("default selection %+v lost original offset: %+v", selection, page[0])
		}
	}
}

func TestSchedulerSummaryPreservesRecordedWorkloadStates(t *testing.T) {
	from := observationTestHour.Add(10 * time.Minute)
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	clock.set(from)
	workloads := []serving.SchedulerWorkloadState{
		{UID: "fixed", State: "Fixed", Reason: "MeasurementEvidenceMissing"},
		{UID: "controlled", State: "Controlled"},
	}
	decision := serving.SchedulerDecision{At: from, Status: "Keep", Workloads: workloads}
	if err := log.SchedulerRecord(SchedulerObservation{At: from, Decision: &decision}); err != nil {
		t.Fatal(err)
	}
	for _, selection := range []SchedulerObservationSelection{{Summary: true}, {Summary: true, Order: "desc"}} {
		page, _, _, _, err := log.SchedulerObservationPage(context.Background(), from, from.Add(time.Minute), 10, 0, selection)
		if err != nil || len(page) != 1 || page[0].Decision == nil {
			t.Fatalf("stored summary decision missing: page=%+v err=%v", page, err)
		}
		if got := page[0].Decision.Workloads; !reflect.DeepEqual(got, workloads) {
			t.Fatalf("stored summary workload states: got %+v, want %+v", got, workloads)
		}
	}
}

func TestSchedulerSummarySkipsUnselectedEvidenceAndCatalog(t *testing.T) {
	from := observationTestHour.Add(10 * time.Minute)
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	clock.set(from)
	// The missing catalog and incompatible detailed field must not affect a list.
	appendObservationLine(t, log, clock, fmt.Sprintf(`{"at":%q,"decision":{"at":%q,"status":"Keep","workloads":[{"uid":"w"}]},"snapshot":{"at":%q,"workloadIDs":["w/1"],"instances":[{"observedAt":{}}]}}`, from.Format(time.RFC3339Nano), from.Format(time.RFC3339Nano), from.Format(time.RFC3339Nano)))
	appendObservationLine(t, log, clock, fmt.Sprintf(`{"at":%q,"node":{"uid":"n","name":"node"},"meter":"missing-meter","startedAt":%q,"powerJoules":12.5,"samples":[{"watts":"detail-only"}]}`, from.Add(time.Second).Format(time.RFC3339Nano), from.Format(time.RFC3339Nano)))
	for _, selection := range []SchedulerObservationSelection{
		{Summary: true},
		{Summary: true, Order: "desc"},
		{Summary: true, Order: "desc", Filter: "decision-action", UID: "w"},
	} {
		page, next, _, _, err := log.SchedulerObservationPage(context.Background(), from, from.Add(time.Minute), 10, 0, selection)
		if err != nil || next != -1 || len(page) == 0 {
			t.Fatalf("summary decoded detail or resolved catalog: selection=%+v page=%+v next=%d err=%v", selection, page, next, err)
		}
		for _, record := range page {
			if record.Offset == nil {
				t.Fatal("summary lost original append offset")
			}
			if record.Decision != nil && (record.Decision.Status != "Keep" || len(record.Decision.Workloads) != 1 || record.Decision.Workloads[0].UID != "w" || record.Snapshot == nil || len(record.Snapshot.Instances) != 0 || len(record.Snapshot.WorkloadIDs) != 0) {
				t.Fatalf("summary lost identity or retained evidence: %+v", record)
			}
			if record.Power != nil && (record.PowerJoules == nil || *record.PowerJoules != 12.5 || len(record.Power.Samples) != 0 || !record.Power.StartedAt.Equal(from)) {
				t.Fatalf("power summary lost measured scalar or retained samples: %+v", record)
			}
		}
	}
	if _, _, _, _, err := log.SchedulerObservationPage(context.Background(), from, from.Add(time.Minute), 1, 0, SchedulerObservationSelection{}); err == nil {
		t.Fatal("original detail stopped validating stored evidence")
	}
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
		Decision: &serving.SchedulerDecision{At: from, Status: "retained", Keep: serving.SchedulerCandidate{Status: "keep"}},
		Snapshot: &snapshot,
		Settings: &SchedulerSettingsChange{ActorUsername: "operator", ActorUID: "actor", Before: settings, After: settings, ValidatedAt: from, AppliedAt: &completed, Affected: []serving.NodeReference{node}, Error: "retained settings detail"},
		Action: &SchedulerActionObservation{
			ID: "selected", Kind: "resize", Node: node, Instance: "instance", Phase: "finished",
			StartedAt: from, CompletedAt: &completed,
			Expected: json.RawMessage(`{"resources":{"cpus":[1,2],"memory":512}}`),
			Actual:   json.RawMessage(`{"resources":{"cpus":[1],"memory":256}}`),
			Error:    "retained action detail", Evidence: []evidencestore.ObjectIdentity{{Store: "raw", Key: "action", Size: 123}},
		},
		Power: power, Node: &node, Meter: "meter", PowerScope: "node",
		PowerSampling: &metricobserver.Request{Interval: time.Second, Duration: time.Minute, RequestTimeout: time.Second},
		PowerCoverage: &powerwindow.Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 0.9, MaximumGap: 2 * time.Second},
		PowerJoules:   &joules, PowerError: "retained power detail",
	}
	if err := observationLog.SchedulerRecord(selected); err != nil {
		t.Fatal(err)
	}
	appendSentinel(from.Add(2 * time.Millisecond))
	// A bounded page must stop after its lookahead, not scan the remaining file.
	appendObservationLine(t, observationLog, clock, "{not-json}")
	page, next, _, _, err := observationLog.SchedulerObservationPage(context.Background(), from, to, 1, 1, SchedulerObservationSelection{})
	if err != nil || len(page) != 1 || next != 2 {
		t.Fatalf("unselected payload was fully decoded or scan was not bounded: %+v %d %v", page, next, err)
	}
	// Stored records keep only the profile ID of each instance.
	stored := snapshot
	stored.DispatchOccupancy = serving.SchedulerSelectedOccupancy(selected.Decision.Settings, snapshot)
	stored.Instances = slices.Clone(snapshot.Instances)
	for i := range stored.Instances {
		stored.Instances[i].ProfileID, stored.Instances[i].Profile = stored.Instances[i].Profile.ID, nil
	}
	stored.Options = slices.Clone(snapshot.Options)
	for i := range stored.Options {
		stored.Options[i].Instance.ProfileID, stored.Options[i].Instance.Profile = stored.Options[i].Instance.Profile.ID, nil
	}
	selected.Snapshot = &stored
	selected.Offset = page[0].Offset
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
		if _, _, _, _, err := observationLog.SchedulerObservationPage(context.Background(), from, to, 1, offset, SchedulerObservationSelection{}); err == nil {
			t.Fatalf("selected type-invalid payload accepted at offset %d", offset)
		}
	}
	if _, _, _, _, err := observationLog.SchedulerObservationPage(context.Background(), from, to, 1, 3, SchedulerObservationSelection{}); err == nil {
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
			if _, _, _, _, err := observationLog.SchedulerObservationPage(context.Background(), from, from.Add(time.Minute), 1, 0, SchedulerObservationSelection{}); err == nil {
				t.Fatal("unselected invalid record was ignored")
			}
			if !observationLog.Health().Healthy {
				t.Fatal("read-only corruption changed append health")
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
	page, next, _, _, err := observationLog.SchedulerObservationPage(context.Background(), at.Add(-time.Nanosecond), at.Add(time.Nanosecond), 1, 0, SchedulerObservationSelection{})
	if err != nil || len(page) != 1 || next != -1 {
		t.Fatalf("reordered envelope rejected: %+v %d %v", page, next, err)
	}
	if !page[0].At.Equal(at) || page[0].Snapshot == nil || !page[0].Snapshot.At.Equal(at) || page[0].Action.ID != "reordered-source" {
		t.Fatalf("iterator changed event time or consumed payload: %+v", page[0])
	}
}

func TestWorkloadObservationReadsOnlyRequestedRange(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	d := &serving.Definition{UID: "workload", Generation: 1, SLO: serving.SLO{Percentile: 99, TargetLatency: time.Second}}
	if err := observationLog.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	request := func(id string, received time.Time, close bool) {
		t.Helper()
		clock.set(received)
		for _, e := range []serving.RequestEvent{
			{Kind: "received", At: received},
			{Kind: "accepted", At: received},
			{Kind: "assigned", At: received, Assignment: &serving.Assignment{Instance: "i1", DispatchedAt: received}},
			{Kind: "response_finished", At: received.Add(time.Millisecond), ElapsedNanos: int64(time.Millisecond), Outcome: "completed"},
		} {
			e.RequestID, e.WorkloadUID, e.Definition, e.ReceivedAt = id, d.UID, d.Identity(), received
			if err := observationLog.RequestEvent(e); err != nil {
				t.Fatal(err)
			}
		}
		if close {
			if err := observationLog.RequestClosed(id, serving.RequestTimings{}); err != nil {
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
	if err := observationLog.RequestClosed("late", serving.RequestTimings{}); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.Drain(context.Background()); err != nil {
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
	result, err := observationLog.Query(context.Background(), d.UID, from, to, nil, nil, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	const want = 2*time.Minute/(15*time.Second) + 1 // receipts every 15 s plus the late request
	if result.Total != int(want) || result.Outcomes["completed"] != int(want) || len(result.Cohorts) != 1 ||
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
	profile.Batches = make([]serving.BatchProfile, 6)
	for b := range profile.Batches {
		profile.Batches[b] = serving.BatchProfile{Batch: b + 1, ServiceSamples: make([]firstfloor.ServiceSample, 200)}
		for s := range profile.Batches[b].ServiceSamples {
			profile.Batches[b].ServiceSamples[s] = fixtures.ServiceSample("physical-trial", uint64(s), 12345679+int64(s))
		}
	}
	instance.Profile = &profile
	instance.WorkloadUID = "w1"
	instances := repeated([]serving.SchedulerInstance{instance}, 9)
	nodes := repeated(snapshot.Nodes, 8)
	snapshot.Nodes, snapshot.Instances = nodes, instances
	definition := snapshot.Workloads[0]
	snapshot.Workloads = make([]*serving.Definition, 16)
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
	snapshot.Baselines = make([]serving.NodeBaseline, 43)
	for i := range snapshot.Baselines {
		snapshot.Baselines[i] = baseline
		snapshot.Baselines[i].ID = fmt.Sprintf("baseline-%02d", i)
		snapshot.Baselines[i].Reference = evidencestore.ObjectIdentity{Store: "evidence", Key: fmt.Sprintf("profiling/baselines/node-%02d/idle.json", i), Size: 4096}
	}
	snapshot.Costs = make([]serving.OperationCost, 42)
	for i := range snapshot.Costs {
		subtracted := 3.5
		snapshot.Costs[i] = serving.OperationCost{
			ID: fmt.Sprintf("cost-%02d", i), Node: nodes[0].Node, Kind: "prepare", From: "absent", To: "ready",
			Trials:      repeated([]serving.OperationTrial{{Duration: 1234567890, Joules: 12.345678, EnergyUncertaintyJoules: 4.5}}, 5),
			EnergyScope: "target-baseline-delta", SubtractedWatts: &subtracted, Meter: "wall", Scope: "node-exclusive-wall",
			Reference:   evidencestore.ObjectIdentity{Store: "evidence", Key: fmt.Sprintf("profiling/costs/node-a/%02d.json", i), Size: 8192},
			BeforeState: []string{"profile-a/cpu-0-3"}, TargetState: []string{"profile-a/cpu-0-3", "profile-b/cpu-4-7"}, MeasuredAt: at,
		}
	}
	snapshot.Arrivals = repeated([]serving.SchedulerArrival{{ID: "request", At: at, Definition: definition, Accepted: true, Compute: true, WorkloadUID: "w1"}}, 3000)
	snapshot.Outstanding = repeated([]serving.SchedulerRequest{{ID: "request", Definition: definition, ReceivedAt: at, Phase: "queued", Instance: "i1"}}, 20)
	actions := []serving.Action{{ID: "prepare", Kind: "prepare", Node: nodes[0].Node, Instance: "i1", DependsOn: []string{}, Status: "planned"}}
	joules := 42.5
	candidate := serving.SchedulerCandidate{
		ID: "candidate", Nodes: nodes, Instances: instances, Routing: map[string]map[string]int32{"w1": {"i1": 1}}, Actions: actions,
		Status: "evaluated", Reasons: []string{"reason"}, ExpectedJoules: &joules, WorstExceedance: 0.25,
		SLO: []serving.SchedulerEstimate{{UID: "w1", Definition: definition.Identity(), Samples: 100, Percentile: 95}}, Assumptions: []string{"assumption"}, Sources: []string{"source"},
	}
	plans := repeated([]serving.SchedulerNodePlan{{Node: nodes[0].Node, Key: "profile/b4/w1", Eligible: true, RoutingRate: map[string]float64{"w1": 34.3}, RetainedRoutingRate: map[string]float64{"w1": 0}, Pre: 20 * time.Second, Joules: 140, EnergyUncertaintyJoules: 7}}, 60)
	capacity := &serving.SchedulerCapacity{Period: 197 * time.Second, Route: 20 * time.Second, Search: &serving.SchedulerPlanSearch{Neighborhood: 3, Evaluated: 25272, Duration: time.Second}, Plans: plans, GainUncertaintyJoules: 12,
		Workloads: []serving.SchedulerCapacityWorkload{{UID: "w1", Rate: 40, RoutingRate: 144, Queued: 61, Weights: map[string]int32{"i1": 1000}}}}
	decision := serving.SchedulerDecision{At: at, Status: "target", Keep: candidate, Target: &candidate, Capacity: capacity, EvaluationElapsed: 3 * time.Millisecond}
	event := SchedulerObservation{At: at, Decision: &decision, Snapshot: &snapshot}
	if err := observationLog.SchedulerRecord(event); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.Drain(context.Background()); err != nil {
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
	page, _, _, _, err := observationLog.SchedulerObservationPage(context.Background(), at, at.Add(time.Second), 1, 0, SchedulerObservationSelection{})
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
	node := serving.NodeReference{Name: "node", UID: "node-uid"}
	_, snapshot := schedulerFixture()
	snapshot.Instances[0].Profile.Batches[0].InputSlopeJoules = observationTestSlope(`{"status":"Identified","value":0}`)
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
		records, _, _, _, err := observationLog.SchedulerObservationPage(context.Background(), observationTestHour, observationTestHour.Add(time.Hour), 10, 0, SchedulerObservationSelection{})
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
	if err := restored.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
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
			if row.Kind == observationProfileCatalog {
				var profile serving.Profile
				if err := json.Unmarshal(row.Body, &profile); err != nil {
					t.Fatal(err)
				}
				if slope, ok := profile.Batches[0].InputSlopeJoules.Value(); !ok || slope != 0 {
					t.Fatal("persisted profile lost its identified zero input slope after restart/hour change")
				}
			}
		}
	}
	for _, key := range []string{"meter/wall", "definition/" + snapshot.Workloads[0].Identity(), "profile/" + snapshot.Instances[0].Profile.ID, "baseline/" + baselineRef(snapshot.Baselines[0])} {
		if !keys[key] {
			t.Fatalf("hour catalog lacks %s: %v", key, keys)
		}
	}
}

func TestOperatingPowerAcrossMinuteBoundaryKeepsJoules(t *testing.T) {
	node := serving.NodeReference{Name: "node", UID: "node-uid"}
	meters := []OperatingPowerMeter{{Node: node, Meter: "wall", Scope: "node-exclusive-wall", Source: operatingTestSource}}
	from := observationTestHour.Add(10*time.Minute - 5*time.Second)
	to := from.Add(10 * time.Second)
	measure := func(split bool) OperatingPowerWindow {
		t.Helper()
		observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
		clock.set(from.Add(-2 * time.Second))
		snapshot := serving.SchedulerSnapshot{At: from.Add(-time.Second), Instances: []serving.SchedulerInstance{{ID: "ready", Node: node, Ready: true, WorkloadUID: "w"}}}
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
		result, err := observationLog.OperatingPower(context.Background(), from, to, meters, 100)
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

func TestSchedulerSelectedPagesPreserveRawReferences(t *testing.T) {
	from, to := observationTestHour, observationTestHour.Add(time.Minute)
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, from)
	clock.set(from.Add(30 * time.Second))
	uid := "workload"
	for _, event := range []SchedulerObservation{
		{At: from.Add(-time.Nanosecond), Action: &SchedulerActionObservation{ID: "outside"}},
		{At: from.Add(3 * time.Second), Action: &SchedulerActionObservation{ID: "newer", Expected: json.RawMessage(`{"workloadUID":"workload"}`)}},
		{At: from, Snapshot: &serving.SchedulerSnapshot{Workloads: []*serving.Definition{{UID: uid, Generation: 1}}}},
		{At: from.Add(time.Second), Decision: &serving.SchedulerDecision{Status: "older", Workloads: []serving.SchedulerWorkloadState{{UID: uid}}}},
		{At: from.Add(3 * time.Second), Action: &SchedulerActionObservation{ID: "tie", Expected: json.RawMessage(`{"workloadUIDs":["workload"]}`)}},
		{At: from.Add(4 * time.Second), Action: &SchedulerActionObservation{ID: "unrelated", Expected: json.RawMessage(`{"workloadUID":"other","target":{"routing":{"workload":{}}}}`)}},
		{At: from.Add(5 * time.Second)},
		{At: from.Add(6 * time.Second), Power: &metricobserver.Result{Source: operatingTestSource, StartedAt: from, EndedAt: from.Add(6 * time.Second)}, Node: &serving.NodeReference{Name: "node", UID: "node-uid"}, Meter: "wall"},
		{At: to, Action: &SchedulerActionObservation{ID: "boundary"}},
	} {
		if err := log.SchedulerRecord(event); err != nil {
			t.Fatal(err)
		}
	}
	selection := SchedulerObservationSelection{Order: "desc", Filter: "decision-action", UID: uid}
	wantOffsets := []int{3, 0, 2}
	cursor := 0
	for i, original := range wantOffsets {
		page, next, _, _, err := log.SchedulerObservationPage(context.Background(), from, to, 1, cursor, selection)
		if err != nil || len(page) != 1 || page[0].Offset == nil || *page[0].Offset != original {
			t.Fatalf("selected page %d: %+v next=%d err=%v", i, page, next, err)
		}
		raw, _, _, _, err := log.SchedulerObservationPage(context.Background(), from, to, 1, original, SchedulerObservationSelection{})
		selected := page[0]
		if err != nil || len(raw) != 1 || !reflect.DeepEqual(selected, raw[0]) {
			t.Fatalf("original detail reference changed: %+v %+v %v", selected, raw, err)
		}
		if i < len(wantOffsets)-1 && next != i+1 || i == len(wantOffsets)-1 && next != -1 {
			t.Fatalf("inconsistent cursor %d: %d", i, next)
		}
		cursor = next
	}
	for _, offset := range []int{len(wantOffsets), len(wantOffsets) + 1} {
		page, next, _, _, err := log.SchedulerObservationPage(context.Background(), from, to, 1, offset, selection)
		if offset == len(wantOffsets) && (err != nil || len(page) != 0 || next != -1) || offset > len(wantOffsets) && err == nil {
			t.Fatalf("selected cursor boundary: page=%+v next=%d err=%v", page, next, err)
		}
	}
	for _, tc := range []struct {
		selection SchedulerObservationSelection
		offsets   []int
	}{
		{SchedulerObservationSelection{Order: "asc", Filter: "decision-action", UID: uid}, []int{0, 2, 3}},
		{SchedulerObservationSelection{Filter: "snapshot", UID: uid}, []int{1}},
		{SchedulerObservationSelection{Filter: schedulerHistoryAction}, []int{0, 3, 4}},
		{SchedulerObservationSelection{Filter: schedulerHistoryOther}, []int{5}},
		{SchedulerObservationSelection{Filter: "power"}, []int{6}},
		{SchedulerObservationSelection{UID: "missing"}, []int{}},
	} {
		page, next, _, _, err := log.SchedulerObservationPage(context.Background(), from, to, 10, 0, tc.selection)
		got := make([]int, 0, len(page))
		for _, event := range page {
			got = append(got, *event.Offset)
		}
		if err != nil || next != -1 || !reflect.DeepEqual(got, tc.offsets) {
			t.Fatalf("selection %+v: offsets=%v next=%d err=%v", tc.selection, got, next, err)
		}
	}
}

func TestSchedulerObservationWorkloadRelationships(t *testing.T) {
	for _, raw := range []string{
		`{"decision":{"workloads":[{"uid":"w"}]}}`,
		`{"decision":{"keep":{"instances":[{"ownerWorkloadUID":"w"}]}}}`,
		`{"snapshot":{"workloadIDs":["w/1/model/1/preprocessor/1"]}}`,
		`{"settings":{"after":{"autonomousWorkloads":["w"]}}}`,
		`{"settings":{"beforeStaticRouting":{"w":{}}}}`,
		`{"action":{"expected":{"workloadUID":"w"}}}`,
		`{"action":{"expected":{"workloadUIDs":["w"]}}}`,
		`{"action":{"expected":{"prepare":{"owner":{"uid":"w"}}}}}`,
		`{"action":{"expected":{"prepare":{"instance":{"workloadUID":"w"}}}}}`,
		`{"action":{"expected":{"target":{"routing":{"w":{}}}}}}`,
	} {
		match, err := schedulerObservationHasWorkload([]byte(raw), "w")
		if err != nil || !match {
			t.Fatalf("workload relationship lost: %s %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"snapshot":{"workloadIDs":["wrong/1/model/1/preprocessor/1"]}}`,
		`{"action":{"node":{"uid":"w"},"error":"w","expected":{"workloadUID":"other","target":{"routing":{"w":{}}}}}}`,
		`{"action":{"expected":{"workloadUIDs":[],"target":{"routing":{"w":{}}}}}}`,
		`{"node":{"uid":"w"},"meter":"w"}`,
	} {
		match, err := schedulerObservationHasWorkload([]byte(raw), "w")
		if err != nil || match {
			t.Fatalf("unrelated workload matched: %s %v", raw, err)
		}
	}
}

func TestSchedulerFilteredSummaryStopsAfterPageLookahead(t *testing.T) {
	from := observationTestHour
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, from)
	for _, raw := range []string{
		fmt.Sprintf(`{"at":%q,"snapshot":{}}`, from.Format(time.RFC3339Nano)),
		fmt.Sprintf(`{"at":%q,"action":{"id":"first","expected":{"workloadUID":"w"}}}`, from.Add(time.Second).Format(time.RFC3339Nano)),
		fmt.Sprintf(`{"at":%q,"action":{"id":"lookahead","expected":{"workloadUID":"w"}}}`, from.Add(2*time.Second).Format(time.RFC3339Nano)),
		`{broken-after-page}`,
	} {
		appendObservationLine(t, log, clock, raw)
	}
	for _, order := range []string{"", "asc"} {
		page, next, _, _, err := log.SchedulerObservationPage(context.Background(), from, from.Add(time.Minute), 1, 0, SchedulerObservationSelection{Summary: true, Filter: "decision-action", Order: order, UID: "w"})
		if err != nil || len(page) != 1 || page[0].Action.ID != "first" || *page[0].Offset != 1 || next != 1 {
			t.Fatalf("filtered summary read past lookahead: order=%q page=%+v next=%d err=%v", order, page, next, err)
		}
	}
}

func TestSchedulerDescendingSummaryDecodesOnlySelectedPage(t *testing.T) {
	from := observationTestHour
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, from)
	for _, raw := range []string{
		fmt.Sprintf(`{"at":%q,"decision":{"status":123}}`, from.Format(time.RFC3339Nano)),
		fmt.Sprintf(`{"at":%q,"decision":{"status":123}}`, from.Add(2*time.Second).Format(time.RFC3339Nano)),
		fmt.Sprintf(`{"at":%q,"decision":{"status":"selected"}}`, from.Add(2*time.Second).Format(time.RFC3339Nano)),
		fmt.Sprintf(`{"at":%q,"decision":{"status":123}}`, from.Add(time.Second).Format(time.RFC3339Nano)),
	} {
		appendObservationLine(t, log, clock, raw)
	}
	selection := SchedulerObservationSelection{Summary: true, Filter: "decision-action", Order: "desc"}
	page, next, _, _, err := log.SchedulerObservationPage(context.Background(), from, from.Add(time.Minute), 1, 0, selection)
	if err != nil || len(page) != 1 || page[0].Decision.Status != "selected" || *page[0].Offset != 2 || next != 1 {
		t.Fatalf("summary decoded an unselected body or lost timestamp/offset ties: page=%+v next=%d err=%v", page, next, err)
	}
	if _, _, _, _, err := log.SchedulerObservationPage(context.Background(), from, from.Add(time.Minute), 1, 1, selection); err == nil {
		t.Fatal("selected summary stopped validating its own fields")
	}
}

func BenchmarkSchedulerFilteredSummaryArchive(b *testing.B) {
	// Synthetic stored-line scale: 4,000 rows near the existing 135 KiB
	// compact-record ceiling, not catalog-expanded HTTP response bytes.
	const rows = 4000
	from := observationTestHour
	archive := &readableObservationArchive{observationArchive: &observationArchive{directory: b.TempDir()}}
	key := observationArchivePrefix + observationHour(from) + "/" + observationSchedulerStream + ".jsonl.gz"
	name := filepath.Join(archive.directory, key)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		b.Fatal(err)
	}
	file, err := os.Create(name)
	if err != nil {
		b.Fatal(err)
	}
	writer := gzip.NewWriter(file)
	body := strings.TrimSuffix(strings.Repeat(`{"samples":[1,2,3,4]},`, 6400), ",")
	var rawBytes int64
	for i := range rows {
		at := from.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano)
		n, err := fmt.Fprintf(writer, "{\"at\":%q,\"decision\":{\"at\":%q,\"status\":\"Keep\",\"keep\":{\"evidence\":[%s]}}}\n", at, at, body)
		if err != nil {
			b.Fatal(err)
		}
		rawBytes += int64(n)
	}
	if err := writer.Close(); err != nil {
		b.Fatal(err)
	}
	if err := file.Close(); err != nil {
		b.Fatal(err)
	}
	log, err := NewObservationLog(ObservationLogConfig{Directory: b.TempDir(), Store: archiveTestStore, Writer: archive, Retention: 2 * time.Hour})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = log.Close(context.Background()) })
	for _, order := range []string{"asc", "desc"} {
		b.Run(order, func(b *testing.B) {
			selection := SchedulerObservationSelection{Summary: true, Filter: "decision-action", Order: order}
			b.ReportMetric(float64(rawBytes), "fixture-B")
			b.ReportAllocs()
			for range b.N {
				opened := archive.opens
				page, next, _, _, err := log.SchedulerObservationPage(context.Background(), from, from.Add(time.Minute), 50, 0, selection)
				if err != nil || len(page) != 50 || next != 50 {
					b.Fatalf("page=%d next=%d err=%v", len(page), next, err)
				}
				b.ReportMetric(float64(archive.opens-opened), "archive-opens/op")
			}
		})
	}
}

func TestSchedulerSummaryTimestampPrefilterPreservesSelection(t *testing.T) {
	from := observationTestHour.Add(time.Minute)
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, from)
	for _, raw := range []string{
		fmt.Sprintf(`{"at":%q,"snapshot":{"unused":%q}}`, from.Add(-time.Second).Format(time.RFC3339Nano), strings.Repeat("x", 1<<20)),
		fmt.Sprintf(`{"at":%q,"decision":{"status":"first"}}`, from.Format(time.RFC3339Nano)),
		fmt.Sprintf(`{"decision":{"status":"second"},"at":%q}`, from.Add(time.Second).Format(time.RFC3339Nano)),
		fmt.Sprintf(`{"at":%q,"decision":{"status":"excluded"}}`, from.Add(2*time.Second).Format(time.RFC3339Nano)),
	} {
		appendObservationLine(t, log, clock, raw)
	}
	for index, order := range []string{"asc", "desc"} {
		selection := SchedulerObservationSelection{Summary: true, Filter: "decision", Order: order}
		page, next, _, _, err := log.SchedulerObservationPage(context.Background(), from, from.Add(2*time.Second), 2, 0, selection)
		if err != nil || len(page) != 2 || next != -1 {
			t.Fatalf("%s summary: %+v next=%d err=%v", order, page, next, err)
		}
		want := []string{"first", "second"}
		if index == 1 {
			slices.Reverse(want)
		}
		for i, record := range page {
			if record.Decision.Status != want[i] {
				t.Fatalf("%s prefilter changed selected result: %+v", order, page)
			}
		}
	}
}
