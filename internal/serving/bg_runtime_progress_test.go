package serving

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// SCH-05 values come from the native At of each CHILL_PROGRESS line, so the
// observer may reflect a line late without changing the scheduler inputs or
// the recorded request rows.
func TestBackendProgressValuesIndependentOfReflectionTime(t *testing.T) {
	_, snapshot := schedulerFixture()
	d := snapshot.Workloads[0]
	received := time.Unix(1000, 0).UTC()
	at := func(ms int) time.Time { return received.Add(time.Duration(ms) * time.Millisecond) }
	phases := []BackendProgress{
		{Phase: phaseQueued, At: at(2)},
		{Phase: phaseRunning, At: at(5)},
		{Phase: phaseFinished, At: at(9), ExecutionComplete: true, NativeElapsedMS: 4},
	}
	type observed struct {
		inputs [][]SchedulerRequest
		rows   []RequestRow
	}
	run := func(delay time.Duration) observed {
		observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Writer: &observationArchive{directory: t.TempDir()}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = observationLog.Close(context.Background()) }()
		if err = observationLog.DefinitionRecord(d); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"received", "accepted"} {
			if err = observationLog.RequestEvent(RequestEvent{RequestID: "late", WorkloadName: d.Name, WorkloadUID: d.UID, Definition: d.Identity(), Kind: kind, At: received, ReceivedAt: received}); err != nil {
				t.Fatal(err)
			}
		}
		r, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 4, ObservationLog: observationLog})
		if err != nil {
			t.Fatal(err)
		}
		defer r.cancel()
		call := &Call{receipt: Receipt{ID: "late", UID: d.UID, ReceivedAt: received, Deadline: received.Add(time.Second)}, definition: d, identity: d.Identity(), assignment: &Assignment{Instance: "i1", DispatchedAt: at(1)}, backendContext: context.Background()}
		r.calls[call.receipt.ID] = call
		now := func() time.Time { return at(20) }
		result := observed{}
		for _, progress := range phases {
			time.Sleep(delay)
			progress.RequestID, progress.WorkloadUID, progress.Definition = call.receipt.ID, d.UID, d.Identity()
			if err = r.ObserveBackend("i1", progress); err != nil {
				t.Fatal(err)
			}
			result.inputs = append(result.inputs, r.SchedulerInputs(now).Outstanding)
		}
		rows, err := observationLog.Query(d.UID, received.Add(-time.Second), received.Add(time.Second), nil, nil, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		result.rows = rows.Requests
		return result
	}
	prompt, late := run(0), run(20*time.Millisecond)
	if len(prompt.rows) != 1 || prompt.rows[0].QueuedNanos == nil || prompt.rows[0].RunningNanos == nil {
		t.Fatalf("progress not recorded: %+v", prompt.rows)
	}
	if !reflect.DeepEqual(prompt, late) {
		t.Fatalf("late reflection changed SCH-05 values:\n%+v\n%+v", prompt, late)
	}
}
