package serving

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
)

func TestLoadWindowKeepsInflowSeparateFromCompletionAndFilters(t *testing.T) {
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}, Definitions: []string{"unrelated"}, Outcomes: []string{"completed"}})
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

func TestSchedulerRawCursorBoundsAppendOrderAcrossWindow(t *testing.T) {
	const laterActionID = "second"
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := observationLog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	// Event time is deliberately older than every append window's metadata.
	from := time.Now().UTC().Add(-2 * time.Hour)
	to := from.Add(time.Second)
	for _, event := range []SchedulerObservation{
		{At: from.Add(-time.Nanosecond), Action: &SchedulerActionObservation{ID: "outside"}},
		{At: from.Add(-24 * time.Hour), Action: &SchedulerActionObservation{ID: "distant"}},
		{At: from.Add(time.Millisecond), Action: &SchedulerActionObservation{ID: "first"}},
		{At: from, Action: &SchedulerActionObservation{ID: laterActionID}},
		{At: to, Action: &SchedulerActionObservation{ID: "boundary"}},
	} {
		if err = observationLog.SchedulerRecord(event); err != nil {
			t.Fatal(err)
		}
		if err := observationLog.finish(); err != nil {
			t.Fatal(err)
		}
		if err := observationLog.start(time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	check := func() {
		t.Helper()
		first, next, err := observationLog.SchedulerObservationPage(from, to, 1, 0)
		if err != nil || len(first) != 1 || first[0].Action.ID != "first" || next != 1 {
			t.Fatalf("first bounded append page: %+v %d %v", first, next, err)
		}
		second, next, err := observationLog.SchedulerObservationPage(from, to, 1, next)
		if err != nil || len(second) != 1 || second[0].Action.ID != laterActionID || next != -1 {
			t.Fatalf("raw cursor reordered/backfilled events: %+v %d %v", second, next, err)
		}
		end, next, err := observationLog.SchedulerObservationPage(from, to, 1, 2)
		if err != nil || len(end) != 0 || next != -1 {
			t.Fatalf("cursor at selected count must return an empty terminal page: %+v %d %v", end, next, err)
		}
		if _, _, err = observationLog.SchedulerObservationPage(from, to, 1, 3); err == nil {
			t.Fatal("out-of-range cursor accepted")
		}
	}
	check()
	if err := observationLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	observationLog, err = NewObservationLog(observationLog.config)
	if err != nil {
		t.Fatal(err)
	}
	check()
}

func TestSchedulerRawPageDecodesOnlySelectedPayloads(t *testing.T) {
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observationLog.Close(context.Background()) }()
	from := time.Now().UTC().Add(-time.Hour)
	to := from.Add(time.Minute)
	appendSentinel := func(at time.Time) {
		t.Helper()
		// Valid JSON, but full SchedulerObservation decoding must reject action.id.
		record := map[string]any{
			"schemaVersion": observationSchema,
			"window":        observationLog.windows[observationLog.active].ID,
			"scheduler": map[string]any{
				"at":     at,
				"action": map[string]any{"id": 42},
			},
		}
		if err := json.NewEncoder(observationLog.files["scheduler"]).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	appendSentinel(from.Add(-time.Nanosecond))
	appendSentinel(from)
	settings, snapshot := schedulerFixture()
	node := snapshot.Nodes[0].Node
	completed := from.Add(time.Second)
	joules := 12.5
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
		Power: &metricobserver.Result{
			StartedAt: from, EndedAt: completed,
			Samples:  []metricobserver.Sample{{ObservedAt: from, Watts: 12.5, RequestLatencySeconds: 0.1}},
			Failures: []metricobserver.Failure{{ObservedAt: completed, Message: "retained failure"}},
			Summary:  metricobserver.Summary{Attempts: 2, SuccessfulSamples: 1, Failures: 1},
		},
		Node: &node, Meter: "meter", PowerScope: "node",
		PowerSampling: &metricobserver.Request{Interval: time.Second, Duration: time.Minute, RequestTimeout: time.Second, MaxEvents: 100},
		PowerCoverage: &powerwindow.Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 0.9, MaximumGap: 2 * time.Second},
		PowerJoules:   &joules, PowerError: "retained power detail",
	}
	if err := observationLog.SchedulerRecord(selected); err != nil {
		t.Fatal(err)
	}
	appendSentinel(from.Add(2 * time.Millisecond))
	// A bounded page must stop after its lookahead, not scan the remaining file.
	if _, err := observationLog.files["scheduler"].WriteString("{not-json}\n"); err != nil {
		t.Fatal(err)
	}
	page, next, err := observationLog.SchedulerObservationPage(from, to, 1, 1)
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
		if _, _, err := observationLog.SchedulerObservationPage(from, to, 1, offset); err == nil {
			t.Fatalf("selected type-invalid payload accepted at offset %d", offset)
		}
	}
	if _, _, err := observationLog.SchedulerObservationPage(from, to, 1, 3); err == nil {
		t.Fatal("syntax error after skipped payloads was ignored")
	}
}

func TestSchedulerRawPageRejectsInvalidUnselectedEnvelopes(t *testing.T) {
	for _, corruption := range []string{"schema", "window", "syntax"} {
		t.Run(corruption, func(t *testing.T) {
			observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = observationLog.Close(context.Background()) }()
			from := time.Now().UTC().Add(-time.Hour)
			record := map[string]any{
				"schemaVersion": observationSchema,
				"window":        observationLog.windows[observationLog.active].ID,
				"scheduler":     map[string]any{"at": from.Add(-time.Nanosecond)},
			}
			switch corruption {
			case "schema":
				record["schemaVersion"] = "not-the-observation-schema"
			case "window":
				record["window"] = "not-the-containing-window"
			}
			if corruption == "syntax" {
				_, err = observationLog.files["scheduler"].WriteString("{not-json}\n")
			} else {
				err = json.NewEncoder(observationLog.files["scheduler"]).Encode(record)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := observationLog.SchedulerRecord(SchedulerObservation{At: from}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := observationLog.SchedulerObservationPage(from, from.Add(time.Minute), 1, 0); err == nil {
				t.Fatal("unselected invalid record was ignored")
			}
			if observationLog.Health().Healthy {
				t.Fatal("read error was not published to observation log health")
			}
		})
	}
}

func TestSchedulerRawPagePreservesReorderedEscapedEnvelope(t *testing.T) {
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observationLog.Close(context.Background()) }()
	at := time.Date(2026, 10, 15, 10, 7, 1, 478738123, time.FixedZone("record-zone", 9*60*60))
	raw := fmt.Sprintf(
		`{"scheduler":{"unknown":{"number":9007199254740993,"text":"schemaVersion/window/at"},"snapshot":{"at":%q,"instances":[]},"action":{"id":"reordered-source"},"at":%q},"w\u0069ndow":%q,"SCHEMAVERSION":%q}`+"\n",
		at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), observationLog.windows[0].ID, observationSchema,
	)
	if _, err = observationLog.files["scheduler"].WriteString(raw); err != nil {
		t.Fatal(err)
	}
	page, next, err := observationLog.SchedulerObservationPage(at.Add(-time.Nanosecond), at.Add(time.Nanosecond), 1, 0)
	if err != nil || len(page) != 1 || next != -1 {
		t.Fatalf("reordered envelope rejected: %+v %d %v", page, next, err)
	}
	if !page[0].At.Equal(at) || page[0].Snapshot == nil || !page[0].Snapshot.At.Equal(at) || page[0].Action.ID != "reordered-source" {
		t.Fatalf("iterator changed event time or consumed payload: %+v", page[0])
	}
}

func TestSchedulerRawPageRejectsMalformedIgnoredSubtrees(t *testing.T) {
	for _, invalid := range []string{`-01`, `1e`, `["value",]`, `"bad\q"`} {
		t.Run(invalid, func(t *testing.T) {
			observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = observationLog.Close(context.Background()) }()
			from := time.Now().UTC()
			raw := fmt.Sprintf(
				`{"schemaVersion":%q,"window":%q,"scheduler":{"at":%q,"snapshot":{"options":%s}}}`+"\n",
				observationSchema, observationLog.windows[0].ID, from.Add(-time.Second).Format(time.RFC3339Nano), invalid,
			)
			if _, err = observationLog.files["scheduler"].WriteString(raw); err != nil {
				t.Fatal(err)
			}
			if err = observationLog.SchedulerRecord(SchedulerObservation{At: from}); err != nil {
				t.Fatal(err)
			}
			if _, _, err = observationLog.SchedulerObservationPage(from, from.Add(time.Second), 1, 0); err == nil || observationLog.Health().Healthy {
				t.Fatalf("malformed skipped JSON did not preserve error/health boundary: %v", err)
			}
		})
	}
}

func TestRequestCensusKeepsLateFactsAcrossWindows(t *testing.T) {
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observationLog.Close(context.Background()) })
	rotate := func() {
		t.Helper()
		if err := observationLog.finish(); err != nil {
			t.Fatal(err)
		}
		if err := observationLog.start(time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	appendEvents := func(events ...RequestEvent) {
		t.Helper()
		for _, event := range events {
			if err := observationLog.RequestEvent(event); err != nil {
				t.Fatal(err)
			}
		}
	}
	from := time.Now().UTC().Add(-24 * time.Hour)
	to := from.Add(time.Minute)
	d := &Definition{UID: "workload", Generation: 1}
	if err := observationLog.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	rotate() // Definition and receipt must not need the same append window.
	appendEvents(RequestEvent{RequestID: "old", Kind: phaseReceived, At: from.Add(-time.Hour)})
	rotate()
	appendEvents(RequestEvent{RequestID: "arrival", Kind: phaseReceived, At: to.Add(time.Hour), ReceivedAt: from})
	rotate()
	appendEvents(
		RequestEvent{RequestID: "arrival", Kind: "validated", At: to.Add(2 * time.Hour), WorkloadUID: d.UID, Definition: d.Identity()},
		RequestEvent{RequestID: "arrival", Kind: phaseAccepted, At: to.Add(2 * time.Hour), LifetimeNanos: int64(time.Second)},
	)
	rotate()
	appendEvents(RequestEvent{RequestID: "arrival", Kind: eventResponseFinished, At: from.Add(-time.Hour), Outcome: outcomeCompleted})
	check := func() {
		t.Helper()
		arrivals, err := observationLog.SchedulerArrivals(from, to)
		if err != nil || len(arrivals) != 1 {
			t.Fatalf("receipt-time selection lost arrival: %+v %v", arrivals, err)
		}
		if a := arrivals[0]; a.ID != "arrival" || !a.At.Equal(from) || a.Definition == nil || a.Definition.Identity() != d.Identity() ||
			!a.Compute || !a.Accepted || a.Outcome != outcomeCompleted || a.Lifetime != time.Second {
			t.Fatalf("later facts without ReceivedAt or older definition were pruned: %+v", a)
		}
	}
	check()
	if err := observationLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	observationLog, err = NewObservationLog(observationLog.config)
	if err != nil {
		t.Fatal(err)
	}
	check()
}

func TestRequestCensusKeepsUnknownPrefixesAndIncompleteMetadata(t *testing.T) {
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observationLog.Close(context.Background()) })
	from := time.Now().UTC().Add(-24 * time.Hour)
	to := from.Add(time.Minute)
	if err := observationLog.RequestEvent(RequestEvent{RequestID: "old", Kind: phaseReceived, At: from.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if arrivals, err := observationLog.SchedulerArrivals(from, to); err != nil || len(arrivals) != 0 {
		t.Fatalf("old facts became current arrivals: %+v %v", arrivals, err)
	}
	// An empty pruned stream must not hide overlapping incomplete metadata.
	current := observationLog.windows[observationLog.active].StartedAt
	observationLog.windows[observationLog.active].Incomplete = true
	if load, err := observationLog.LoadWindow(current, current.Add(time.Second)); err != nil || len(load.Arrivals) != 0 || len(load.IncompleteWindows) != 1 {
		t.Fatalf("pruned metadata completeness was lost: %+v %v", load, err)
	}
	// Direct writes bypass counted appends: changed prefixes must be scanned
	// even when their known bounds say the query is empty.
	event := RequestEvent{RequestID: "uncounted", Kind: phaseReceived, At: from.Add(-time.Hour), ReceivedAt: from}
	if err := json.NewEncoder(observationLog.files[observationRequestStream]).Encode(observationRecord{SchemaVersion: observationSchema, Window: observationLog.windows[observationLog.active].ID, Request: &event}); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.finish(); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.start(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.RequestEvent(RequestEvent{RequestID: event.RequestID, Kind: eventResponseFinished, At: from.Add(-time.Hour), Outcome: outcomeIncomplete}); err != nil {
		t.Fatal(err)
	}
	arrivals, err := observationLog.SchedulerArrivals(from, to)
	if err != nil || len(arrivals) != 1 || arrivals[0].ID != event.RequestID || arrivals[0].Outcome != outcomeIncomplete {
		t.Fatalf("unknown prefix or its later facts became an empty success: %+v %v", arrivals, err)
	}
	if _, err := observationLog.files[observationRequestStream].WriteString("{not-json}\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := observationLog.SchedulerArrivals(from, to); err == nil {
		t.Fatal("unknown corrupt prefix became an empty success")
	}
}

func TestWorkloadObservationReadsOnlyRequestedWindow(t *testing.T) {
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observationLog.Close(context.Background()) })
	d := &Definition{UID: "workload", Generation: 1, SLO: SLO{Percentile: 99, TargetLatency: time.Second}}
	if err := observationLog.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	appendEvents := func(events ...RequestEvent) {
		t.Helper()
		for _, event := range events {
			event.WorkloadUID, event.Definition = d.UID, d.Identity()
			if err := observationLog.RequestEvent(event); err != nil {
				t.Fatal(err)
			}
		}
	}
	request := func(id string, received time.Time) []RequestEvent {
		return []RequestEvent{
			{RequestID: id, Kind: phaseReceived, At: received, ReceivedAt: received},
			{RequestID: id, Kind: phaseAccepted, At: received, ReceivedAt: received},
			{RequestID: id, Kind: eventResponseFinished, At: received.Add(time.Millisecond), ReceivedAt: received, ElapsedNanos: int64(time.Millisecond), Outcome: outcomeCompleted},
		}
	}
	base := time.Now().UTC().Add(-48 * time.Hour)
	const windows, perWindow, target = 20, 200, 10
	from := base.Add(target * time.Hour)
	to := from.Add(2 * time.Minute)
	for w := range windows {
		if w > 0 {
			if err := observationLog.finish(); err != nil {
				t.Fatal(err)
			}
			if err := observationLog.start(time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
		for r := range perWindow {
			appendEvents(request(fmt.Sprintf("%d-%d", w, r), base.Add(time.Duration(w)*time.Hour+time.Duration(r)*15*time.Second))...)
		}
		switch w {
		case target:
			late := request("late", from.Add(time.Minute))
			appendEvents(late[:2]...)
			// Out-of-range lines keep a valid envelope but fail full decoding.
			for range 3 {
				raw := fmt.Sprintf(`{"schemaVersion":%q,"window":%q,"request":{"requestID":"sentinel","workloadUID":%q,"kind":"received","at":%q,"receivedAt":%q,"elapsedNanos":"bad"}}`+"\n",
					observationSchema, observationLog.windows[observationLog.active].ID, d.UID, to.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano))
				if _, err := observationLog.files[observationRequestStream].WriteString(raw); err != nil {
					t.Fatal(err)
				}
			}
		case target + 1:
			appendEvents(request("late", from.Add(time.Minute))[2])
		}
	}
	// Windows outside the requested receipts are unreadable: any read fails the query.
	for w, window := range observationLog.windows {
		if w == target || w == target+1 {
			continue
		}
		name := filepath.Join(observationLog.config.Directory, window.ID, observationRequestStream+".jsonl")
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, bytes.Repeat([]byte{'x'}, int(info.Size())), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := observationLog.Query(d.UID, from, to, nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	const want = 2*time.Minute/(15*time.Second) + 1 // receipts every 15 s plus the late request
	if result.Total != int(want) || result.Outcomes[outcomeCompleted] != int(want) || len(result.Cohorts) != 1 ||
		result.Cohorts[0].SampleCount != int(want) || len(result.Requests) != 3*int(want) {
		t.Fatalf("narrow window result: total %d outcomes %v cohorts %+v requests %d", result.Total, result.Outcomes, result.Cohorts, len(result.Requests))
	}
}

func TestDefinitionCensusLoadsAppendsToPreviouslyEmptyPrefixes(t *testing.T) {
	for _, counted := range []bool{true, false} {
		t.Run(fmt.Sprintf("counted=%t", counted), func(t *testing.T) {
			observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = observationLog.Close(context.Background()) })
			from := time.Now().UTC().Add(-24 * time.Hour)
			to := from.Add(time.Minute)
			d := &Definition{UID: "retained", Generation: 1}
			if err := observationLog.RequestEvent(RequestEvent{RequestID: "waiting", Kind: phaseReceived, At: from, Definition: d.Identity()}); err != nil {
				t.Fatal(err)
			}
			arrivals, err := observationLog.SchedulerArrivals(from, to)
			if err != nil || len(arrivals) != 1 || arrivals[0].Definition != nil {
				t.Fatalf("missing definition must stay unknown: %+v %v", arrivals, err)
			}
			if counted {
				err = observationLog.DefinitionRecord(d)
			} else {
				// A changed prefix must invalidate the known-empty summary.
				err = json.NewEncoder(observationLog.files[observationDefinitionStream]).Encode(observationRecord{
					SchemaVersion: observationSchema, Window: observationLog.windows[observationLog.active].ID,
					Definition: &DefinitionObservation{Identity: d.Identity(), Definition: d},
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			check := func() {
				t.Helper()
				arrivals, err := observationLog.SchedulerArrivals(from, to)
				if err != nil || len(arrivals) != 1 || arrivals[0].Definition == nil || arrivals[0].Definition.Identity() != d.Identity() {
					t.Fatalf("new or uncounted definition remained pruned: %+v %v", arrivals, err)
				}
			}
			check()
			if err := observationLog.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			observationLog, err = NewObservationLog(observationLog.config)
			if err != nil {
				t.Fatal(err)
			}
			check()
		})
	}
}

// Scale of docs/design/serving/decision-record-diet.md §1.
func TestSchedulerRecordStoresCompactDecision(t *testing.T) {
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observationLog.Close(context.Background()) }()
	_, snapshot := schedulerFixture()
	at := time.Now().UTC().Add(-time.Minute)
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
	instance.ArrivalAges = map[string][]time.Duration{"w1": {time.Second, 2 * time.Second}}
	instances := repeated([]SchedulerInstance{instance}, 6)
	nodes := repeated(snapshot.Nodes, 8)
	snapshot.Nodes, snapshot.Instances = nodes, instances
	snapshot.Workloads = repeated(snapshot.Workloads, 3)
	snapshot.Options = repeated(snapshot.Options[:1], 16)
	for i := range snapshot.Options {
		snapshot.Options[i].Instance = instance
	}
	definition := snapshot.Workloads[0]
	snapshot.Arrivals = repeated([]SchedulerArrival{{ID: "request", At: at, Definition: definition, Accepted: true, Compute: true, WorkloadUID: "w1"}}, 3000)
	snapshot.Outstanding = repeated([]SchedulerRequest{{ID: "request", Definition: definition, ReceivedAt: at, Phase: "queued", Instance: "i1"}}, 20)
	actions := []Action{{ID: "prepare", Kind: "prepare", Node: nodes[0].Node, Instance: "i1", DependsOn: []string{}, Status: "planned"}}
	joules := 42.5
	candidate := SchedulerCandidate{
		ID: "candidate", Nodes: nodes, Instances: instances, Routing: map[string]map[string]int32{"w1": {"i1": 1}}, Actions: actions,
		Status: "evaluated", Reasons: []string{"reason"}, ExpectedJoules: &joules, WorstExceedance: 0.25,
		SLO: []SchedulerEstimate{{UID: "w1", Definition: definition.Identity(), Samples: 100, Percentile: 95}}, Assumptions: []string{"assumption"}, Sources: []string{"source"},
	}
	decision := SchedulerDecision{At: at, Status: "target", Keep: candidate, Target: &candidate, Candidates: repeated([]SchedulerCandidate{candidate}, 128), EvaluationElapsed: 3 * time.Millisecond,
		Policy: &SchedulerPolicyDecision{Kernels: map[string]map[string]SchedulerTransitionProjection{"a": {"b": {}}}}}
	event := SchedulerObservation{At: at, Decision: &decision, Snapshot: &snapshot}
	before, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err = observationLog.SchedulerRecord(event); err != nil {
		t.Fatal(err)
	}
	info, err := observationLog.files["scheduler"].Stat()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("decision record bytes: before %d after %d", len(before), info.Size())
	if info.Size() > 100<<10 {
		t.Fatalf("stored decision record %d bytes exceeds 100KiB", info.Size())
	}
	// The Dashboard history route reads this page.
	page, _, err := observationLog.SchedulerObservationPage(at, at.Add(time.Second), 1, 0)
	if err != nil || len(page) != 1 {
		t.Fatalf("stored decision not readable: %+v %v", page, err)
	}
	stored, storedSnapshot := page[0].Decision, page[0].Snapshot
	if stored.EvaluationElapsed != 3*time.Millisecond || stored.Policy.Kernels != nil {
		t.Fatalf("decision timing or kernels: %+v", stored)
	}
	if stored.Keep.Instances[0].ProfileID != profile.ID || stored.Keep.Instances[0].Profile != nil || stored.Target.SLO == nil || len(stored.Target.Nodes) != 8 {
		t.Fatalf("keep or target lost its body: %+v", stored.Keep)
	}
	summary := stored.Candidates[127]
	if len(stored.Candidates) != 128 || summary.ID != "candidate" || summary.Status != "evaluated" || *summary.ExpectedJoules != joules || summary.WorstExceedance != 0.25 || len(summary.Actions) != 1 || summary.Instances != nil || summary.SLO != nil {
		t.Fatalf("candidate summary: %+v", summary)
	}
	if storedSnapshot.Arrivals != nil || storedSnapshot.Outstanding[0].Definition != nil || storedSnapshot.Outstanding[0].DefinitionID != definition.Identity() || storedSnapshot.Instances[0].ArrivalAges["w1"][1] != 2*time.Second {
		t.Fatalf("snapshot replay inputs: %+v", storedSnapshot)
	}
}

func repeated[T any](items []T, n int) []T {
	out := make([]T, 0, len(items)*n)
	for range n {
		out = append(out, items...)
	}
	return out
}
