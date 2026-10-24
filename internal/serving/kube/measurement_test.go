package kube

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

func TestIdleRejectsInferenceBetweenEmptySnapshots(t *testing.T) {
	node := serving.NodeReference{Name: "node", UID: "uid"}
	before := OperationState{Node: node, PowerMode: "fixed", Pods: []workloadobserver.Pod{{UID: "pod"}}, States: []string{"profile:[0]::1024:1"}, Native: map[string]native.State{"physical": {Ready: true, AcceptedRequests: 3, CompletedRequests: 3, Executions: 2}}}
	after := before
	after.Native = map[string]native.State{"physical": {Ready: true, AcceptedRequests: 4, CompletedRequests: 4, Executions: 3}}
	if err := stableIdle(before, before); err != nil {
		t.Fatal(err)
	}
	if err := stableIdle(before, after); err == nil {
		t.Fatal("empty queues hid actual inference inside idle window")
	}
	unloaded := OperationState{Node: node, PowerMode: "fixed", Native: map[string]native.State{}}
	if err := stableIdle(unloaded, unloaded); err != nil {
		t.Fatal("actual unloaded idle rejected", err)
	}
	recreated := before
	recreated.Node.UID = node.UID + "-recreated"
	if err := stableIdle(before, recreated); err == nil {
		t.Fatal("Node identity change accepted as idle stability")
	}
}
func TestOperationCostUsesActualSingleStateEffectAndWholeWallWindow(t *testing.T) {
	start := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	joules := 20.0
	record := OperationMeasurement{
		SchemaVersion: OperationMeasurementSchema, Verdict: measurementInsufficient,
		Request: MeasurementRequest{
			Operation: Operation{ID: "prepare", Kind: operationPrepare, Node: serving.NodeReference{Name: "node", UID: "uid"}},
			Meter:     MeterDeclaration{ID: "meter", Scope: measurementWallScope},
			Timing:    &edge.MeasurementTimingSpec{ChangeIntervalP95Seconds: "1", MaximumRequestSeconds: "0", MaximumReceiptGapSeconds: "0", Source: "synthetic empirical fixture"},
			Coverage:  powerwindow.Policy{MaximumGap: time.Second},
		},
		Result: OperationResult{Status: nativeOperationComplete, StartedAt: start, EndedAt: start.Add(time.Second),
			Before: OperationState{States: []string{"other"}}, Actual: OperationState{States: []string{"other", "new:[0]::1024:1"}}},
		Power: metricobserver.Result{Samples: []metricobserver.Sample{
			{ObservedAt: start.Add(-time.Second), Watts: 20}, {ObservedAt: start, Watts: 20},
			{ObservedAt: start.Add(time.Second), Watts: 20}, {ObservedAt: start.Add(2 * time.Second), Watts: 20},
		}},
	}
	record.Result.Operation = record.Request.Operation
	record.Result.Before.PowerMode = "fixed"
	record.Result.Actual.PowerMode = "fixed"
	if err := record.judge(nil, nil); err != nil || record.Verdict != measurementAccepted || record.WallJoules == nil || *record.WallJoules != joules {
		t.Fatalf("invalid current-contract operation fixture: %+v, %v", record, err)
	}
	cost, err := measurementCost(record, evidencestore.ObjectIdentity{Store: "store", Key: "raw", Size: 100})
	if err != nil {
		t.Fatal(err)
	}
	if cost.From != "unloaded" || cost.To != "new:[0]::1024:1" || cost.Trials[0].Duration != time.Second || cost.Trials[0].Joules != 20 || cost.EnergyScope != "whole-wall" {
		t.Fatalf("time/energy/state meaning changed %+v", cost)
	}
	record.Power.Samples = record.Power.Samples[:1]
	if _, err = measurementCost(record, cost.Reference); err == nil {
		t.Fatal("too-short unknown energy silently accepted as zero")
	}
	record.Result.Actual.States = append(record.Result.Actual.States, "unexpected")
	if _, _, err = operationCostKeys(record.Result); err == nil {
		t.Fatal("multiple effects relabeled as one atomic preparation")
	}
}

// A measured power change on the empty Node is its re-entry transition cost.
func TestPowerCostUsesEmptyNodeModeChange(t *testing.T) {
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	joules := 30.0
	record := OperationMeasurement{
		SchemaVersion: OperationMeasurementSchema, Verdict: measurementInsufficient,
		Request: MeasurementRequest{
			Operation: Operation{ID: "raise", Kind: operationPower, Node: serving.NodeReference{Name: "node", UID: "uid"}},
			Meter:     MeterDeclaration{ID: "meter", Scope: measurementWallScope},
			Timing:    &edge.MeasurementTimingSpec{ChangeIntervalP95Seconds: "1", MaximumRequestSeconds: "0", MaximumReceiptGapSeconds: "0", Source: "synthetic empirical fixture"},
			Coverage:  powerwindow.Policy{MaximumGap: time.Second},
		},
		Result: OperationResult{Status: nativeOperationComplete, StartedAt: start, EndedAt: start.Add(2 * time.Second),
			Before: OperationState{PowerMode: lowMode}, Actual: OperationState{PowerMode: "boost"}},
		Power: metricobserver.Result{Samples: []metricobserver.Sample{
			{ObservedAt: start.Add(-time.Second), Watts: 15}, {ObservedAt: start, Watts: 15},
			{ObservedAt: start.Add(time.Second), Watts: 15}, {ObservedAt: start.Add(2 * time.Second), Watts: 15},
			{ObservedAt: start.Add(3 * time.Second), Watts: 15},
		}},
	}
	record.Result.Operation = record.Request.Operation
	if err := record.judge(nil, nil); err != nil || record.Verdict != measurementAccepted || record.WallJoules == nil || *record.WallJoules != joules {
		t.Fatalf("invalid current-contract power fixture: %+v, %v", record, err)
	}
	cost, err := measurementCost(record, evidencestore.ObjectIdentity{Store: "store", Key: "raw", Size: 100})
	if err != nil {
		t.Fatal(err)
	}
	if cost.Kind != operationPower || cost.From != lowMode || cost.To != "boost" || len(cost.BeforeState) != 0 || len(cost.TargetState) != 0 || cost.Trials[0].Joules != 30 || cost.Trials[0].Duration != 2*time.Second {
		t.Fatalf("empty-Node power cost %+v", cost)
	}
	judged := record
	judged.Verdict, judged.WallJoules = measurementInsufficient, nil
	if err = judged.judge(nil, nil); err != nil || judged.Verdict != measurementAccepted || *judged.WallJoules != joules {
		t.Fatalf("empty-Node power change not accepted when measured: %+v", judged)
	}
	record.Result.Actual.PowerMode = record.Result.Before.PowerMode
	if _, err = measurementCost(record, cost.Reference); err == nil {
		t.Fatal("power record without a mode change became a transition cost")
	}
	// A loaded power change belongs to PowerModeTransitionCost.
	loaded := []string{"new:[0]::1024:1"}
	record.Result.Before.States, record.Result.Actual = loaded, OperationState{PowerMode: "boost", States: loaded}
	if _, err = measurementCost(record, cost.Reference); err == nil {
		t.Fatal("loaded power record became a serving-operation transition cost")
	}
	record.Verdict, record.WallJoules = measurementInsufficient, nil
	if err = record.judge(nil, nil); err != nil || record.Verdict == measurementAccepted {
		t.Fatal("loaded power change accepted when measured")
	}
}
func TestResizeRemovedSourceNeedsRetainedActualZeroInferenceProof(t *testing.T) {
	result := OperationResult{Operation: Operation{Kind: operationResize}, Before: OperationState{Native: map[string]native.State{"physical": {Ready: true, AcceptedRequests: 5, CompletedRequests: 5, Executions: 3}}}, Actual: OperationState{Native: map[string]native.State{"replacement": {Ready: true}}}}
	if err := operationZeroInference(result); err == nil {
		t.Fatal("removed process disappearance invented zero-inference proof")
	}
	result.DrainNative = map[string]native.State{"physical": {Ready: true, AcceptedRequests: 5, CompletedRequests: 5, Executions: 3}}
	if err := operationZeroInference(result); err != nil {
		t.Fatal(err)
	}
	result.DrainNative["physical"] = native.State{Ready: true, AcceptedRequests: 6, CompletedRequests: 6, Executions: 4}
	if err := operationZeroInference(result); err == nil {
		t.Fatal("serving-inclusive wall cost would be double counted by replay")
	}
}
func TestMeasuredOperationNeedsProvenStateEffect(t *testing.T) {
	start := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	measured := func(before, actual []string) OperationMeasurement {
		record := OperationMeasurement{
			Verdict: measurementInsufficient,
			Request: MeasurementRequest{Operation: Operation{ID: "remove", Kind: operationRemove},
				Timing:   &edge.MeasurementTimingSpec{ChangeIntervalP95Seconds: "1", MaximumRequestSeconds: "0", MaximumReceiptGapSeconds: "0", Source: "synthetic empirical fixture"},
				Coverage: powerwindow.Policy{MaximumGap: time.Second}},
			Result: OperationResult{Status: nativeOperationComplete, StartedAt: start, EndedAt: start.Add(time.Second),
				Before: OperationState{States: before}, Actual: OperationState{States: actual}},
			Power: metricobserver.Result{Samples: []metricobserver.Sample{
				{ObservedAt: start.Add(-time.Second), Watts: 20}, {ObservedAt: start, Watts: 20},
				{ObservedAt: start.Add(time.Second), Watts: 20}, {ObservedAt: start.Add(2 * time.Second), Watts: 20},
			}},
		}
		record.Result.Operation = record.Request.Operation
		if err := record.judge(nil, nil); err != nil {
			t.Fatal(err)
		}
		return record
	}
	// The runner was already gone: before and after are both empty.
	if record := measured(nil, nil); record.Verdict != measurementInsufficient || record.WallJoules != nil || record.Reason == "" {
		t.Fatalf("remove without a state effect was accepted: %+v", record)
	}
	if record := measured([]string{"old:[0]::1024:1"}, nil); record.Verdict != measurementAccepted || record.WallJoules == nil || *record.WallJoules != 20 {
		t.Fatalf("remove with its state effect was rejected: %+v", record)
	}
}
func TestIsolationFailureCannotPersistAcceptedScientificCost(t *testing.T) {
	joules, watts := 12.0, 6.0
	node := serving.NodeReference{Name: "node", UID: "uid"}
	record := OperationMeasurement{SchemaVersion: OperationMeasurementSchema, Verdict: "Accepted", WallJoules: &joules, MeanWatts: &watts, Request: MeasurementRequest{Operation: Operation{Node: node}}, Result: OperationResult{EndedAt: time.Unix(10, 0)}}
	witness := MeasurementIsolation{Node: node, Tainted: true, CheckedAt: time.Unix(11, 0), Workloads: []serving.WorkloadReference{{Name: "late", UID: "late-uid"}}}
	if err := record.applyIsolation(witness, errors.New("actual new Workload ownership")); err == nil {
		t.Fatal("ownership violation ignored")
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var stored OperationMeasurement
	if err = json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Verdict != "Insufficient" || stored.WallJoules != nil || stored.MeanWatts != nil || stored.Isolation == nil || stored.Isolation.Error == "" || len(stored.Isolation.Workloads) != 1 {
		t.Fatal("failed final isolation remained accepted", stored)
	}
}

func TestIsolationRejectsUntaintedWitness(t *testing.T) {
	joules := 12.0
	node := serving.NodeReference{Name: "node", UID: "uid"}
	for _, tainted := range []bool{true, false} {
		record := OperationMeasurement{Verdict: "Accepted", WallJoules: &joules, Request: MeasurementRequest{Operation: Operation{Node: node}}, Result: OperationResult{EndedAt: time.Unix(10, 0)}}
		err := record.applyIsolation(MeasurementIsolation{Node: node, Tainted: tainted, CheckedAt: time.Unix(11, 0)}, nil)
		if (err == nil) != tainted || (record.WallJoules == nil) != !tainted {
			t.Fatalf("tainted=%v: err=%v joules=%v", tainted, err, record.WallJoules)
		}
	}
}

func timingMeasurementFixture() OperationMeasurement {
	start := time.Unix(100, 0).UTC()
	operation := Operation{ID: "remove", Kind: operationRemove, Node: testNode}
	record := OperationMeasurement{
		SchemaVersion: OperationMeasurementSchema, Verdict: measurementInsufficient,
		Request: MeasurementRequest{Operation: operation, Meter: MeterDeclaration{ID: "meter", Scope: measurementWallScope},
			Timing:   &edge.MeasurementTimingSpec{ChangeIntervalP95Seconds: "1", MaximumRequestSeconds: "0", MaximumReceiptGapSeconds: "0", Source: "empirical fixture"},
			Coverage: powerwindow.Policy{MaximumGap: 3 * time.Second}},
		Result: OperationResult{Operation: operation, Status: nativeOperationComplete, StartedAt: start, EndedAt: start.Add(time.Second),
			Before: OperationState{PowerMode: "fixed", States: []string{"old"}}, Actual: OperationState{PowerMode: "fixed"}},
	}
	for i, watts := range []float64{0, 2, 0, 2} {
		record.Power.Samples = append(record.Power.Samples, metricobserver.Sample{ObservedAt: start.Add(time.Duration(i-1) * time.Second), Watts: watts})
	}
	return record
}

func TestTimingMeasurementAcceptsHighUncertaintyAndProjectsSeparateB(t *testing.T) {
	record := timingMeasurementFixture()
	if err := record.judge(nil, nil); err != nil || record.Verdict != measurementAccepted || record.WallJoules == nil || *record.WallJoules != 1 || record.EnergyUncertaintyJoules == nil || *record.EnergyUncertaintyJoules != 0.5 || record.EnergyUncertaintyRatio == nil || *record.EnergyUncertaintyRatio != 0.5 {
		t.Fatalf("50%% uncertainty must remain a valid measurement: %+v, %v", record, err)
	}
	cost, err := measurementCost(record, evidencestore.ObjectIdentity{Store: "evidence", Key: "raw", Size: 1})
	if err != nil || cost.Trials[0].Joules != 1 || cost.Trials[0].EnergyUncertaintyJoules != 0.5 {
		t.Fatalf("nominal/B projection: %+v, %v", cost, err)
	}
	*record.EnergyUncertaintyJoules = 0
	if _, err := measurementCost(record, cost.Reference); err == nil {
		t.Fatal("tampered stored uncertainty was not reproduced from raw")
	}
}

func TestTimingMeasurementZeroConstantIsAcceptedWithUndefinedRatio(t *testing.T) {
	record := timingMeasurementFixture()
	for i := range record.Power.Samples {
		record.Power.Samples[i].Watts = 0
	}
	if err := record.judge(nil, nil); err != nil || record.Verdict != measurementAccepted || record.WallJoules == nil || *record.WallJoules != 0 || record.EnergyUncertaintyJoules == nil || *record.EnergyUncertaintyJoules != 0 || record.EnergyUncertaintyRatio != nil {
		t.Fatalf("zero constant trace: %+v, %v", record, err)
	}
}

func TestTimingMeasurementCorrectsFailuresAndLongGapsWithoutCoverageVeto(t *testing.T) {
	for _, failed := range []bool{true, false} {
		record := timingMeasurementFixture()
		if failed {
			record.Power.Failures = []metricobserver.Failure{{ObservedAt: record.Result.StartedAt.Add(time.Second / 2), Reason: metricobserver.FailureReasonTimeout}}
		} else {
			record.Request.Coverage.MaximumGap = time.Second / 2
		}
		if err := record.judge(nil, nil); err != nil || record.Verdict != measurementAccepted || record.MissingGapJoules == nil || *record.MissingGapJoules != 2 || *record.EnergyUncertaintyJoules != 2.5 {
			t.Fatalf("failed=%v: %+v, %v", failed, record, err)
		}
	}
}

func TestTimingMeasurementRejectsMissingRealSupportButKeepsPhysicalFailure(t *testing.T) {
	record := timingMeasurementFixture()
	record.Power.Samples = record.Power.Samples[1:]
	if err := record.judge(nil, nil); err != nil || record.Verdict != measurementInsufficient || record.WallJoules != nil || record.Reason == "" {
		t.Fatalf("missing actual support: %+v, %v", record, err)
	}
	record = timingMeasurementFixture()
	record.Power.Failures = []metricobserver.Failure{{ObservedAt: record.Power.Samples[1].ObservedAt, Reason: metricobserver.FailureReasonTimeout}}
	if err := record.judge(nil, nil); err != nil || record.Verdict != measurementInsufficient || record.WallJoules != nil {
		t.Fatalf("conflicting raw attempt timestamps accepted: %+v, %v", record, err)
	}
	record = timingMeasurementFixture()
	physical := errors.New("physical removal failed")
	if err := record.judge(physical, nil); !errors.Is(err, physical) || record.Verdict != operationFailed {
		t.Fatalf("actual operation failure changed lifecycle: %+v, %v", record, err)
	}
}

func TestMeasurementReadersRequireEmpiricalTiming(t *testing.T) {
	for _, kind := range []string{operationRemove, operationIdle} {
		t.Run(kind, func(t *testing.T) {
			record := timingMeasurementFixture()
			if kind == operationIdle {
				record.Request.Operation.Kind, record.Result.Operation.Kind = kind, kind
				record.Result.Before, record.Result.Actual = OperationState{PowerMode: "fixed"}, OperationState{PowerMode: "fixed"}
				record.StateObservations = []OperationState{record.Result.Before, record.Result.Actual}
			}
			if err := record.judge(nil, nil); err != nil || record.Verdict != measurementAccepted {
				t.Fatalf("current-contract fixture was rejected: %+v, %v", record, err)
			}
			if kind == operationIdle {
				if _, err := idleMeasurementBaseline(record, evidencestore.ObjectIdentity{}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := measurementCost(record, evidencestore.ObjectIdentity{}); err != nil {
				t.Fatal(err)
			}
			record.Request.Timing = nil
			if _, err := record.energyMeasurement(); err == nil {
				t.Fatal("missing empirical timing silently produced energy with B=0")
			}
			if kind == operationIdle {
				if _, err := idleMeasurementBaseline(record, evidencestore.ObjectIdentity{}); err == nil {
					t.Fatal("missing empirical timing became a zero-uncertainty baseline")
				}
			} else if _, err := measurementCost(record, evidencestore.ObjectIdentity{}); err == nil {
				t.Fatal("missing empirical timing became a zero-uncertainty operation cost")
			}
		})
	}
}

func TestTimingAllowancePreservesFractionalNanosecondInputs(t *testing.T) {
	timing := &edge.MeasurementTimingSpec{ChangeIntervalP95Seconds: "1.0339932335", MaximumRequestSeconds: "0.248723980", MaximumReceiptGapSeconds: "0.325263383", Source: "completed 120s observation; empirical Gen3 transfer"}
	allowance, err := measurementTimingAllowance(timing)
	if err != nil || allowance != 1607980597*time.Nanosecond || timing.ChangeIntervalP95Seconds != "1.0339932335" {
		t.Fatalf("frozen quantile/sum: %s %+v, %v", allowance, timing, err)
	}
	for _, raw := range []string{"NaN", "Inf", "-1", "1/2", ""} {
		timing.ChangeIntervalP95Seconds = raw
		if _, err := measurementTimingAllowance(timing); err == nil {
			t.Fatalf("invalid timing %q accepted", raw)
		}
	}
}

type measurementTestSource struct {
	identity       metricobserver.SourceIdentity
	failAfterFirst bool
	reads          int
}

func (source *measurementTestSource) Identity() metricobserver.SourceIdentity { return source.identity }
func (source *measurementTestSource) ReadMetrics(context.Context) (metricobserver.Reading, error) {
	source.reads++
	if source.failAfterFirst && source.reads > 1 {
		return metricobserver.Reading{}, errors.New("actual meter read failed")
	}
	return metricobserver.Reading{Watts: 0}, nil
}

func TestMeasureCollectsActualExpandedSupportAndRetainsRealBracketFailure(t *testing.T) {
	for _, mode := range []string{"valid", "read failure", "operation timeout"} {
		fail := mode == "read failure"
		h := newAdmissionHarness(t)
		_, _, _, class := observerSourceFixture()
		h.create(&class)
		source := &measurementTestSource{identity: metricobserver.SourceIdentity{Kind: "fixture", NodeName: testNode.Name, Endpoint: "http://meter", Metric: "power"}, failAfterFirst: fail}
		writer := managerArchive{directory: t.TempDir()}
		request := MeasurementRequest{Operation: Operation{ID: "idle", Kind: operationIdle, Node: testNode},
			Meter: MeterDeclaration{ID: "meter", Node: testNode, Scope: measurementWallScope, Source: source.Identity()}, Store: "evidence",
			Timing:          &edge.MeasurementTimingSpec{ChangeIntervalP95Seconds: "0.005", MaximumRequestSeconds: "0", MaximumReceiptGapSeconds: "0", Source: "empirical fixture"},
			Sampling:        metricobserver.Request{Interval: 2 * time.Millisecond, RequestTimeout: time.Millisecond, Duration: time.Second, MaxEvents: 503},
			OperationBudget: 500 * time.Millisecond, IdleDuration: 20 * time.Millisecond, Coverage: powerwindow.Policy{MaximumGap: 10 * time.Millisecond}}
		if mode == "operation timeout" {
			request.OperationBudget = 10 * time.Millisecond
			request.IdleDuration = 500 * time.Millisecond
		}
		record, receipt, err := h.controller.measurement.Measure(context.Background(), request, source, writer, nil)
		raw, readErr := os.ReadFile(filepath.Join(writer.directory, receipt.Object.Key))
		var retained OperationMeasurement
		if receipt.Object.Key == "" || readErr != nil || json.Unmarshal(raw, &retained) != nil || retained.Request.Operation.ID != request.Operation.ID || retained.Verdict != record.Verdict {
			t.Fatalf("physical action/raw evidence lost: %+v receipt %+v, read %v, measure %v", record, receipt, readErr, err)
		}
		if mode == "operation timeout" {
			if !errors.Is(err, context.DeadlineExceeded) || record.Result.Status != nativeOperationFailed || record.Verdict != operationFailed {
				t.Fatalf("finite physical operation budget not enforced: %+v, %v", record, err)
			}
			continue
		}
		if record.Result.Status != nativeOperationComplete {
			t.Fatalf("physical idle failed: %+v, %v", record, err)
		}
		if fail {
			if err == nil || record.Verdict != measurementInsufficient || record.WallJoules != nil || len(record.Power.Failures) == 0 {
				t.Fatalf("real bracket failure fabricated an accepted endpoint: %+v, %v", record, err)
			}
			continue
		}
		first, last := record.Power.Samples[0].ObservedAt, record.Power.Samples[len(record.Power.Samples)-1].ObservedAt
		if err != nil || record.Verdict != measurementAccepted || first.After(record.Result.StartedAt.Add(-5*time.Millisecond)) || last.Before(record.Result.EndedAt.Add(5*time.Millisecond)) || record.EnergyUncertaintyJoules == nil || *record.EnergyUncertaintyJoules != 0 {
			t.Fatalf("real expanded support missing: %+v, %v", record, err)
		}
	}
}
