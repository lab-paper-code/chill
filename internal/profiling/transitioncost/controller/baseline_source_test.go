package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"testing"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	batchsaturation "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

const otherMeasurementStateValue = "other"

func TestNewIdleBaselineSourceValidatesDependencies(t *testing.T) {
	reader := &baselineResultReader{}
	tests := map[string]struct {
		reader  evidencestore.Reader
		maximum int64
	}{
		"nil reader":        {maximum: 1},
		"zero bound":        {reader: reader},
		"overflowing bound": {reader: reader, maximum: math.MaxInt64},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewIdleBaselineSource(test.reader, test.maximum); err == nil {
				t.Fatal("invalid baseline source dependencies were accepted")
			}
		})
	}
}

func TestIdleBaselineSourceReadsOneMatchingAggregate(t *testing.T) {
	state := baselineMeasurementState()
	baseline := retainedIdleBaseline(t, "state", 8)
	reference, payload := retainedBatchResult(t, state, "Stopped/NoFurtherImprovement", sequentialDetail(t, baseline))
	reader := &baselineResultReader{payload: payload}
	source := newIdleBaselineSource(t, reader, int64(len(payload)))

	resolved, err := source.Read(context.Background(), state, reference)
	if err != nil {
		t.Fatalf("read idle baseline: %v", err)
	}
	if resolved.MeanWallWatts() != baseline.ModelLoadedIdleMeanWallWatts ||
		resolved.Reference() != reference {
		t.Fatalf("resolved baseline = watts %v, reference %+v", resolved.MeanWallWatts(), resolved.Reference())
	}
	wantIdentity := evidencestore.ObjectIdentity{
		Store: reference.Store, Key: reference.Key, Size: reference.Size,
	}
	if reader.calls != 1 || reader.identity != wantIdentity {
		t.Fatalf("reader calls = %d, identity = %+v, want %+v", reader.calls, reader.identity, wantIdentity)
	}
}

func TestIdleBaselineSourceRejectsReferenceBeforeOpen(t *testing.T) {
	state := baselineMeasurementState()
	baseline := retainedIdleBaseline(t, "state", 8)
	reference, payload := retainedBatchResult(t, state, "Stopped/CapabilityBoundary", sequentialDetail(t, baseline))
	tests := map[string]struct {
		maximum   int64
		reference batchsaturation.StoredObjectReference
	}{
		"invalid object": {maximum: int64(len(payload)), reference: func() batchsaturation.StoredObjectReference {
			invalid := reference
			invalid.Key = ""
			return invalid
		}()},
		"invalid schema": {maximum: int64(len(payload)), reference: func() batchsaturation.StoredObjectReference {
			invalid := reference
			invalid.SchemaVersion = " padded "
			return invalid
		}()},
		"above bound": {maximum: int64(len(payload) - 1), reference: reference},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			reader := &baselineResultReader{payload: payload}
			source := newIdleBaselineSource(t, reader, test.maximum)
			if _, err := source.Read(context.Background(), state, test.reference); err == nil {
				t.Fatal("invalid reference was accepted")
			}
			if reader.calls != 0 {
				t.Fatalf("invalid reference reached reader: calls = %d", reader.calls)
			}
		})
	}
}

func TestIdleBaselineSourceRequiresExactCurrentResultBytes(t *testing.T) {
	state := baselineMeasurementState()
	baseline := retainedIdleBaseline(t, "state", 8)
	reference, payload := retainedBatchResult(t, state, "Stopped/EnergyDivergence", sequentialDetail(t, baseline))
	tests := map[string]struct {
		payload   []byte
		reference batchsaturation.StoredObjectReference
	}{
		"short object": {payload: payload, reference: func() batchsaturation.StoredObjectReference {
			changed := reference
			changed.Size++
			return changed
		}()},
		"long object": {payload: payload, reference: func() batchsaturation.StoredObjectReference {
			changed := reference
			changed.Size--
			return changed
		}()},
		"old schema": func() struct {
			payload   []byte
			reference batchsaturation.StoredObjectReference
		} {
			old := bytes.Replace(payload, []byte("batch-saturation-result.v1alpha3"), []byte("batch-saturation-result.v1alpha2"), 1)
			changed := reference
			changed.SchemaVersion = "chill.dacs.io/batch-saturation-result.v1alpha2"
			return struct {
				payload   []byte
				reference batchsaturation.StoredObjectReference
			}{old, changed}
		}(),
		"reference schema mismatch": {payload: payload, reference: func() batchsaturation.StoredObjectReference {
			changed := reference
			changed.SchemaVersion = "chill.dacs.io/batch-saturation-result.v9alpha9"
			return changed
		}()},
		"unknown field": func() struct {
			payload   []byte
			reference batchsaturation.StoredObjectReference
		} {
			changedPayload := bytes.Replace(payload, []byte(`{"schemaVersion"`), []byte(`{"unexpected":true,"schemaVersion"`), 1)
			changed := reference
			changed.Size = int64(len(changedPayload))
			return struct {
				payload   []byte
				reference batchsaturation.StoredObjectReference
			}{changedPayload, changed}
		}(),
		"trailing value": func() struct {
			payload   []byte
			reference batchsaturation.StoredObjectReference
		} {
			changedPayload := append(bytes.Clone(payload), []byte(` {}`)...)
			changed := reference
			changed.Size = int64(len(changedPayload))
			return struct {
				payload   []byte
				reference batchsaturation.StoredObjectReference
			}{changedPayload, changed}
		}(),
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			reader := &baselineResultReader{payload: test.payload}
			source := newIdleBaselineSource(t, reader, int64(len(test.payload))+1)
			if _, err := source.Read(context.Background(), state, test.reference); err == nil {
				t.Fatal("invalid retained result was accepted")
			}
		})
	}
}

func TestIdleBaselineSourceRequiresExactMeasurementState(t *testing.T) {
	state := baselineMeasurementState()
	baseline := retainedIdleBaseline(t, "state", 8)
	reference, payload := retainedBatchResult(t, state, "Stopped/NoFurtherImprovement", sequentialDetail(t, baseline))
	fields := map[string]func(*batchsaturation.ResultMeasurementState){
		"target Node": func(value *batchsaturation.ResultMeasurementState) {
			value.TargetNode = otherMeasurementStateValue
		},
		"ModelSpec": func(value *batchsaturation.ResultMeasurementState) {
			value.ModelSpec = otherMeasurementStateValue
		},
		"DeviceClass": func(value *batchsaturation.ResultMeasurementState) {
			value.DeviceClass = otherMeasurementStateValue
		},
		"execution path": func(value *batchsaturation.ResultMeasurementState) {
			value.ExecutionPath = otherMeasurementStateValue
		},
		"power mode": func(value *batchsaturation.ResultMeasurementState) {
			value.PowerMode = otherMeasurementStateValue
		},
	}
	for name, change := range fields {
		t.Run(name, func(t *testing.T) {
			expected := state
			change(&expected)
			source := newIdleBaselineSource(t, &baselineResultReader{payload: payload}, int64(len(payload)))
			if _, err := source.Read(context.Background(), expected, reference); err == nil {
				t.Fatal("mismatched measurement state was accepted")
			}
		})
	}
}

func TestIdleBaselineSourceRequiresSuccessfulResultWithOneAggregate(t *testing.T) {
	state := baselineMeasurementState()
	first := retainedIdleBaseline(t, "state", 8)
	second := retainedIdleBaseline(t, "other-state", 9)
	tests := map[string]struct {
		outcome batchsaturation.TerminalOutcome
		detail  []byte
	}{
		"unsuccessful outcome": {outcome: "Failed", detail: sequentialDetail(t, first)},
		"malformed detail":     {outcome: "Stopped/NoFurtherImprovement", detail: []byte(`{"unknown":true}`)},
		"no aggregate":         {outcome: "Stopped/NoFurtherImprovement", detail: sequentialDetail(t)},
		"multiple aggregates":  {outcome: "Stopped/NoFurtherImprovement", detail: sequentialDetail(t, first, second)},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			reference, payload := retainedBatchResult(t, state, test.outcome, test.detail)
			source := newIdleBaselineSource(t, &baselineResultReader{payload: payload}, int64(len(payload)))
			if _, err := source.Read(context.Background(), state, reference); err == nil {
				t.Fatal("invalid baseline result was accepted")
			}
		})
	}
}

type baselineResultReader struct {
	payload  []byte
	calls    int
	identity evidencestore.ObjectIdentity
}

func (r *baselineResultReader) Open(
	_ context.Context,
	identity evidencestore.ObjectIdentity,
) (io.ReadCloser, error) {
	r.calls++
	r.identity = identity
	return io.NopCloser(bytes.NewReader(r.payload)), nil
}

func newIdleBaselineSource(
	t *testing.T,
	reader evidencestore.Reader,
	maximumBytes int64,
) *IdleBaselineSource {
	t.Helper()
	source, err := NewIdleBaselineSource(reader, maximumBytes)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func baselineMeasurementState() batchsaturation.ResultMeasurementState {
	return batchsaturation.ResultMeasurementState{
		TargetNode: "node-under-test", ModelSpec: "model-under-test",
		DeviceClass: "device-class-under-test", ExecutionPath: "execution-path-under-test",
		PowerMode: "target-mode",
	}
}

func retainedIdleBaseline(t *testing.T, state string, mean float64) profilederivation.IdleBaseline {
	t.Helper()
	trials := make([]profilederivation.IdleTrial, 3)
	for index := range trials {
		trials[index] = profilederivation.IdleTrial{
			StateIdentity: state, EnvironmentIdentity: "environment",
			MeasurementProtocolIdentity: "idle-protocol", TrialID: state + "-idle-" + string(rune('1'+index)),
			RawEvidence: profilederivation.RawEvidenceIdentity{
				Store: "evidence", Key: state + "/idle/" + string(rune('1'+index)), Size: int64(index + 1),
			},
			AdmissionPolicyVersion: "idle-admission-v1", ModelLoadedIdleMeanWallWatts: mean + float64(index),
		}
	}
	baseline, err := profilederivation.DeriveIdleBaseline(trials, profilederivation.PointPolicy{
		Version: "idle-point-v1", MinimumTrials: 3, ConfidenceLevel: .95,
	})
	if err != nil {
		t.Fatal(err)
	}
	return baseline
}

func sequentialDetail(t *testing.T, baselines ...profilederivation.IdleBaseline) []byte {
	t.Helper()
	byState := make(map[string]profilederivation.IdleBaseline, len(baselines))
	for _, baseline := range baselines {
		byState[baseline.StateIdentity] = baseline
	}
	payload, err := json.Marshal(profilederivation.SequentialResult{IdleBaselinesByState: byState})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func retainedBatchResult(
	t *testing.T,
	state batchsaturation.ResultMeasurementState,
	outcome batchsaturation.TerminalOutcome,
	detail []byte,
) (batchsaturation.StoredObjectReference, []byte) {
	t.Helper()
	record, err := batchsaturation.NewResultRecord(batchsaturation.ResultRecordSpec{
		Outcome: outcome, MeasurementState: state,
		CurvePlan: batchsaturation.StoredObjectReference{
			Store: "evidence", Key: "batch-saturations/campaign/curve-plan.json",
			SchemaVersion: "chill.dacs.io/curve-plan.v1alpha4", Size: 1024,
		},
		Detail: detail,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := record.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return batchsaturation.StoredObjectReference{
		Store: "evidence", Key: "batch-saturations/campaign/result.json",
		SchemaVersion: record.SchemaVersion(), Size: int64(len(payload)),
	}, payload
}
