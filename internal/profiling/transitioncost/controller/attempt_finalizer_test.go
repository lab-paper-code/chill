package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type finalizationOutcome int

const (
	acceptedAttempt finalizationOutcome = iota
	invalidAttempt
	failedAttempt
	insufficientAttempt
	attemptEvidenceStore = "evidence"
)

func TestAttemptFinalizerRetainsEveryVerdictFromCompleteOrFailedJob(t *testing.T) {
	tests := []struct {
		name      string
		outcome   finalizationOutcome
		condition batchv1.JobConditionType
		verdict   transitioncost.Verdict
	}{
		{name: "accepted complete", outcome: acceptedAttempt,
			condition: batchv1.JobComplete, verdict: transitioncost.VerdictAccepted},
		{name: "invalid failed", outcome: invalidAttempt,
			condition: batchv1.JobFailed, verdict: transitioncost.VerdictInvalid},
		{name: "failed complete", outcome: failedAttempt,
			condition: batchv1.JobComplete, verdict: transitioncost.VerdictFailed},
		{name: "insufficient failed", outcome: insufficientAttempt,
			condition: batchv1.JobFailed, verdict: transitioncost.VerdictInsufficient},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttemptFinalizerFixture(t, test.outcome, test.condition)
			finalized, err := fixture.finalizer.Finalize(
				context.Background(), fixture.measurement, fixture.prepared, 0, 1, fixture.progress,
			)
			if err != nil {
				t.Fatal(err)
			}
			if finalized.Observation.Verdict() != test.verdict ||
				finalized.RawReference != fixture.rawReference || finalized.FinalReference.Size < 1 || fixture.store.puts != 1 {
				t.Fatalf("finalization = %#v, puts = %d", finalized, fixture.store.puts)
			}
			emptyReader := fake.NewClientBuilder().WithScheme(fixture.scheme).Build()
			restarted, err := NewAttemptFinalizer(emptyReader, fixture.store, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			delete(fixture.store.objects, fixture.rawReference.Location())
			recovered, err := restarted.Finalize(
				context.Background(), fixture.measurement,
				PreparedMeasurement{EvidenceStore: fixture.prepared.EvidenceStore}, 0, 1, AttemptProgress{},
			)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Observation.Verdict() != test.verdict || fixture.store.puts != 1 {
				t.Fatalf("recovered finalization = %#v, puts = %d", recovered, fixture.store.puts)
			}
		})
	}
}

func TestAttemptFinalizerUsesImmutableWinnerWithoutComparison(t *testing.T) {
	fixture := newAttemptFinalizerFixture(t, acceptedAttempt, batchv1.JobComplete)
	winnerRaw := transitionRawObservation(fixture.prepared, invalidAttempt)
	winnerResult, err := transitioncost.Derive(
		fixture.prepared.Directions[0].FrozenContract, winnerRaw, fixture.prepared.Policy,
	)
	if err != nil {
		t.Fatal(err)
	}
	winner, err := transitioncost.FinalizeResult(winnerResult)
	if err != nil {
		t.Fatal(err)
	}
	fixture.store.winnerOnPut, err = encodeFinalizedTransition(
		evidencestore.ObjectIdentity{Store: attemptEvidenceStore, Key: "winner/raw.json", Size: 1}, winner,
	)
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := fixture.finalizer.Finalize(
		context.Background(), fixture.measurement, fixture.prepared, 0, 1, fixture.progress,
	)
	if err != nil {
		t.Fatal(err)
	}
	if finalized.Observation.Verdict() != transitioncost.VerdictInvalid || fixture.store.puts != 1 {
		t.Fatalf("winner finalization = %#v, puts = %d", finalized, fixture.store.puts)
	}
}

func TestFinalizedTransitionCodecRejectsMalformedCurrentRecord(t *testing.T) {
	fixture := newAttemptFinalizerFixture(t, acceptedAttempt, batchv1.JobComplete)
	result, err := transitioncost.Derive(
		fixture.prepared.Directions[0].FrozenContract,
		transitionRawObservation(fixture.prepared, acceptedAttempt), fixture.prepared.Policy,
	)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := transitioncost.FinalizeResult(result)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := encodeFinalizedTransition(fixture.rawReference, observation)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"unknown field": bytes.Replace(payload, []byte(`"observation":`), []byte(`"unknown":true,"observation":`), 1),
		"old schema": bytes.Replace(payload, []byte(FinalizedTransitionRecordSchemaVersion),
			[]byte("chill.dacs.io/transition-cost-finalized-observation.v1alpha1"), 1),
		"trailing value": append(append([]byte(nil), payload...), []byte(" {}")...),
	}
	for name, malformed := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeFinalizedTransition(malformed); err == nil {
				t.Fatal("malformed finalized transition was accepted")
			}
		})
	}
}

func TestTransitionTerminationReceiptIsStrictAndBounded(t *testing.T) {
	valid := evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion,
		Object:        evidencestore.ObjectIdentity{Store: attemptEvidenceStore, Key: "raw.json", Size: 10},
		Outcome:       evidencestore.CreateOutcomeCreated,
	}
	payload, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"unknown":   strings.Replace(string(payload), `"object":`, `"unknown":true,"object":`, 1),
		"trailing":  string(payload) + ` {}`,
		"oversized": strings.Repeat("x", maximumTransitionReceiptBytes+1),
	}
	for name, message := range tests {
		t.Run(name, func(t *testing.T) {
			pod := receiptPod("job-uid", message)
			if _, err := transitionTerminationReceipt(pod); err == nil {
				t.Fatal("malformed termination receipt was accepted")
			}
		})
	}
}

func TestAttemptFinalizerRequiresExactlyOneOwnedPodAndExactRawSize(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "measurements", Name: "job", UID: "job-uid"}}
	for name, pods := range map[string][]client.Object{
		"missing": nil,
		"duplicate": {receiptPod(job.UID, "x"), func() *corev1.Pod {
			pod := receiptPod(job.UID, "x")
			pod.Name, pod.UID = otherMeasurementStateValue, "other-uid"
			return pod
		}()},
	} {
		t.Run(name, func(t *testing.T) {
			reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pods...).Build()
			finalizer, err := NewAttemptFinalizer(reader, newFinalizationStore(), 1024)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := finalizer.ownedAttemptPod(context.Background(), job.Namespace, job); err == nil {
				t.Fatal("invalid owned Pod count was accepted")
			}
		})
	}
	store := newFinalizationStore()
	identity := evidencestore.ObjectIdentity{Store: attemptEvidenceStore, Key: "raw.json", Size: 4}
	store.objects[identity.Location()] = []byte("abc")
	if _, err := readExactTransitionObject(context.Background(), store, identity, 1024); err == nil {
		t.Fatal("raw size mismatch was accepted")
	}
	store.objects[identity.Location()] = bytes.Repeat([]byte("x"), 1025)
	identity.Size = 1025
	if _, err := readExactTransitionObject(context.Background(), store, identity, 1024); err == nil {
		t.Fatal("oversized raw object was accepted")
	}
}

func TestAttemptFinalizerRejectsMismatchedReferences(t *testing.T) {
	t.Run("raw receipt location", func(t *testing.T) {
		fixture := newAttemptFinalizerFixture(t, acceptedAttempt, batchv1.JobComplete, ".wrong")
		_, err := fixture.finalizer.Finalize(
			context.Background(), fixture.measurement, fixture.prepared, 0, 1, fixture.progress,
		)
		if err == nil || fixture.store.opens != 1 || fixture.store.puts != 0 {
			t.Fatalf("err = %v, opens = %d, puts = %d", err, fixture.store.opens, fixture.store.puts)
		}
	})
	t.Run("final write receipt", func(t *testing.T) {
		fixture := newAttemptFinalizerFixture(t, acceptedAttempt, batchv1.JobComplete)
		fixture.store.badReceipt = true
		_, err := fixture.finalizer.Finalize(
			context.Background(), fixture.measurement, fixture.prepared, 0, 1, fixture.progress,
		)
		if err == nil {
			t.Fatal("mismatched final write receipt was accepted")
		}
	})
}

type attemptFinalizerFixture struct {
	measurement  *edgev1alpha1.PowerModeTransitionCost
	prepared     PreparedMeasurement
	scheme       *runtime.Scheme
	store        *finalizationStore
	finalizer    *AttemptFinalizer
	progress     AttemptProgress
	rawReference evidencestore.ObjectIdentity
}

func newAttemptFinalizerFixture(t *testing.T, outcome finalizationOutcome, condition batchv1.JobConditionType,
	receiptKeySuffix ...string,
) attemptFinalizerFixture {
	t.Helper()
	measurement, prepared, scheme := preparationRecordFixture(t)
	measurement.Spec.AttemptLimit = 5
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	selection, err := selectAttempt(measurement, prepared.EvidenceStore, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	owner := metav1.OwnerReference{
		APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "PowerModeTransitionCost",
		Name: measurement.Name, UID: measurement.UID, Controller: ptr.To(true),
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Namespace: measurement.Namespace, Name: selection.name, UID: "job-uid",
		OwnerReferences: []metav1.OwnerReference{owner},
	}, Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
		Type: condition, Status: corev1.ConditionTrue,
	}}}}
	readyAt := metav1.NewMicroTime(time.Unix(20, 0))
	targetAt := metav1.NewMicroTime(time.Unix(19, 0))
	actuation := &edgev1alpha1.NodeActuation{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: measurement.Namespace, Name: selection.name, UID: "actuation-uid", Generation: 1,
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Status: edgev1alpha1.NodeActuationStatus{
			ObservedGeneration: 1, Phase: edgev1alpha1.NodeActuationPhaseReady, ReadyAt: &readyAt,
			TargetObservation: &edgev1alpha1.NodeActuationObservation{ObservedAt: targetAt},
		},
	}
	rawBytes, err := transitioncost.EncodeRawRecord(transitionRawObservation(prepared, outcome))
	if err != nil {
		t.Fatal(err)
	}
	rawReference := evidencestore.ObjectIdentity{
		Store: selection.rawLocation.Store, Key: selection.rawLocation.Key, Size: int64(len(rawBytes)),
	}
	receiptReference := rawReference
	if len(receiptKeySuffix) > 0 {
		receiptReference.Key += receiptKeySuffix[0]
	}
	receipt := evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: receiptReference,
		Outcome: evidencestore.CreateOutcomeCreated,
	}
	receiptBytes, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	pod := receiptPod(job.UID, string(receiptBytes))
	pod.Namespace = measurement.Namespace
	store := newFinalizationStore()
	store.objects[rawReference.Location()] = rawBytes
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(job, actuation, pod).Build()
	finalizer, err := NewAttemptFinalizer(reader, store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return attemptFinalizerFixture{
		measurement: measurement, prepared: prepared, scheme: scheme, store: store, finalizer: finalizer,
		progress:     AttemptProgress{Stage: AttemptStageFinalizationReady, Job: job, NodeActuation: actuation},
		rawReference: rawReference,
	}
}

func transitionRawObservation(prepared PreparedMeasurement, outcome finalizationOutcome) transitioncost.RawObservation {
	base := time.Unix(100, 0).UTC()
	direction := prepared.Directions[0].FrozenContract
	process := transitioncost.ProcessIdentity{
		ContainerName: "runtime", ContainerID: "container", StartedAt: base.Add(-time.Hour),
	}
	raw := transitioncost.RawObservation{
		ObservationStartedAt: base, ObservationEndedAt: base.Add(4 * time.Second),
		SourceReadBack: transitioncost.StateReadBack{
			Fact: transitioncost.MappedFact{
				OccurredAt: base.Add(500 * time.Millisecond), MappedAt: base.Add(500 * time.Millisecond),
			},
			State: direction.SourceState,
		},
		TransitionStart: &transitioncost.OccurredFact{OccurredAt: base.Add(time.Second)},
		TargetReadBack: &transitioncost.TargetStateReadBack{
			Fact:  transitioncost.OccurredFact{OccurredAt: base.Add(2 * time.Second)},
			State: direction.TargetState,
		},
		MarkerBrackets: transitioncost.TransitionMarkerBrackets{
			ApplyStartedAt: transitioncost.ReceiptTimeBracket{
				LastAbsentReceivedAt:   base.Add(990 * time.Millisecond),
				FirstPresentReceivedAt: base.Add(time.Second),
			},
			TargetObservation: transitioncost.ReceiptTimeBracket{
				LastAbsentReceivedAt:   base.Add(1990 * time.Millisecond),
				FirstPresentReceivedAt: base.Add(2 * time.Second),
			},
			ReadyProcessWitness: transitioncost.ReceiptTimeBracket{
				LastAbsentReceivedAt:   base.Add(2990 * time.Millisecond),
				FirstPresentReceivedAt: base.Add(3 * time.Second),
			},
		},
		Readiness: []transitioncost.ReadyObservation{{ObservedAt: base.Add(3 * time.Second), Ready: true}},
		PowerSamples: []transitioncost.PowerSample{
			{ObservedAt: base.Add(1010 * time.Millisecond), Watts: 15},
			{ObservedAt: base.Add(1500 * time.Millisecond), Watts: 15},
			{ObservedAt: base.Add(2 * time.Second), Watts: 15},
			{ObservedAt: base.Add(2500 * time.Millisecond), Watts: 15},
			{ObservedAt: base.Add(2990 * time.Millisecond), Watts: 15},
		},
		PreTransitionProcess: &transitioncost.ProcessObservation{
			ObservedAt: base.Add(900 * time.Millisecond), Process: process,
		},
		PostTransitionProcess: &process,
	}
	switch outcome {
	case invalidAttempt:
		raw.SourceReadBack.State.PowerMode = otherMeasurementStateValue
	case failedAttempt:
		raw.PostTransitionProcess = nil
		raw.MarkerBrackets.ReadyProcessWitness.FirstPresentReceivedAt = time.Time{}
	case insufficientAttempt:
		raw.MarkerBrackets.ApplyStartedAt.LastAbsentReceivedAt =
			raw.MarkerBrackets.ApplyStartedAt.FirstPresentReceivedAt.
				Add(-prepared.Policy.MaximumBoundaryUncertainty - time.Millisecond)
	}
	return raw
}

func receiptPod(jobUID types.UID, message string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "measurements", Name: "observer", UID: "pod-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: batchv1.SchemeGroupVersion.String(), Kind: "Job", Name: "job",
				UID: jobUID, Controller: ptr.To(true),
			}},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  transitionObserverContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: message}},
		}}},
	}
}

type finalizationStore struct {
	objects     map[evidencestore.ObjectLocation][]byte
	winnerOnPut []byte
	puts        int
	opens       int
	badReceipt  bool
}

func newFinalizationStore() *finalizationStore {
	return &finalizationStore{objects: map[evidencestore.ObjectLocation][]byte{}}
}

func (s *finalizationStore) OpenLocation(
	_ context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	s.opens++
	payload, found := s.objects[location]
	if !found {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func (s *finalizationStore) PutImmutable(_ context.Context, identity evidencestore.ObjectIdentity,
	content io.Reader,
) (evidencestore.Receipt, error) {
	s.puts++
	if s.winnerOnPut != nil {
		s.objects[identity.Location()] = append([]byte(nil), s.winnerOnPut...)
		s.winnerOnPut = nil
		return finalizationReceipt(identity, evidencestore.CreateOutcomeExisting), nil
	}
	if _, found := s.objects[identity.Location()]; found {
		return finalizationReceipt(identity, evidencestore.CreateOutcomeExisting), nil
	}
	payload, err := io.ReadAll(content)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	s.objects[identity.Location()] = payload
	receipt := finalizationReceipt(identity, evidencestore.CreateOutcomeCreated)
	if s.badReceipt {
		receipt.Object.Key += ".wrong"
	}
	return receipt, nil
}

func finalizationReceipt(
	identity evidencestore.ObjectIdentity,
	outcome evidencestore.CreateOutcome,
) evidencestore.Receipt {
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: outcome,
	}
}
