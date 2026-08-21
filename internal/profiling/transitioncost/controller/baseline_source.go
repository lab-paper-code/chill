// Package controller prepares durable inputs for power-mode transition-cost measurements.
package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	idlebaseline "github.com/lab-paper-code/chill/internal/profiling/idlebaseline"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ResolvedIdleBaseline is a target-idle aggregate that crossed the retained
// ModelLoadedIdleBaseline result boundary once.
type ResolvedIdleBaseline struct {
	meanWallWatts float64
	reference     idlebaseline.StoredObjectReference
}

func (b ResolvedIdleBaseline) MeanWallWatts() float64 { return b.meanWallWatts }

func (b ResolvedIdleBaseline) Reference() idlebaseline.StoredObjectReference {
	return b.reference
}

// IdleBaselineSource resolves the only current Complete baseline for one exact
// state and reads its retained result.
type IdleBaselineSource struct {
	kube           client.Reader
	objects        evidencestore.Reader
	maxObjectBytes int64
}

func NewIdleBaselineSource(
	kube client.Reader,
	objects evidencestore.Reader,
	maxObjectBytes int64,
) (*IdleBaselineSource, error) {
	if kube == nil || objects == nil {
		return nil, errors.New("idle baseline Kubernetes and result readers are required")
	}
	if maxObjectBytes < 1 || maxObjectBytes == math.MaxInt64 {
		return nil, errors.New("maximum idle baseline result bytes must be positive and below MaxInt64")
	}
	return &IdleBaselineSource{kube: kube, objects: objects, maxObjectBytes: maxObjectBytes}, nil
}

func (s *IdleBaselineSource) Resolve(
	ctx context.Context,
	namespace string,
	expected idlebaseline.MeasurementState,
) (ResolvedIdleBaseline, error) {
	reference, err := s.resolveReference(ctx, namespace, expected)
	if err != nil {
		return ResolvedIdleBaseline{}, err
	}
	identity := evidencestore.ObjectIdentity{
		Store: reference.Store, Key: reference.Key, Size: reference.Size,
	}
	if err := identity.Validate(); err != nil {
		return ResolvedIdleBaseline{}, fmt.Errorf("validate idle baseline result reference: %w", err)
	}
	if reference.SchemaVersion != idlebaseline.ResultSchemaVersion {
		return ResolvedIdleBaseline{}, fmt.Errorf(
			"idle baseline result schema %q is not current", reference.SchemaVersion,
		)
	}
	if identity.Size > s.maxObjectBytes {
		return ResolvedIdleBaseline{}, fmt.Errorf(
			"idle baseline result declares %d bytes above %d-byte limit", identity.Size, s.maxObjectBytes,
		)
	}
	payload, err := s.read(ctx, identity)
	if err != nil {
		return ResolvedIdleBaseline{}, err
	}
	result, err := idlebaseline.DecodeResult(payload)
	if err != nil {
		return ResolvedIdleBaseline{}, fmt.Errorf("decode idle baseline result: %w", err)
	}
	if result.MeasurementState() != expected {
		return ResolvedIdleBaseline{}, errors.New(
			"ModelLoadedIdleBaseline result measurement state differs from the transition target",
		)
	}
	return ResolvedIdleBaseline{
		meanWallWatts: result.IdleBaseline().ModelLoadedIdleMeanWallWatts,
		reference:     reference,
	}, nil
}

func (s *IdleBaselineSource) resolveReference(
	ctx context.Context,
	namespace string,
	expected idlebaseline.MeasurementState,
) (idlebaseline.StoredObjectReference, error) {
	if !clean(namespace) {
		return idlebaseline.StoredObjectReference{}, errors.New("idle baseline namespace is required")
	}
	baselines := &edgev1alpha1.ModelLoadedIdleBaselineList{}
	if err := s.kube.List(ctx, baselines, client.InNamespace(namespace)); err != nil {
		return idlebaseline.StoredObjectReference{}, fmt.Errorf("list ModelLoadedIdleBaselines: %w", err)
	}
	matches := make([]idlebaseline.StoredObjectReference, 0, 1)
	for index := range baselines.Items {
		candidate := &baselines.Items[index]
		if candidate.DeletionTimestamp != nil || candidate.Status.ObservedGeneration != candidate.Generation ||
			candidate.Status.Phase != edgev1alpha1.ModelLoadedIdleBaselinePhaseComplete ||
			candidate.Status.Result == nil || projectBaselineMeasurementState(candidate.Spec.State) != expected {
			continue
		}
		matches = append(matches, storedObjectReference(*candidate.Status.Result))
	}
	if len(matches) != 1 {
		return idlebaseline.StoredObjectReference{}, fmt.Errorf(
			"exact state has %d current Complete ModelLoadedIdleBaselines; require exactly one", len(matches),
		)
	}
	return matches[0], nil
}

func projectBaselineMeasurementState(state edgev1alpha1.MeasurementStateSpec) idlebaseline.MeasurementState {
	return idlebaseline.MeasurementState{
		TargetNode: state.TargetNode, ModelSpec: state.Candidate.ModelSpec,
		DeviceClass: state.Candidate.DeviceClass, ExecutionPath: state.Candidate.ExecutionPath,
		PowerMode: state.Candidate.PowerMode,
	}
}

func storedObjectReference(reference edgev1alpha1.ProfilingRunEvidenceReference) idlebaseline.StoredObjectReference {
	return idlebaseline.StoredObjectReference{
		Store: reference.Store, Key: reference.Key, SchemaVersion: reference.SchemaVersion, Size: reference.Size,
	}
}

func (s *IdleBaselineSource) read(
	ctx context.Context,
	identity evidencestore.ObjectIdentity,
) ([]byte, error) {
	stream, err := s.objects.Open(ctx, identity)
	if err != nil {
		return nil, fmt.Errorf("open idle baseline result: %w", err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, identity.Size+1))
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if int64(len(payload)) != identity.Size {
		return nil, fmt.Errorf(
			"idle baseline result is %d bytes where the reference declares %d",
			len(payload), identity.Size,
		)
	}
	return payload, nil
}
