package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	idlebaseline "github.com/lab-paper-code/chill/internal/profiling/idlebaseline"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	baselineNamespace          = "measurements"
	otherMeasurementStateValue = "different-state"
)

func TestNewIdleBaselineSourceValidatesDependencies(t *testing.T) {
	reader := &baselineResultReader{}
	kube := newBaselineKube(t)
	tests := map[string]struct {
		kube    client.Reader
		reader  evidencestore.Reader
		maximum int64
	}{
		"nil Kubernetes reader": {reader: reader, maximum: 1},
		"nil result reader":     {kube: kube, maximum: 1},
		"zero bound":            {kube: kube, reader: reader},
		"overflowing bound":     {kube: kube, reader: reader, maximum: math.MaxInt64},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewIdleBaselineSource(test.kube, test.reader, test.maximum); err == nil {
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
	source := newIdleBaselineSource(
		t, reader, int64(len(payload)), currentBaselineObject("baseline", state, reference),
	)

	resolved, err := source.Resolve(context.Background(), baselineNamespace, state)
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
			source := newIdleBaselineSource(
				t, reader, test.maximum, currentBaselineObject("baseline", state, test.reference),
			)
			if _, err := source.Resolve(context.Background(), baselineNamespace, state); err == nil {
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
			old := bytes.Replace(payload, []byte("result.v1alpha2"), []byte("result.v1alpha0"), 1)
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
			source := newIdleBaselineSource(
				t, reader, int64(len(test.payload))+1,
				currentBaselineObject("baseline", state, test.reference),
			)
			if _, err := source.Resolve(context.Background(), baselineNamespace, state); err == nil {
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
				currentBaselineObject("baseline", expected, reference),
			)
			if _, err := source.Resolve(context.Background(), baselineNamespace, expected); err == nil {
				t.Fatal("mismatched measurement state was accepted")
			}
		})
	}
}

func TestIdleBaselineSourceRequiresExactlyOneCurrentCompleteExactState(t *testing.T) {
	state := baselineMeasurementState()
	baseline := retainedIdleBaseline(t)
	reference, payload := retainedIdleResult(t, state, baseline)
	matching := currentBaselineObject("matching", state, reference)
	wrongState := currentBaselineObject("wrong-state", state, reference)
	wrongState.Spec.State.Candidate.PowerMode = otherMeasurementStateValue
	noncurrent := currentBaselineObject("noncurrent", state, reference)
	noncurrent.Status.ObservedGeneration = 0
	incomplete := currentBaselineObject("incomplete", state, reference)
	incomplete.Status.Phase = edgev1alpha1.ModelLoadedIdleBaselinePhaseMeasuring
	missingResult := currentBaselineObject("missing-result", state, reference)
	missingResult.Status.Result = nil
	deleting := currentBaselineObject("deleting", state, reference)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"test-finalizer"}
	otherNamespace := currentBaselineObject("other-namespace", state, reference)
	otherNamespace.Namespace = "other-namespace"

	reader := &baselineResultReader{payload: payload}
	source := newIdleBaselineSource(
		t, reader, int64(len(payload)), matching, wrongState, noncurrent,
		incomplete, missingResult, deleting, otherNamespace,
	)
	if _, err := source.Resolve(context.Background(), baselineNamespace, state); err != nil {
		t.Fatalf("resolve sole current Complete baseline: %v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("result reader calls = %d, want 1", reader.calls)
	}

	for name, objects := range map[string][]*edgev1alpha1.ModelLoadedIdleBaseline{
		"missing":   {wrongState, noncurrent, incomplete, missingResult, deleting, otherNamespace},
		"ambiguous": {matching, currentBaselineObject("second-match", state, reference)},
	} {
		t.Run(name, func(t *testing.T) {
			reader := &baselineResultReader{payload: payload}
			source := newIdleBaselineSource(t, reader, int64(len(payload)), objects...)
			if _, err := source.Resolve(context.Background(), baselineNamespace, state); err == nil {
				t.Fatal("non-unique baseline selection was accepted")
			}
			if reader.calls != 0 {
				t.Fatalf("non-unique selection opened evidence: calls = %d", reader.calls)
			}
		})
	}

	reader = &baselineResultReader{payload: payload}
	source = newIdleBaselineSource(t, reader, int64(len(payload)), matching)
	if _, err := source.Resolve(context.Background(), "", state); err == nil {
		t.Fatal("empty measurement namespace was accepted")
	}
	if reader.calls != 0 {
		t.Fatalf("empty namespace opened evidence: calls = %d", reader.calls)
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
	baselines ...*edgev1alpha1.ModelLoadedIdleBaseline,
) *IdleBaselineSource {
	t.Helper()
	objects := make([]client.Object, len(baselines))
	for index := range baselines {
		objects[index] = baselines[index]
	}
	source, err := NewIdleBaselineSource(newBaselineKube(t, objects...), reader, maximumBytes)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func newBaselineKube(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func currentBaselineObject(
	name string,
	state idlebaseline.MeasurementState,
	reference idlebaseline.StoredObjectReference,
) *edgev1alpha1.ModelLoadedIdleBaseline {
	return &edgev1alpha1.ModelLoadedIdleBaseline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: baselineNamespace, Generation: 1},
		Spec: edgev1alpha1.ModelLoadedIdleBaselineSpec{State: edgev1alpha1.MeasurementStateSpec{
			TargetNode: state.TargetNode,
			Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
				ModelSpec: state.ModelSpec, DeviceClass: state.DeviceClass,
				ExecutionPath: state.ExecutionPath, PowerMode: state.PowerMode,
			},
		}},
		Status: edgev1alpha1.ModelLoadedIdleBaselineStatus{
			ObservedGeneration: 1, Phase: edgev1alpha1.ModelLoadedIdleBaselinePhaseComplete,
			Result: &edgev1alpha1.ProfilingRunEvidenceReference{
				Store: reference.Store, Key: reference.Key,
				SchemaVersion: reference.SchemaVersion, Size: reference.Size,
			},
		},
	}
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
			ModelLoadedIdleMeanWallWatts: mean + float64(index),
			StateIdentity:                state, EnvironmentIdentity: "environment",
			MeasurementProtocolIdentity: "idle-protocol", TrialID: fmt.Sprintf("%s-idle-%d", state, index+1),
			RawEvidence: profilederivation.RawEvidenceIdentity{
				Store: "evidence", Key: fmt.Sprintf("%s/idle/%d", state, index+1), Size: int64(index + 1),
			},
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
	accepted := make(idlebaseline.FinalEvidence, len(baseline.Trials))
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
