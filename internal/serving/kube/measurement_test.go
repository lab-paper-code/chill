package kube

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/internal/serving"
)

func TestIdleRejectsInferenceBetweenEmptySnapshots(t *testing.T) {
	node := serving.NodeReference{Name: "node", UID: "uid"}
	before := OperationState{Node: node, Pool: "pool", PowerMode: "fixed", Pods: []string{"pod"}, States: []string{"profile:[0]::1024:1"}, Native: map[string]native.State{"physical": {Ready: true, AcceptedRequests: 3, CompletedRequests: 3, Executions: 2}}}
	after := before
	after.Native = map[string]native.State{"physical": {Ready: true, AcceptedRequests: 4, CompletedRequests: 4, Executions: 3}}
	if err := stableIdle(before, before); err != nil {
		t.Fatal(err)
	}
	if err := stableIdle(before, after); err == nil {
		t.Fatal("empty queues hid actual inference inside idle window")
	}
	unloaded := OperationState{Node: node, Pool: "pool", PowerMode: "fixed", Native: map[string]native.State{}}
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
	record := OperationMeasurement{SchemaVersion: OperationMeasurementSchema, Verdict: "Accepted", WallJoules: &joules, Request: MeasurementRequest{Operation: Operation{ID: "prepare", Kind: "prepare", Node: serving.NodeReference{Name: "node", UID: "uid"}}, Meter: MeterDeclaration{ID: "meter", Scope: "node-exclusive-wall"}, Coverage: powerwindow.Policy{MaximumBoundaryDistance: time.Millisecond, MinimumCoverage: 1, MaximumGap: time.Second}}, Result: OperationResult{Status: "complete", StartedAt: start, EndedAt: start.Add(time.Second), Before: OperationState{States: []string{"other"}}, Actual: OperationState{States: []string{"other", "new:[0]::1024:1"}}}, Power: metricobserver.Result{Samples: []metricobserver.Sample{{ObservedAt: start, Watts: 20}, {ObservedAt: start.Add(time.Second), Watts: 20}}}}
	record.Result.Operation = record.Request.Operation
	record.Result.Before.PowerMode = "fixed"
	record.Result.Actual.PowerMode = "fixed"
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
func TestIsolationFailureCannotPersistAcceptedScientificCost(t *testing.T) {
	joules, watts := 12.0, 6.0
	node := serving.NodeReference{Name: "node", UID: "uid"}
	record := OperationMeasurement{SchemaVersion: OperationMeasurementSchema, Verdict: "Accepted", WallJoules: &joules, MeanWatts: &watts, Request: MeasurementRequest{Operation: Operation{Node: node}}, Result: OperationResult{EndedAt: time.Unix(10, 0)}}
	witness := MeasurementIsolation{Node: node, Pool: "measurement-test", CheckedAt: time.Unix(11, 0), Workloads: []serving.WorkloadReference{{Name: "late", UID: "late-uid"}}}
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
