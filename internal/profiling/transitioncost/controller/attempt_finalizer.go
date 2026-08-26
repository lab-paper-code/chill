package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	FinalizedTransitionRecordSchemaVersion = "chill.dacs.io/transition-cost-finalized-observation.v1alpha2"
	maximumTransitionReceiptBytes          = 4 * 1024
)

// AttemptFinalizationStore is the exact immutable read/write boundary needed
// to finalize and later rediscover one transition attempt.
type AttemptFinalizationStore interface {
	evidencestore.LocationReader
	evidencestore.ImmutableWriter
}

// FinalizedAttempt returns retained references and the verified raw-free observation.
type FinalizedAttempt struct {
	RawReference   evidencestore.ObjectIdentity
	FinalReference evidencestore.ObjectIdentity
	Observation    transitioncost.FinalizedObservation
}

type AttemptFinalizer struct {
	reader             client.Reader
	store              AttemptFinalizationStore
	maximumObjectBytes int64
}

func NewAttemptFinalizer(
	reader client.Reader,
	store AttemptFinalizationStore,
	maximumObjectBytes int64,
) (*AttemptFinalizer, error) {
	if reader == nil || store == nil {
		return nil, errors.New("Kubernetes reader and transition evidence store are required")
	}
	if maximumObjectBytes < 1 || maximumObjectBytes == 1<<63-1 {
		return nil, errors.New("maximum transition evidence object size is invalid")
	}
	return &AttemptFinalizer{reader: reader, store: store, maximumObjectBytes: maximumObjectBytes}, nil
}

// Finalize retains one FinalizationReady attempt, loading a deterministic final object first.
func (f *AttemptFinalizer) Finalize(ctx context.Context, measurement *edgev1alpha1.PowerModeTransitionCost,
	prepared PreparedMeasurement, directionIndex int, attemptNumber int32, progress AttemptProgress,
) (FinalizedAttempt, error) {
	selection, err := selectAttempt(
		measurement, prepared.EvidenceStore, directionIndex, attemptNumber,
	)
	if err != nil {
		return FinalizedAttempt{}, err
	}
	if existing, found, err := f.load(ctx, selection.finalLocation); err != nil {
		return FinalizedAttempt{}, err
	} else if found {
		return existing, nil
	}
	rawReference, observation, err := f.deriveFinalizedObservationFromTerminalEvidence(
		ctx, measurement, prepared, selection, progress,
	)
	if err != nil {
		return FinalizedAttempt{}, err
	}
	return f.retainFinalizedAttempt(ctx, selection.finalLocation, rawReference, observation)
}

func (f *AttemptFinalizer) deriveFinalizedObservationFromTerminalEvidence(
	ctx context.Context,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	prepared PreparedMeasurement,
	selection attemptSelection,
	progress AttemptProgress,
) (evidencestore.ObjectIdentity, transitioncost.FinalizedObservation, error) {
	job, err := validateFinalizationProgress(measurement, selection, progress)
	if err != nil {
		return evidencestore.ObjectIdentity{}, transitioncost.FinalizedObservation{}, err
	}
	pod, err := f.ownedAttemptPod(ctx, measurement.Namespace, job)
	if err != nil {
		return evidencestore.ObjectIdentity{}, transitioncost.FinalizedObservation{}, err
	}
	rawReceipt, err := transitionTerminationReceipt(pod)
	if err != nil {
		return evidencestore.ObjectIdentity{}, transitioncost.FinalizedObservation{}, err
	}
	if rawReceipt.Object.Location() != selection.rawLocation {
		return evidencestore.ObjectIdentity{}, transitioncost.FinalizedObservation{},
			errors.New("transition raw receipt does not match the deterministic attempt location")
	}
	rawBytes, err := readExactTransitionObject(ctx, f.store, rawReceipt.Object, f.maximumObjectBytes)
	if err != nil {
		return evidencestore.ObjectIdentity{}, transitioncost.FinalizedObservation{},
			fmt.Errorf("read transition raw evidence: %w", err)
	}
	raw, err := transitioncost.DecodeRawRecord(rawBytes)
	if err != nil {
		return evidencestore.ObjectIdentity{}, transitioncost.FinalizedObservation{}, err
	}
	result, err := transitioncost.Derive(
		prepared.Directions[selection.directionIndex].FrozenContract,
		raw,
		prepared.Policy,
	)
	if err != nil {
		return evidencestore.ObjectIdentity{}, transitioncost.FinalizedObservation{}, err
	}
	observation, err := transitioncost.FinalizeResult(result)
	if err != nil {
		return evidencestore.ObjectIdentity{}, transitioncost.FinalizedObservation{}, err
	}
	return rawReceipt.Object, observation, nil
}

func (f *AttemptFinalizer) retainFinalizedAttempt(
	ctx context.Context,
	location evidencestore.ObjectLocation,
	rawReference evidencestore.ObjectIdentity,
	observation transitioncost.FinalizedObservation,
) (FinalizedAttempt, error) {
	payload, err := encodeFinalizedTransition(rawReference, observation)
	if err != nil {
		return FinalizedAttempt{}, err
	}
	if int64(len(payload)) > f.maximumObjectBytes {
		return FinalizedAttempt{}, fmt.Errorf("finalized transition evidence exceeds %d-byte limit", f.maximumObjectBytes)
	}
	finalReference := evidencestore.ObjectIdentity{
		Store: location.Store, Key: location.Key, Size: int64(len(payload)),
	}
	receipt, err := f.store.PutImmutable(ctx, finalReference, bytes.NewReader(payload))
	if err != nil {
		return FinalizedAttempt{}, fmt.Errorf("create finalized transition evidence: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return FinalizedAttempt{}, fmt.Errorf("validate finalized transition receipt: %w", err)
	}
	if receipt.Object != finalReference {
		return FinalizedAttempt{}, errors.New("finalized transition writer returned an unexpected object reference")
	}
	if receipt.Outcome == evidencestore.CreateOutcomeExisting {
		winner, found, err := f.load(ctx, location)
		if err != nil {
			return FinalizedAttempt{}, err
		}
		if !found {
			return FinalizedAttempt{}, errors.New("existing finalized transition evidence disappeared")
		}
		return winner, nil
	}
	return FinalizedAttempt{
		RawReference: rawReference, FinalReference: finalReference, Observation: observation,
	}, nil
}
func validateFinalizationProgress(measurement *edgev1alpha1.PowerModeTransitionCost, selection attemptSelection,
	progress AttemptProgress,
) (*batchv1.Job, error) {
	job, actuation := progress.Job, progress.NodeActuation
	if progress.Stage != AttemptStageFinalizationReady || job == nil || actuation == nil {
		return nil, errors.New("transition attempt is not ready for finalization")
	}
	if job.Namespace != measurement.Namespace || actuation.Namespace != measurement.Namespace ||
		job.Name != selection.name || actuation.Name != selection.name || job.UID == "" || actuation.UID == "" ||
		!metav1.IsControlledBy(job, measurement) || !metav1.IsControlledBy(actuation, measurement) {
		return nil, errors.New("transition attempt Job and NodeActuation ownership or names do not match")
	}
	if !jobComplete(job) && !jobFailed(job) {
		return nil, errors.New("transition attempt Job is not terminal")
	}
	if _, terminal := actuationTerminalTime(actuation); !terminal {
		return nil, errors.New("transition attempt NodeActuation is not terminal")
	}
	return job, nil
}
func (f *AttemptFinalizer) ownedAttemptPod(
	ctx context.Context,
	namespace string,
	job *batchv1.Job,
) (*corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := f.reader.List(ctx, pods, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list transition attempt Pods: %w", err)
	}
	owned := make([]*corev1.Pod, 0, 1)
	for index := range pods.Items {
		pod := &pods.Items[index]
		owner := metav1.GetControllerOf(pod)
		if owner != nil && owner.Kind == "Job" && owner.UID == job.UID {
			owned = append(owned, pod)
		}
	}
	if len(owned) != 1 {
		return nil, fmt.Errorf("expected one Pod owned by transition attempt Job, found %d", len(owned))
	}
	return owned[0].DeepCopy(), nil
}

type finalizedTransitionWire struct {
	SchemaVersion string                                    `json:"schemaVersion"`
	RawEvidence   evidencestore.ObjectIdentity              `json:"rawEvidence"`
	Observation   transitioncost.FinalizedObservationRecord `json:"observation"`
}

func encodeFinalizedTransition(rawReference evidencestore.ObjectIdentity,
	observation transitioncost.FinalizedObservation,
) ([]byte, error) {
	if err := rawReference.Validate(); err != nil {
		return nil, fmt.Errorf("validate transition raw reference: %w", err)
	}
	payload, err := json.Marshal(finalizedTransitionWire{
		SchemaVersion: FinalizedTransitionRecordSchemaVersion,
		RawEvidence:   rawReference, Observation: observation.Record(),
	})
	if err != nil {
		return nil, fmt.Errorf("encode finalized transition evidence: %w", err)
	}
	return payload, nil
}
func decodeFinalizedTransition(
	payload []byte,
) (evidencestore.ObjectIdentity, transitioncost.FinalizedObservation, error) {
	var wire finalizedTransitionWire
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return evidencestore.ObjectIdentity{}, transitioncost.FinalizedObservation{},
			fmt.Errorf("decode finalized transition evidence: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return evidencestore.ObjectIdentity{}, transitioncost.FinalizedObservation{},
			errors.New("finalized transition evidence contains a trailing JSON value")
	}
	if wire.SchemaVersion != FinalizedTransitionRecordSchemaVersion {
		return evidencestore.ObjectIdentity{}, transitioncost.FinalizedObservation{},
			fmt.Errorf("unsupported finalized transition schema %q", wire.SchemaVersion)
	}
	if err := wire.RawEvidence.Validate(); err != nil {
		return evidencestore.ObjectIdentity{}, transitioncost.FinalizedObservation{},
			fmt.Errorf("validate finalized raw reference: %w", err)
	}
	observation, err := transitioncost.RestoreFinalizedObservation(wire.Observation)
	if err != nil {
		return evidencestore.ObjectIdentity{}, transitioncost.FinalizedObservation{},
			fmt.Errorf("restore finalized transition observation: %w", err)
	}
	return wire.RawEvidence, observation, nil
}
func (f *AttemptFinalizer) load(
	ctx context.Context,
	location evidencestore.ObjectLocation,
) (FinalizedAttempt, bool, error) {
	payload, err := readBoundedTransitionObject(ctx, f.store, location, f.maximumObjectBytes)
	if errors.Is(err, evidencestore.ErrNotFound) {
		return FinalizedAttempt{}, false, nil
	}
	if err != nil {
		return FinalizedAttempt{}, false, fmt.Errorf("read finalized transition evidence: %w", err)
	}
	rawReference, observation, err := decodeFinalizedTransition(payload)
	if err != nil {
		return FinalizedAttempt{}, false, err
	}
	return FinalizedAttempt{
		RawReference:   rawReference,
		FinalReference: evidencestore.ObjectIdentity{Store: location.Store, Key: location.Key, Size: int64(len(payload))},
		Observation:    observation,
	}, true, nil
}

func transitionTerminationReceipt(pod *corev1.Pod) (evidencestore.Receipt, error) {
	if pod == nil {
		return evidencestore.Receipt{}, errors.New("transition receipt Pod is required")
	}
	var terminated *corev1.ContainerStateTerminated
	for index := range pod.Status.ContainerStatuses {
		status := &pod.Status.ContainerStatuses[index]
		if status.Name != transitionObserverContainerName {
			continue
		}
		terminated = status.State.Terminated
		break
	}
	if terminated == nil || terminated.Message == "" {
		return evidencestore.Receipt{}, fmt.Errorf(
			"transition receipt container %q has no terminal receipt", transitionObserverContainerName,
		)
	}
	payload := []byte(terminated.Message)
	if len(payload) > maximumTransitionReceiptBytes {
		return evidencestore.Receipt{}, fmt.Errorf("transition receipt exceeds %d-byte limit", maximumTransitionReceiptBytes)
	}
	var receipt evidencestore.Receipt
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("decode transition receipt: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return evidencestore.Receipt{}, errors.New("transition receipt contains a trailing JSON value")
	}
	if err := receipt.Validate(); err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("validate transition receipt: %w", err)
	}
	return receipt, nil
}

func readExactTransitionObject(ctx context.Context, reader evidencestore.LocationReader,
	identity evidencestore.ObjectIdentity, maximumBytes int64,
) ([]byte, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	if identity.Size > maximumBytes {
		return nil, fmt.Errorf("evidence object exceeds %d-byte limit", maximumBytes)
	}
	payload, err := readBoundedTransitionObject(ctx, reader, identity.Location(), maximumBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) != identity.Size {
		return nil, fmt.Errorf("%w: evidence object size does not match its receipt", evidencestore.ErrContentMismatch)
	}
	return payload, nil
}

func readBoundedTransitionObject(ctx context.Context, reader evidencestore.LocationReader,
	location evidencestore.ObjectLocation, maximumBytes int64,
) ([]byte, error) {
	stream, err := reader.OpenLocation(ctx, location)
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, maximumBytes+1))
	closeErr := stream.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(payload)) > maximumBytes {
		return nil, fmt.Errorf("evidence object exceeds %d-byte limit", maximumBytes)
	}
	return payload, nil
}
