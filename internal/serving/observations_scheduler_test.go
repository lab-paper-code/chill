package serving

import (
	"context"
	"testing"
	"time"
)

func TestLoadWindowKeepsInflowSeparateFromCompletionAndFilters(t *testing.T) {
	j, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}, Definitions: []string{"unrelated"}, Outcomes: []string{"completed"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := j.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	from := time.Now().UTC().Add(-time.Second)
	to := from.Add(time.Second)
	d := &Definition{Name: "workload", UID: "uid", Generation: 1, SLO: SLO{Percentile: 95, TargetLatency: time.Second}}
	if err = j.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	appendEvent := func(id, uid, kind, outcome string, at time.Time) {
		t.Helper()
		e := RequestEvent{RequestID: id, WorkloadUID: uid, Kind: kind, Outcome: outcome, At: at}
		if kind == "validated" || kind == "accepted" {
			e.Definition = d.Identity()
		}
		if err := j.RequestEvent(e); err != nil {
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
	window, err := j.LoadWindow(from, to)
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
	empty, err := j.LoadWindow(to.Add(time.Second), to.Add(2*time.Second))
	if err != nil || len(empty.Arrivals) != 0 || len(empty.Workloads) != 0 {
		t.Fatalf("empty event window: %+v %v", empty, err)
	}
}

func TestObservedOverheadJoinsReceiptResidualAndResponseByInstance(t *testing.T) {
	j, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := j.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	from := time.Now().UTC().Add(-time.Second)
	to := from.Add(time.Second)
	ms := time.Millisecond
	// exchangeAt starts at receipt; client starts only at the native HTTP call.
	record := func(id, instance, workload string, receivedAt time.Time, outcome string, client, native, exchangeAt, responseAt time.Duration) {
		t.Helper()
		d := &Definition{UID: workload, Generation: 1, Model: Model{UID: "model", Generation: 1, Paths: []ModelPath{{Name: "native", Artifact: "artifact"}}}}
		if err := j.DefinitionRecord(d); err != nil {
			t.Fatal(err)
		}
		progress := &BackendProgress{Phase: "finished", ProfileID: "profile", ExecutionComplete: true, NativeElapsedMS: float64(native) / float64(ms)}
		for _, e := range []RequestEvent{
			{RequestID: id, Kind: "backend_exchange_finished", Outcome: outcome, At: receivedAt, ReceivedAt: receivedAt, WorkloadUID: workload, Definition: d.Identity(), ElapsedNanos: int64(exchangeAt), ClientSpanNanos: int64(client), Assignment: &Assignment{Instance: instance, Node: NodeReference{Name: "node", UID: "node-uid"}, Profile: "profile", Artifact: "artifact", ExecutionPath: "native"}, NativeProgress: progress},
			{RequestID: id, Kind: "response_finished", Outcome: outcome, At: receivedAt, ReceivedAt: receivedAt, ElapsedNanos: int64(responseAt)},
		} {
			if err := j.RequestEvent(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	record("a", "i1", "w1", from, "completed", 10*ms, 6*ms, 20*ms, 23*ms)
	record("b", "i1", "w2", from.Add(ms), "completed", 12*ms, 5*ms, 30*ms, 31*ms)
	record("dropped", "i1", "w1", from.Add(2*ms), "timeout", 900*ms, 1*ms, 900*ms, 950*ms)
	record("before", "i1", "w1", from.Add(-ms), "completed", 800*ms, 1*ms, 800*ms, 850*ms)
	record("at-end", "i1", "w1", to, "completed", 800*ms, 1*ms, 800*ms, 850*ms)
	record("clock-inconsistent", "i2", "w1", from, "completed", 3*ms, 5*ms, 20*ms, 21*ms)
	got, err := j.ObservedOverhead(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("unmeasured instance must stay absent, not zero: %+v", got)
	}
	// a: (20-6)+(23-20)=17ms, b: (30-5)+(31-30)=26ms.
	if o := got["i1"]; o.Completion != 26*ms || o.Requests != 2 || len(o.Definitions) != 2 || o.Node.UID != "node-uid" || o.Profile != "profile" || o.Artifact != "artifact" || o.ExecutionPath != "native" || o.Source == "" {
		t.Fatalf("overhead must be the observed maximum of completed in-window requests for the instance: %+v", o)
	}
	adjacent, err := j.ObservedOverhead(to, to.Add(time.Second))
	if err != nil || len(adjacent) != 1 || adjacent["i1"].Requests != 1 || adjacent["i1"].Completion != 849*ms {
		t.Fatalf("receipt exactly at the prior window end must belong to the adjacent window: %+v %v", adjacent, err)
	}
	empty, err := j.ObservedOverhead(to.Add(time.Second), to.Add(2*time.Second))
	if err != nil || len(empty) != 0 {
		t.Fatalf("measurements outside the receipt window must not be reused: %+v %v", empty, err)
	}
	// A slow upload/auth/decode before dispatch is part of the receipt-based SLO,
	// even though the native HTTP client span is only 10ms.
	record("slow-ingress", "i1", "w1", from, "completed", 10*ms, 6*ms, 1210*ms, 1213*ms)
	delayed, err := j.ObservedOverhead(from, to)
	if err != nil || delayed["i1"].Completion != 1207*ms {
		t.Fatalf("receipt-to-dispatch delay was omitted: %+v %v", delayed, err)
	}
	settings, snapshot := schedulerFixture()
	settings.Controls.Enabled = false
	snapshot.Workloads[0].SLO.TargetLatency = time.Second
	snapshot.Instances[0].CompletionOverhead = delayed["i1"].Completion
	snapshot.Arrivals = []SchedulerArrival{{At: snapshot.At.Add(-settings.ObservationWindow), Definition: snapshot.Workloads[0], Compute: true, Accepted: true}}
	decision := EvaluateScheduler(settings, snapshot)
	estimate := schedulerRequireEstimate(t, decision.Keep, snapshot.Workloads[0].UID)
	if estimate.ExpectedNanos == nil || *estimate.ExpectedNanos < float64(1207*ms) || decision.Keep.Status != schedulerStatusOverload {
		t.Fatalf("large receipt-to-dispatch delay must miss the 1s SLO: %+v", decision.Keep)
	}
	j.windows[j.active].Incomplete = true
	if _, err := j.ObservedOverhead(from, to.Add(time.Second)); err == nil {
		t.Fatal("overhead from an incomplete observation window became usable")
	}
}

func TestObservedOverheadRequiresFrozenExecutionIdentity(t *testing.T) {
	j, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close(context.Background()) }()
	d := &Definition{UID: "workload", Generation: 1, Model: Model{UID: "model", Generation: 1, Paths: []ModelPath{{Name: "native", Artifact: "artifact"}}}}
	if err := j.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	for _, name := range []string{"definition missing", "node missing", "profile mismatch", "input path mismatch", "conflicting executor"} {
		a := Assignment{Instance: name, Node: NodeReference{Name: "node", UID: "node-uid"}, Profile: "profile", Artifact: "artifact", ExecutionPath: "native"}
		p := BackendProgress{Phase: "finished", ProfileID: a.Profile, ExecutionComplete: true, NativeElapsedMS: 1}
		e := RequestEvent{RequestID: name, WorkloadUID: d.UID, Definition: d.Identity(), Kind: "backend_exchange_finished", Outcome: "completed", At: at, ReceivedAt: at, Assignment: &a, NativeProgress: &p, ClientSpanNanos: int64(2 * time.Millisecond), ElapsedNanos: int64(3 * time.Millisecond)}
		switch name {
		case "definition missing":
			e.Definition = "not-retained"
		case "node missing":
			a.Node.UID = ""
		case "profile mismatch":
			p.ProfileID = "different-runtime"
		case "input path mismatch":
			a.ExecutionPath = "different-input"
		}
		for trial := range 2 {
			e.RequestID = name + string(rune('a'+trial))
			if name == "conflicting executor" && trial == 1 {
				a.Profile, p.ProfileID = "other-profile", "other-profile"
			}
			if err := j.RequestEvent(e); err != nil {
				t.Fatal(err)
			}
			if err := j.RequestEvent(RequestEvent{RequestID: e.RequestID, Kind: "response_finished", Outcome: "completed", At: at, ReceivedAt: at, ElapsedNanos: int64(4 * time.Millisecond)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := j.ObservedOverhead(at.Add(-time.Second), at.Add(time.Second))
	if err != nil || len(got) != 0 {
		t.Fatalf("missing or conflicting execution identities must stay unobserved: %+v %v", got, err)
	}
}

func TestSchedulerRawCursorBoundsAppendOrderAcrossWindow(t *testing.T) {
	j, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := j.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	from := time.Now().UTC().Add(-time.Second)
	to := from.Add(time.Second)
	for _, event := range []SchedulerObservation{
		{At: from.Add(-time.Nanosecond), Action: &SchedulerActionObservation{ID: "outside"}},
		{At: from.Add(time.Millisecond), Action: &SchedulerActionObservation{ID: "first"}},
		{At: from, Action: &SchedulerActionObservation{ID: "second"}},
		{At: to, Action: &SchedulerActionObservation{ID: "boundary"}},
	} {
		if err = j.SchedulerRecord(event); err != nil {
			t.Fatal(err)
		}
	}
	first, next, err := j.SchedulerObservationPage(from, to, 1, 0)
	if err != nil || len(first) != 1 || first[0].Action.ID != "first" || next != 1 {
		t.Fatalf("first bounded append page: %+v %d %v", first, next, err)
	}
	second, next, err := j.SchedulerObservationPage(from, to, 1, next)
	if err != nil || len(second) != 1 || second[0].Action.ID != "second" || next != -1 {
		t.Fatalf("raw cursor reordered/backfilled events: %+v %d %v", second, next, err)
	}
	if _, _, err = j.SchedulerObservationPage(from, to, 1, 3); err == nil {
		t.Fatal("out-of-range cursor accepted")
	}
}
