package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"testing"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	idlebaseline "github.com/lab-paper-code/chill/internal/profiling/idlebaseline"
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
	baseline := retainedIdleBaseline(t)
	reference, payload := retainedIdleResult(t, state, baseline)
	reader := &baselineResultReader{payload: payload}
	source := newIdleBaselineSource(t, reader, int64(len(payload)))

	resolved, err := source.Read(context.Background(), state, reference)
	if err != nil {
		t.Fatalf("read idle baseline: %v", err)
	}
	if resolved.MeanWallWatts() != baseline.ModelLoadedIdleMeanWallWatts ||
		resolved.Reference() != reference {
		t.Fatalf(
			"resolved baseline = watts %v, reference %+v",
			resolved.MeanWallWatts(),
			resolved.Reference(),
		)
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
	baseline := retainedIdleBaseline(t)
	reference, payload := retainedIdleResult(t, state, baseline)
	tests := map[string]struct {
		maximum   int64
		reference idlebaseline.StoredObjectReference
	}{
		"invalid object": {maximum: int64(len(payload)), reference: func() idlebaseline.StoredObjectReference {
			invalid := reference
			invalid.Key = ""
			return invalid
		}()},
		"retired schema": {maximum: int64(len(payload)), reference: func() idlebaseline.StoredObjectReference {
			invalid := reference
			invalid.SchemaVersion = "chill.dacs.io/model-loaded-idle-baseline-result.v1alpha0"
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
	baseline := retainedIdleBaseline(t)
	reference, payload := retainedIdleResult(t, state, baseline)
	tests := map[string]struct {
		payload   []byte
		reference idlebaseline.StoredObjectReference
	}{
		"short object": {payload: payload, reference: func() idlebaseline.StoredObjectReference {
			changed := reference
			changed.Size++
			return changed
		}()},
		"long object": {payload: payload, reference: func() idlebaseline.StoredObjectReference {
			changed := reference
			changed.Size--
			return changed
		}()},
		"retired payload": func() struct {
			payload   []byte
			reference idlebaseline.StoredObjectReference
		} {
			old := bytes.Replace(payload, []byte("result.v1alpha1"), []byte("result.v1alpha0"), 1)
			changed := reference
			changed.Size = int64(len(old))
			return struct {
				payload   []byte
				reference idlebaseline.StoredObjectReference
			}{old, changed}
		}(),
		"unknown field": func() struct {
			payload   []byte
			reference idlebaseline.StoredObjectReference
		} {
			changedPayload := bytes.Replace(
				payload,
				[]byte(`{"schemaVersion"`),
				[]byte(`{"unexpected":true,"schemaVersion"`),
				1,
			)
			changed := reference
			changed.Size = int64(len(changedPayload))
			return struct {
				payload   []byte
				reference idlebaseline.StoredObjectReference
			}{changedPayload, changed}
		}(),
		"trailing value": func() struct {
			payload   []byte
			reference idlebaseline.StoredObjectReference
		} {
			changedPayload := append(bytes.Clone(payload), []byte(` {}`)...)
			changed := reference
			changed.Size = int64(len(changedPayload))
			return struct {
				payload   []byte
				reference idlebaseline.StoredObjectReference
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
	baseline := retainedIdleBaseline(t)
	reference, payload := retainedIdleResult(t, state, baseline)
	fields := map[string]func(*idlebaseline.MeasurementState){
		"target Node": func(value *idlebaseline.MeasurementState) {
			value.TargetNode = otherMeasurementStateValue
		},
		"ModelSpec": func(value *idlebaseline.MeasurementState) {
			value.ModelSpec = otherMeasurementStateValue
		},
		"DeviceClass": func(value *idlebaseline.MeasurementState) {
			value.DeviceClass = otherMeasurementStateValue
		},
		"execution path": func(value *idlebaseline.MeasurementState) {
			value.ExecutionPath = otherMeasurementStateValue
		},
		"power mode": func(value *idlebaseline.MeasurementState) {
			value.PowerMode = otherMeasurementStateValue
		},
	}
	for name, change := range fields {
		t.Run(name, func(t *testing.T) {
			expected := state
			change(&expected)
			source := newIdleBaselineSource(
				t,
				&baselineResultReader{payload: payload},
				int64(len(payload)),
			)
			if _, err := source.Read(context.Background(), expected, reference); err == nil {
				t.Fatal("mismatched measurement state was accepted")
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

func baselineMeasurementState() idlebaseline.MeasurementState {
	return idlebaseline.MeasurementState{
		TargetNode: "node-under-test", ModelSpec: "model-under-test",
		DeviceClass: "device-class-under-test", ExecutionPath: "execution-path-under-test",
		PowerMode: "target-mode",
	}
}

func retainedIdleBaseline(t *testing.T) profilederivation.IdleBaseline {
	t.Helper()
	const (
		state = "state"
		mean  = 8
	)
	trials := make([]profilederivation.IdleTrial, 3)
	for index := range trials {
		trials[index] = profilederivation.IdleTrial{
			StateIdentity: state, EnvironmentIdentity: "environment",
			MeasurementProtocolIdentity: "idle-protocol", TrialID: fmt.Sprintf("%s-idle-%d", state, index+1),
			RawEvidence: profilederivation.RawEvidenceIdentity{
				Store: "evidence", Key: fmt.Sprintf("%s/idle/%d", state, index+1), Size: int64(index + 1),
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

func retainedIdleResult(
	t *testing.T,
	state idlebaseline.MeasurementState,
	baseline profilederivation.IdleBaseline,
) (idlebaseline.StoredObjectReference, []byte) {
	t.Helper()
	accepted := make(idlebaseline.AcceptedFinalEvidence, len(baseline.Trials))
	for _, trial := range baseline.Trials {
		accepted[trial.TrialID] = idlebaseline.StoredObjectReference{
			Store: "evidence", Key: "runs/" + trial.TrialID + "/final.json",
			SchemaVersion: "final.v1", Size: 20,
		}
	}
	result, err := idlebaseline.NewResult(state, baseline, accepted)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := result.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return idlebaseline.StoredObjectReference{
		Store: "evidence", Key: "model-loaded-idle-baselines/baseline/result.json",
		SchemaVersion: idlebaseline.ResultSchemaVersion, Size: int64(len(payload)),
	}, payload
}
