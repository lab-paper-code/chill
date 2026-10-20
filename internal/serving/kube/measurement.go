package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/internal/serving"
)

const OperationMeasurementSchema = "chill.dacs.io/serving-operation-measurement.v1alpha2"
const measurementInsufficient = "Insufficient"

// MeterDeclaration is explicit evidence scope, never inferred from an URL.
type MeterDeclaration struct {
	ID     string                        `json:"id"`
	Node   serving.NodeReference         `json:"node"`
	Scope  string                        `json:"scope"`
	Source metricobserver.SourceIdentity `json:"source"`
}
type MeasurementRequest struct {
	Operation Operation              `json:"operation"`
	Meter     MeterDeclaration       `json:"meter"`
	Sampling  metricobserver.Request `json:"sampling"`
	Coverage  powerwindow.Policy     `json:"coverage"`
	// Store is the logical store; the key is OperationMeasurementKey.
	Store        string        `json:"store"`
	IdleDuration time.Duration `json:"idleDurationNanos,omitempty"`
}
type OperationMeasurement struct {
	SchemaVersion     string                `json:"schemaVersion"`
	Request           MeasurementRequest    `json:"request"`
	Result            OperationResult       `json:"result"`
	Power             metricobserver.Result `json:"power"`
	StateObservations []OperationState      `json:"stateObservations"`
	Verdict           string                `json:"verdict"`
	Reason            string                `json:"reason,omitempty"`
	WallJoules        *float64              `json:"wallJoules,omitempty"`
	MeanWatts         *float64              `json:"meanWatts,omitempty"`
	Isolation         *MeasurementIsolation `json:"isolation,omitempty"`
}

// MeasurementIsolation witnesses the measured Node at the end: it still carries
// the measurement POOL taint and no Workload uses the measured models.
type MeasurementIsolation struct {
	Node      serving.NodeReference       `json:"node"`
	Tainted   bool                        `json:"tainted"`
	Workloads []serving.WorkloadReference `json:"workloads"`
	CheckedAt time.Time                   `json:"checkedAt"`
	Error     string                      `json:"error,omitempty"`
}

// Measure surrounds the SAME production operation, beginning only after an
// actual successful read. Failure records are immutable too; insufficient
// windows never become a zero-valued accepted cost.
func (o *Operations) Measure(ctx context.Context, request MeasurementRequest, source metricobserver.Source, writer evidencestore.ImmutableWriter, finalIsolation func(context.Context) (MeasurementIsolation, error)) (OperationMeasurement, evidencestore.Receipt, error) {
	record := OperationMeasurement{SchemaVersion: OperationMeasurementSchema, Request: request, Verdict: measurementInsufficient}
	if err := validateMeasurementRequest(request, source, writer); err != nil {
		return record, evidencestore.Receipt{}, err
	}
	observer, err := metricobserver.New(source)
	if err != nil {
		return record, evidencestore.Receipt{}, err
	}
	sampleCtx, cancelSamples := context.WithCancel(ctx)
	defer cancelSamples()
	operationCtx, cancelOperation := context.WithTimeout(ctx, request.Sampling.Duration)
	defer cancelOperation()
	started := make(chan struct{})
	done := make(chan struct{})
	var operationErr error
	power, observeErr := observer.Observe(sampleCtx, request.Sampling, func() {
		close(started)
		go func() {
			defer close(done)
			defer cancelSamples()
			if request.Operation.Kind == operationIdle {
				record.Result, record.StateObservations, operationErr = o.measureIdle(operationCtx, request.Operation, request.IdleDuration, request.Sampling.Interval)
			} else {
				record.Result, operationErr = o.Execute(operationCtx, request.Operation)
			}
		}()
	})
	record.Power = power
	select {
	case <-started:
		<-done
	default:
		record.Reason = "no successful first power sample; operation was not invoked"
	}
	operationErr = record.judge(operationErr, observeErr)
	if finalIsolation != nil {
		checkCtx, cancelCheck := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		witness, isolationErr := finalIsolation(checkCtx)
		cancelCheck()
		operationErr = errors.Join(operationErr, record.applyIsolation(witness, isolationErr))
	}
	raw, encodeErr := json.Marshal(record)
	if encodeErr != nil {
		return record, evidencestore.Receipt{}, encodeErr
	}
	identity := evidencestore.ObjectIdentity{Store: request.Store, Key: OperationMeasurementKey(request.Operation), Size: int64(len(raw))}
	// A cancelled caller must not discard already collected failed/partial facts.
	writeCtx, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancelWrite()
	receipt, writeErr := writer.PutImmutable(writeCtx, identity, bytes.NewReader(raw))
	if writeErr == nil {
		writeErr = receipt.Validate()
		if writeErr == nil && receipt.Object != identity {
			writeErr = errors.New("immutable operation receipt has different object identity")
		}
	}
	return record, receipt, errors.Join(operationErr, observeErr, writeErr)
}

// judge sets the verdict from the actual result and power window. A generic
// operation is Accepted only with the primitive state effect its cost needs.
func (record *OperationMeasurement) judge(operationErr, observeErr error) error {
	generic := record.Request.Operation.Kind != operationIdle
	if operationErr == nil && generic {
		operationErr = operationZeroInference(record.Result)
	}
	if record.Result.StartedAt.IsZero() {
		record.Verdict = measurementInsufficient
	} else if operationErr != nil {
		record.Verdict = operationFailed
		record.Reason = operationErr.Error()
	} else {
		var effectErr error
		if generic {
			_, _, effectErr = operationCostKeys(record.Result)
		}
		samples := make([]powerwindow.Sample, len(record.Power.Samples))
		for i, s := range record.Power.Samples {
			samples[i] = powerwindow.Sample{ObservedAt: s.ObservedAt, Watts: s.Watts}
		}
		joules, integrationErr := powerwindow.Integrate(powerwindow.Window{Start: record.Result.StartedAt, End: record.Result.EndedAt, Samples: samples}, record.Request.Coverage)
		if effectErr != nil {
			record.Reason = effectErr.Error()
		} else if integrationErr != nil {
			record.Reason = integrationErr.Error()
		} else if observeErr != nil {
			record.Reason = observeErr.Error()
		} else {
			record.Verdict = measurementAccepted
			record.WallJoules = &joules
			if record.Request.Operation.Kind == operationIdle {
				watts := joules / record.Result.EndedAt.Sub(record.Result.StartedAt).Seconds()
				record.MeanWatts = &watts
			}
		}
	}
	return operationErr
}
func validateMeasurementRequest(request MeasurementRequest, source metricobserver.Source, writer evidencestore.ImmutableWriter) error {
	if writer == nil || source == nil {
		return errors.New("actual metric source and immutable writer required")
	}
	if err := request.Sampling.Validate(); err != nil {
		return err
	}
	if strings.ContainsAny(request.Operation.Node.UID+request.Operation.ID, "/\\") {
		return errors.New("operation ID and Node UID must be single key components")
	}
	if err := (evidencestore.ObjectLocation{Store: request.Store, Key: OperationMeasurementKey(request.Operation)}).Validate(); err != nil {
		return err
	}
	if request.Meter.ID == "" || request.Meter.Scope != measurementWallScope || request.Meter.Node != request.Operation.Node || !reflect.DeepEqual(request.Meter.Source, source.Identity()) || source.Identity().NodeName != request.Operation.Node.Name {
		return errors.New("exact node-exclusive wall meter declaration required")
	}
	if request.Coverage.MaximumBoundaryDistance <= 0 || request.Coverage.MaximumGap <= 0 || request.Coverage.MinimumCoverage <= 0 || request.Coverage.MinimumCoverage > 1 {
		return errors.New("valid explicit power coverage policy required")
	}
	return nil
}
func stableIdle(before, after OperationState) error {
	if before.Node != after.Node || before.PowerMode == "" || before.PowerMode != after.PowerMode || !reflect.DeepEqual(before.Pods, after.Pods) || !reflect.DeepEqual(before.States, after.States) || len(before.Reasons) > 0 || len(after.Reasons) > 0 || len(before.Native) != len(after.Native) {
		return errors.New("idle physical state changed or native observation incomplete")
	}
	for id, a := range before.Native {
		b, ok := after.Native[id]
		if !ok || !a.Ready || !b.Ready || len(a.Requests) != 0 || len(b.Requests) != 0 || a.AcceptedRequests != b.AcceptedRequests || a.CompletedRequests != b.CompletedRequests || a.Executions != b.Executions || !reflect.DeepEqual(a.Metadata, b.Metadata) {
			return fmt.Errorf("idle zero-inference/state stability unconfirmed for %s", id)
		}
	}
	for _, i := range after.Instances {
		for _, counts := range []map[string]int{i.AssignedUnknown, i.Waiting, i.Running} {
			for _, count := range counts {
				if count != 0 {
					return errors.New("idle ingress/native outstanding work")
				}
			}
		}
	}
	return nil
}
func (o *Operations) measureIdle(ctx context.Context, operation Operation, duration, interval time.Duration) (OperationResult, []OperationState, error) {
	result := OperationResult{Operation: operation, StartedAt: time.Now().UTC(), Status: nativeOperationRunning}
	var observations []OperationState
	if duration <= 0 {
		return result, nil, errors.New("idle duration must be positive")
	}
	before, err := o.Capture(ctx, operation.Node)
	result.Before = before
	observations = append(observations, before)
	if err == nil {
		err = stableIdle(before, before)
	}
	result.StartedAt = time.Now().UTC()
	if err == nil {
		deadline := time.NewTimer(duration)
		defer deadline.Stop()
		ticks := time.NewTicker(interval)
		defer ticks.Stop()
	loop:
		for {
			select {
			case <-ctx.Done():
				err = ctx.Err()
				break loop
			case <-deadline.C:
				break loop
			case <-ticks.C:
				state, e := o.Capture(ctx, operation.Node)
				observations = append(observations, state)
				if e == nil {
					e = stableIdle(before, state)
				}
				if e != nil {
					err = e
					break loop
				}
			}
		}
	}
	after, e := o.Capture(ctx, operation.Node)
	result.Actual = after
	observations = append(observations, after)
	if e == nil {
		e = stableIdle(before, after)
	}
	err = errors.Join(err, e)
	result.EndedAt = time.Now().UTC()
	result.Status = nativeOperationComplete
	if err != nil {
		result.Status = nativeOperationFailed
		result.Error = err.Error()
	}
	return result, observations, err
}
func measurementCost(record OperationMeasurement, reference evidencestore.ObjectIdentity) (serving.OperationCost, error) {
	if record.SchemaVersion != OperationMeasurementSchema || record.Verdict != measurementAccepted || record.Result.Status != nativeOperationComplete || record.WallJoules == nil || math.IsNaN(*record.WallJoules) || math.IsInf(*record.WallJoules, 0) || *record.WallJoules < 0 || !record.Result.EndedAt.After(record.Result.StartedAt) {
		return serving.OperationCost{}, errors.New("operation cost lacks accepted actual window")
	}
	if record.Request.Operation.Kind == operationIdle {
		return serving.OperationCost{}, errors.New("idle baselines use their distinct cost contract")
	}
	if err := operationZeroInference(record.Result); err != nil {
		return serving.OperationCost{}, err
	}
	if record.Request.Operation.Kind != operationPower && (record.Result.Before.PowerMode == "" || record.Result.Before.PowerMode != record.Result.Actual.PowerMode) {
		return serving.OperationCost{}, errors.New("generic operation host power condition unknown or changed")
	}
	samples := make([]powerwindow.Sample, len(record.Power.Samples))
	for i, s := range record.Power.Samples {
		samples[i] = powerwindow.Sample{ObservedAt: s.ObservedAt, Watts: s.Watts}
	}
	joules, err := powerwindow.Integrate(powerwindow.Window{Start: record.Result.StartedAt, End: record.Result.EndedAt, Samples: samples}, record.Request.Coverage)
	if err != nil {
		return serving.OperationCost{}, err
	}
	if joules != *record.WallJoules {
		return serving.OperationCost{}, errors.New("stored cost differs from retained raw integral")
	}
	from, to, err := operationCostKeys(record.Result)
	if err != nil {
		return serving.OperationCost{}, err
	}
	var beforeBaseline *float64
	// Explicitly sourced pre-action idle reading, not an invented mean or zero.
	// Its source, time and raw reading remain in the same immutable record.
	for _, sample := range record.Power.Samples {
		if !sample.ObservedAt.After(record.Result.StartedAt) && !math.IsNaN(sample.Watts) && !math.IsInf(sample.Watts, 0) && sample.Watts >= 0 {
			watts := sample.Watts
			beforeBaseline = &watts
		}
	}
	return serving.OperationCost{ID: record.Request.Operation.ID, Node: record.Request.Operation.Node, Kind: record.Request.Operation.Kind, From: from, To: to, Trials: []serving.OperationTrial{{Duration: record.Result.EndedAt.Sub(record.Result.StartedAt), Joules: joules}}, EnergyScope: "whole-wall", Meter: record.Request.Meter.ID, Scope: record.Request.Meter.Scope, Reference: reference, PowerMode: record.Result.Before.PowerMode, BeforeState: stateProfileKeys(record.Result.Before), TargetState: stateProfileKeys(record.Result.Actual), BeforeBaselineWatts: beforeBaseline, MeasuredAt: record.Result.EndedAt}, nil
}
func stateProfileKeys(state OperationState) []string { return append([]string(nil), state.States...) }
func operationCostKeys(result OperationResult) (string, string, error) {
	difference := func(a, b []string) []string {
		remaining := slices.Clone(b)
		var diff []string
		for _, value := range a {
			index := slices.Index(remaining, value)
			if index < 0 {
				diff = append(diff, value)
			} else {
				remaining = slices.Delete(remaining, index, index+1)
			}
		}
		return diff
	}
	switch result.Operation.Kind {
	case operationPrepare:
		added := difference(result.Actual.States, result.Before.States)
		removed := difference(result.Before.States, result.Actual.States)
		if len(added) == 1 && len(removed) == 0 {
			return nativeStateUnloaded, added[0], nil
		}
	case operationRemove:
		removed := difference(result.Before.States, result.Actual.States)
		added := difference(result.Actual.States, result.Before.States)
		if len(removed) == 1 && len(added) == 0 {
			return removed[0], nativeStateUnloaded, nil
		}
	case operationPower:
		// The measured power change is the empty-Node transition; the loaded one is a PowerModeTransitionCost.
		if before, after := result.Before.PowerMode, result.Actual.PowerMode; before != "" && after != "" && before != after && len(result.Before.States) == 0 && len(result.Actual.States) == 0 {
			return before, after, nil
		}
	}
	return "", "", errors.New("cost requires exactly one proven primitive state effect; resize uses measured prepare/remove composition")
}
func operationZeroInference(result OperationResult) error {
	if len(result.Before.Reasons) > 0 || len(result.Actual.Reasons) > 0 {
		return errors.New("operation native observations incomplete")
	}
	for id, before := range result.Before.Native {
		after, ok := result.Actual.Native[id]
		if !ok {
			after, ok = result.DrainNative[id]
		}
		if !ok || len(before.Requests) != 0 || len(after.Requests) != 0 || before.AcceptedRequests != after.AcceptedRequests || before.CompletedRequests != after.CompletedRequests || before.Executions != after.Executions {
			return errors.New("operation wall cost includes unseparated serving execution or lacks zero-inference proof")
		}
	}
	for id, after := range result.Actual.Native {
		if _, existed := result.Before.Native[id]; !existed && (after.AcceptedRequests != 0 || after.CompletedRequests != 0 || after.Executions != 0 || len(after.Requests) != 0) {
			return errors.New("new runner served inference during measured preparation")
		}
	}
	return nil
}
func (record *OperationMeasurement) applyIsolation(witness MeasurementIsolation, err error) error {
	record.Isolation = &witness
	if err == nil && (witness.Node != record.Request.Operation.Node || !witness.Tainted || witness.CheckedAt.Before(record.Result.EndedAt)) {
		err = errors.New("final actual isolation witness does not match operation boundary")
	}
	if err != nil {
		record.Isolation.Error = err.Error()
		record.Verdict = measurementInsufficient
		record.Reason = "measurement isolation unconfirmed: " + err.Error()
		record.WallJoules = nil
		record.MeanWatts = nil
	}
	return err
}
