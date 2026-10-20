package serving

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func assignObservationRequest(t *testing.T, observationLog *ObservationLog, id string, received time.Time) {
	t.Helper()
	assignment := &Assignment{Node: NodeReference{Name: "node", UID: "node-uid"}, Instance: "i1", DispatchedAt: received}
	for _, kind := range []string{phaseReceived, phaseAccepted, phaseAssigned} {
		event := RequestEvent{RequestID: id, WorkloadUID: "uid", Kind: kind, At: received, ReceivedAt: received, Assignment: assignment}
		if err := observationLog.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
}

// A failed final append keeps the open row; the next Flush stores it.
func TestRequestClosedKeepsRowUntilFinalIsStored(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	received := observationTestHour.Add(time.Second)
	assignObservationRequest(t, observationLog, "closing", received)
	clock.set(observationTestHour.Add(time.Hour))
	blocked := filepath.Join(observationLog.root, "20261020T11")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.RequestClosed("closing"); err == nil || observationLog.open["closing"] == nil {
		t.Fatalf("failed final append dropped the open row: %v", err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	finals := 0
	for _, row := range readRequestRows(t, observationLog.config.Directory) {
		if row.Kind == requestRowFinal && row.AcceptedNanos != nil && row.DispatchedNanos != nil {
			finals++
		}
	}
	if finals != 1 || len(observationLog.open) != 0 {
		t.Fatalf("retried final rows %d, open %d", finals, len(observationLog.open))
	}
}

// The hour holding an unsettled assignment, and later hours, stay local so a
// restart can still drain it.
func TestArchiveKeepsHourOfUnsettledAssignment(t *testing.T) {
	ctx := context.Background()
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	assignObservationRequest(t, observationLog, "outstanding", observationTestHour.Add(time.Second))
	clock.set(observationTestHour.Add(3*time.Hour + time.Minute))
	if err := observationLog.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	hour := filepath.Join(observationLog.root, "20261020T10")
	if _, err := os.Stat(hour); err != nil {
		t.Fatalf("hour of an unsettled assignment was deleted: %v", err)
	}
	restarted, err := NewObservationLog(observationLog.config)
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := restarted.runtimeFacts(); len(pending) != 1 {
		t.Fatalf("restart lost the unsettled assignment: %+v", pending)
	}
	if err := restarted.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.RequestClosed("outstanding"); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hour); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("settled hour was kept: %v", err)
	}
}

// Deleting an hour advances the start of local observation.
func TestArchiveDeletionReportsGap(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	seedObservationMinutes(t, observationLog, clock, observationTestHour.Add(time.Second))
	clock.set(observationTestHour.Add(2*time.Hour + time.Minute))
	if err := observationLog.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	from, floor := observationTestHour, observationTestHour.Add(time.Hour)
	result, err := observationLog.Query("uid", from, from.Add(3*time.Hour), nil, nil, 0, "")
	if err != nil || result.GapUntil == nil || !result.GapUntil.Equal(floor) || result.Total != 0 {
		t.Fatalf("workload query after deletion: %+v %v", result, err)
	}
	load, err := observationLog.LoadWindow(from, from.Add(3*time.Hour))
	if err != nil || load.GapUntil == nil || !load.GapUntil.Equal(floor) {
		t.Fatalf("load after deletion: %+v %v", load, err)
	}
	power, err := observationLog.OperatingPower(from, from.Add(time.Minute), nil, 10)
	if err != nil || power.GapUntil == nil || power.Complete {
		t.Fatalf("power after deletion: %+v %v", power, err)
	}
	if _, _, gap, err := observationLog.SchedulerObservationPage(from, from.Add(time.Hour), 10, 0); err != nil || gap == nil {
		t.Fatalf("scheduler page after deletion: %v %v", gap, err)
	}
}

// A capture sees a whole hour or its deletion with the gap, never part of it.
func TestCaptureSeesWholeHourOrGap(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	minutes := make([]time.Time, 30)
	for index := range minutes {
		minutes[index] = observationTestHour.Add(time.Duration(index)*time.Minute + time.Second)
	}
	seedObservationMinutes(t, observationLog, clock, minutes...)
	clock.set(observationTestHour.Add(2*time.Hour + time.Minute))
	done := make(chan error, 1)
	go func() { done <- observationLog.Flush(context.Background()) }()
	for range 200 {
		result, err := observationLog.Query("uid", observationTestHour, time.Time{}, nil, nil, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		whole := result.Total == len(minutes) && result.GapUntil == nil
		gone := result.Total == 0 && result.GapUntil != nil && result.GapUntil.Equal(observationTestHour.Add(time.Hour))
		if !whole && !gone {
			t.Fatalf("capture saw part of a deleted hour: total %d gap %v", result.Total, result.GapUntil)
		}
	}
	awaitObservationLogOperation(t, done)
}

// A power interval whose membership snapshot lies before local observation
// reports that start as its gap.
func TestOperatingPowerReportsGapForMissingBaseline(t *testing.T) {
	observationLog, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	from := observationTestHour.Add(10 * time.Minute)
	clock.set(from)
	node := NodeReference{Name: "node", UID: "node-uid"}
	snapshot := SchedulerSnapshot{At: from.Add(time.Second), Instances: []SchedulerInstance{{ID: "i", Node: node, Ready: true, WorkloadUID: "w"}}}
	if err := observationLog.SchedulerRecord(SchedulerObservation{At: snapshot.At, Snapshot: &snapshot}); err != nil {
		t.Fatal(err)
	}
	power, err := observationLog.OperatingPower(from, from.Add(time.Minute), nil, 10)
	if err != nil || power.GapUntil == nil || !power.GapUntil.Equal(observationTestHour) || power.Complete {
		t.Fatalf("missing baseline before local observation: %+v %v", power, err)
	}
	baseline := snapshot
	baseline.At = from.Add(-time.Second)
	if err := observationLog.SchedulerRecord(SchedulerObservation{At: baseline.At, Snapshot: &baseline}); err != nil {
		t.Fatal(err)
	}
	if power, err = observationLog.OperatingPower(from, from.Add(time.Minute), nil, 10); err != nil || power.GapUntil != nil {
		t.Fatalf("retained baseline reported a gap: %+v %v", power, err)
	}
}

// Publishing the same definition again, as every restart does, appends nothing.
func TestDefinitionRecordAppendsEachBodyOnce(t *testing.T) {
	config := ObservationLogConfig{Directory: t.TempDir()}
	observationLog, _ := clockedObservationLog(t, config, observationTestHour)
	definition := &Definition{Name: "workload", UID: "uid", Generation: 1, SLO: SLO{Percentile: 95, TargetLatency: time.Second}}
	if err := observationLog.DefinitionRecord(definition); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted, _ := clockedObservationLog(t, observationLog.config, observationTestHour)
	changed := *definition
	changed.SLO.TargetLatency = 2 * time.Second
	for _, d := range []*Definition{definition, definition, &changed} {
		if err := restarted.DefinitionRecord(d); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(restarted.root, observationDefinitionsFile))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 2 {
		t.Fatalf("definitions.jsonl has %d lines, want the first and the changed body", lines)
	}
}
