package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
)

func TestPathModeConsumesRawRequestsAndReplaysChosenCandidate(t *testing.T) {
	at := time.Unix(100, 0)
	node := serving.NodeReference{Name: "node", UID: "node-uid"}
	d := &serving.Definition{UID: "w", Name: "workload", SLO: serving.SLO{Percentile: 100, TargetLatency: 100 * time.Millisecond},
		Model: serving.Model{Name: "model", Paths: []serving.ModelPath{{Name: "cpu", Artifact: "model", RuntimeFamily: "firstfloor", Backend: "ort-cpu"}}}}
	zero := int64(0)
	p := &serving.Profile{ID: "profile", Condition: profilederivation.CurvePlanSpec{NodeUID: node.UID, PowerMode: "fixed", LogicalModel: "model", RuntimeFamily: "firstfloor", Backend: "ort-cpu",
		Artifact: profilederivation.CurveArtifactContract{Name: "model", ExecutionPath: "cpu"}}, Batches: []serving.BatchProfile{{
		Batch: 1, MemoryBytes: 10, EnergyPerInputJoules: 1,
		ServiceSamples: []firstfloor.ServiceSample{{TrialID: "trial", ServiceDurationNanos: int64(10 * time.Millisecond)}, {TrialID: "trial", ServiceDurationNanos: int64(10 * time.Millisecond), PrecedingIdleNanos: &zero}},
	}}}
	i := serving.SchedulerInstance{ID: "instance", WorkloadUID: d.UID, Node: node, Profile: p, Ready: true, ObservedAt: at,
		Routable: map[string]bool{d.UID: true}, Weight: 1, MaxBatch: 1, Capacity: 10, Allocation: serving.SchedulerAllocation{MemoryBytes: 100}}
	snapshot := serving.SchedulerSnapshot{At: at, ObservationEnd: at, Workloads: []*serving.Definition{d},
		Nodes:     []serving.SchedulerNode{{Node: node, Ready: true, ObservedAt: at, PowerMode: "fixed", MemoryBytes: 1000}},
		Instances: []serving.SchedulerInstance{i}, Routing: map[string]map[string]int32{d.UID: {i.ID: 1}}}
	settings := serving.SchedulerSettings{ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second,
		MaxReplicas: 2, MaxInstances: 2, CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1, CapacityMinimumRequests: 1,
		CapacityGridSteps: 1, CapacityTailSamples: 1, CapacityBisectionWidth: .1,
		PlanSearchWorkers: 1, PlanSearchStrategy: "v1", PlanSearchEvaluations: 1, PlanExactFinalists: 1,
		DefaultOperationCost: map[string]serving.OperationCostValue{"prepare": {Seconds: 1}, "remove": {Seconds: 1}, "power": {Seconds: 1}},
		DefaultIdlePower:     &serving.IdlePowerDefault{NodeWatts: 1, PerInstanceWatts: 1}}
	accepted, dispatched, elapsed := int64(0), int64(time.Millisecond), int64(15*time.Millisecond)
	worker := time.Unix(86400, 0)
	finished := worker.Add(10 * time.Millisecond)
	native, frozen := 10.0, 2.0
	final := serving.RequestRow{Kind: serving.RequestRowFinal, RequestID: "a", WorkloadUID: d.UID, DefinitionID: d.Identity(), Node: &node, Instance: i.ID, ProfileID: p.ID,
		ReceivedAt: at.Add(-500 * time.Millisecond), AcceptedNanos: &accepted, DispatchedNanos: &dispatched, ElapsedNanos: &elapsed,
		Outcome: "completed", ExecutionComplete: true, NativeOutcome: "completed", NativeExecutionComplete: true,
		NativeQueuedAt: &worker, NativeRunningAt: &worker, NativeFinishedAt: &finished, NativeElapsedMS: &native,
		BatchID: 1, BatchSize: 1, ServiceDurationNanos: int64(10 * time.Millisecond), PrecedingIdleNanos: &zero, CompletionMarginMS: &frozen}
	final.ServerTiming.Record(serving.ServerSenderFreeze, 2*time.Millisecond)
	assigned := final
	assigned.Kind, assigned.ElapsedNanos, assigned.ExecutionComplete, assigned.Outcome = serving.RequestRowAssigned, nil, false, ""
	open := assigned
	open.RequestID, open.ReceivedAt, open.BatchID = "b", at.Add(-200*time.Millisecond), 0
	late := serving.RequestRow{Kind: serving.RequestRowNative, RequestID: "a", WorkloadUID: d.UID, DefinitionID: d.Identity(), Instance: i.ID,
		BatchID: 1, BatchSize: 1, NativeExecutionComplete: true, NativeOutcome: "completed", ServiceDurationNanos: int64(10 * time.Millisecond), PrecedingIdleNanos: &zero}
	dir := t.TempDir()
	requestsFile := filepath.Join(dir, "requests.jsonl")
	file, err := os.Create(requestsFile)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	// A late assigned row must not overwrite final. Native never replaces HTTP.
	for _, row := range []serving.RequestRow{assigned, final, assigned, open, late} {
		if err := encoder.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	rowsFile := filepath.Join(dir, "rows.json")
	rows, err := json.Marshal([]serving.CapacityRow{{Grid: []serving.CapacityJudgment{{PercentileMS: []float64{-1}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rowsFile, rows, 0600); err != nil {
		t.Fatal(err)
	}
	report, err := pathAssessmentReport(settings, snapshot, rowsFile, requestsFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ReplayReasons) != 0 || report.Requests != 2 || len(report.Observed) != 1 || len(report.PredictedSLO) != 1 {
		t.Fatalf("raw request/planner/replay integration failed: %+v", report)
	}
	predicted, observed := report.PredictedSLO[0], report.Observed[0]
	if predicted.Samples == 0 || predicted.ExpectedNanos == nil || *predicted.ExpectedNanos <= 0 || observed.Completed != 1 || observed.Incomplete != 1 {
		t.Fatalf("predicted and observed populations conflated or grid sentinel used: %+v", report)
	}
	if observed.PathSamples != 1 || observed.FrozenPostExceedances != 1 || observed.Physical.Batches != 1 || observed.Physical.ComparedBatches != 1 {
		t.Fatalf("consumer lost frozen POST/direct physical evidence: %+v", observed)
	}
}

func TestPathRawLoaderRejectsArrivalProjectionAndKeepsMissingEvidence(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "requests.jsonl")
	if err := os.WriteFile(name, []byte("{\"id\":\"arrival\",\"at\":\"2026-11-08T00:00:00Z\",\"compute\":true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPathRequests(name); err == nil {
		t.Fatal("SchedulerArrival projection accepted as raw request evidence")
	}
	data, err := json.Marshal(serving.RequestRow{Kind: serving.RequestRowAssigned, RequestID: "open", ReceivedAt: time.Unix(100, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := readPathRequests(name)
	if err != nil || len(rows) != 1 || rows[0].ElapsedNanos != nil || rows[0].CompletionMarginMS != nil || rows[0].NativeQueuedAt != nil {
		t.Fatalf("missing evidence manufactured: %+v %v", rows, err)
	}
}
