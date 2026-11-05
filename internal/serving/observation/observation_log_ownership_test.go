package observation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
)

const restoredRequest = "request"

// A log-clean handoff can retain a physical assignment: recording success does
// not prove execution complete. RestartDrain still requires exact completion.
func TestRestartRequiresExplicitPhysicalCompletionProof(t *testing.T) {
	ctx := context.Background()
	config := ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: &observationArchive{directory: t.TempDir()}}
	observationLog, _ := clockedObservationLog(t, config, time.Now().UTC())
	config = observationLog.config
	d := &serving.Definition{Name: "workload", UID: "uid", Generation: 1, Model: serving.Model{UID: "model", Generation: 1}, SLO: serving.SLO{Percentile: 99, TargetLatency: time.Second}}
	if err := observationLog.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	assignment := &serving.Assignment{Instance: "physical", Node: serving.NodeReference{Name: "node", UID: "node-uid"}, Profile: "profile", DispatchedAt: now}
	for _, event := range []serving.RequestEvent{
		{RequestID: restoredRequest, WorkloadUID: d.UID, Definition: d.Identity(), Kind: "received", At: now, ReceivedAt: now},
		{RequestID: restoredRequest, WorkloadUID: d.UID, Definition: d.Identity(), Kind: "assigned", At: now, ReceivedAt: now, Assignment: assignment},
		{RequestID: restoredRequest, WorkloadUID: d.UID, Definition: d.Identity(), Kind: "backend_finished", At: now, NativeProgress: &serving.BackendProgress{Phase: "finished", ExecutionComplete: false}},
		{RequestID: restoredRequest, WorkloadUID: d.UID, Definition: d.Identity(), Kind: "response_finished", At: now, Outcome: "canceled"},
	} {
		if err := observationLog.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := observationLog.Close(ctx); err != nil { // Graceful log handoff, not a crash.
		t.Fatal(err)
	}
	restartedLog, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{QueueLimit: 1, ObservationLog: restartedLog})
	if err != nil {
		t.Fatal(err)
	}
	state, stateErr := restored.State(d.Name)
	if stateErr != nil || state.Reason != "RestartDrain" || state.Assigned != 1 {
		t.Fatalf("restored assignment did not close admission: %+v %v", state, stateErr)
	}
	if detached, err := restored.DetachInstance("physical"); err != nil || detached {
		t.Fatalf("bare finished phase freed restarted physical ownership: %v %v", detached, err)
	}
	if err = restored.ConfirmInstanceStopped("physical"); err != nil {
		t.Fatal(err)
	}
	if err = restartedLog.Close(ctx); err != nil {
		t.Fatal(err)
	}
	finals := 0
	for _, row := range readRequestRows(t, config.Directory) {
		if row.Kind == serving.RequestRowFinal {
			finals++
			if row.RequestID != restoredRequest || row.Outcome != "incomplete" || row.Error != "EntrypointRestarted" ||
				row.DispatchedNanos == nil || *row.DispatchedNanos != 0 {
				t.Fatalf("restored request closed with a wrong final row: %+v", row)
			}
		}
	}
	if finals != 1 {
		t.Fatalf("restored request has %d final rows", finals)
	}
	confirmedLog, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = confirmedLog.Close(ctx) }()
	confirmed, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{QueueLimit: 1, ObservationLog: confirmedLog})
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed.Drained(d.UID) {
		t.Fatal("actual container termination proof lost at restart")
	}
	checkRestoredDemand(t, confirmedLog, now)
}

// m4 r6: after a restart under load a restored assignment the native no longer
// holds is finished by the same absent-native rule as a live call; admission
// stayed RestartDrain until the instance Pod was deleted.
func TestRestoredCallTheNativeDoesNotHoldFinishes(t *testing.T) {
	ctx := context.Background()
	config := ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: &observationArchive{directory: t.TempDir()}}
	observationLog, _ := clockedObservationLog(t, config, time.Now().UTC())
	config = observationLog.config
	_, snapshot := schedulerFixture()
	d, p := snapshot.Workloads[0], snapshot.Instances[0].Profile
	if err := observationLog.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	assignment := &serving.Assignment{Instance: "i1", Node: snapshot.Nodes[0].Node, Profile: p.ID, DispatchedAt: now}
	for _, event := range []serving.RequestEvent{
		{RequestID: restoredRequest, WorkloadUID: d.UID, Definition: d.Identity(), Kind: "received", At: now, ReceivedAt: now},
		{RequestID: restoredRequest, WorkloadUID: d.UID, Definition: d.Identity(), Kind: "assigned", At: now, ReceivedAt: now, Assignment: assignment},
	} {
		if err := observationLog.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := observationLog.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restartedLog, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restartedLog.Close(ctx) }()
	restored, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{QueueLimit: 1, ObservationLog: restartedLog})
	if err != nil {
		t.Fatal(err)
	}
	restored.SetProfiles([]serving.Profile{*p})
	instance := serving.ReadyInstance{State: serving.InstanceState{ID: "i1", Node: snapshot.Nodes[0].Node, Profile: p.ID, Ready: true, ObservedAt: time.Now(), NativeVerified: true, Native: &native.State{Ready: true}},
		Profile: p, Capacity: 1, Execute: func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
			return serving.BackendResult{}, nil
		}}
	if err = restored.InstallInstances([]serving.ReadyInstance{instance}); err != nil {
		t.Fatal(err)
	}
	if !restored.Drained(d.UID) {
		t.Fatal("a restored call the native does not hold kept RestartDrain")
	}
}

// The restored final row still counts as accepted, assigned computable demand.
func checkRestoredDemand(t *testing.T, observationLog *ObservationLog, now time.Time) {
	t.Helper()
	load, err := observationLog.LoadWindow(context.Background(), now.Add(-time.Second), now.Add(time.Second))
	if err != nil || len(load.Arrivals) != 1 || !load.Arrivals[0].Accepted || !load.Arrivals[0].Compute ||
		len(load.Workloads) != 1 || load.Workloads[0].Accepted != 1 || load.Workloads[0].Assigned != 1 {
		t.Fatalf("restored request lost its dispatch: %+v %v", load, err)
	}
}

func readRequestRows(t *testing.T, directory string) []serving.RequestRow {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(directory, observationVersion, "*", observationRequestStream+".*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []serving.RequestRow
	for _, name := range names {
		file, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(file)
		for decoder.More() {
			var row serving.RequestRow
			if err := decoder.Decode(&row); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, row)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return rows
}
