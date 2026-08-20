package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	batchsaturation "github.com/lab-paper-code/chill/internal/profiling/campaign"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

// IdleBaselineReader resolves one admitted target-mode idle aggregate.
type IdleBaselineReader interface {
	Read(
		context.Context,
		batchsaturation.ResultMeasurementState,
		batchsaturation.StoredObjectReference,
	) (ResolvedIdleBaseline, error)
}

// Preparer resolves the external facts needed before materializing transition
// observation and actuation objects.
type Preparer struct {
	metrics   profiler.MetricSourceResolver
	baselines IdleBaselineReader
}

func NewPreparer(
	metrics profiler.MetricSourceResolver,
	baselines IdleBaselineReader,
) (*Preparer, error) {
	if metrics == nil || baselines == nil {
		return nil, errors.New("transition metric source and idle baseline reader are required")
	}
	return &Preparer{metrics: metrics, baselines: baselines}, nil
}

// PreparedMeasurement contains only the selections consumed by the next Job
// and NodeActuation materialization boundary.
type PreparedMeasurement struct {
	Namespace                  string
	TargetNode                 string
	ServingService             string
	ServingContainer           string
	PowerSource                metricobserver.SourceIdentity
	PowerRequest               metricobserver.Request
	FactPollInterval           time.Duration
	FactObservationTimeout     time.Duration
	BoundaryMappingUncertainty time.Duration
	Policy                     transitioncost.Policy
	Directions                 [2]PreparedDirection
}

// PreparedDirection is one ordered transition with its target-mode baseline
// and the configured actuation-policy name needed to reach that target.
type PreparedDirection struct {
	FrozenContract           transitioncost.FrozenContract
	TargetIdleBaselineResult batchsaturation.StoredObjectReference
	ActuationPolicy          string
}

type preparedObservation struct {
	powerRequest               metricobserver.Request
	factPollInterval           time.Duration
	factObservationTimeout     time.Duration
	boundaryMappingUncertainty time.Duration
	policy                     transitioncost.Policy
}

func (p *Preparer) Prepare(
	ctx context.Context,
	measurement *edgev1alpha1.PowerModeTransitionCost,
) (PreparedMeasurement, error) {
	if err := validatePreparationInput(measurement); err != nil {
		return PreparedMeasurement{}, err
	}
	observation, err := prepareObservation(measurement.Spec.ObservationPolicy)
	if err != nil {
		return PreparedMeasurement{}, err
	}

	resolvedMetrics, err := p.metrics.ResolveMetricSource(ctx, measurement.Spec.TargetNode)
	if err != nil {
		return PreparedMeasurement{}, fmt.Errorf("resolve transition power source: %w", err)
	}
	powerSource, deviceType, err := preparePowerSource(resolvedMetrics)
	if err != nil {
		return PreparedMeasurement{}, err
	}

	var baselines [2]ResolvedIdleBaseline
	for index, mode := range measurement.Spec.Modes {
		expected := batchsaturation.ResultMeasurementState{
			TargetNode: measurement.Spec.TargetNode, ModelSpec: measurement.Spec.ModelSpec,
			DeviceClass: measurement.Spec.DeviceClass, ExecutionPath: measurement.Spec.ExecutionPath,
			PowerMode: mode.PowerMode,
		}
		baseline, err := p.baselines.Read(ctx, expected, storedObjectReference(mode.IdleBaseline))
		if err != nil {
			return PreparedMeasurement{}, fmt.Errorf("resolve idle baseline for power mode %q: %w", mode.PowerMode, err)
		}
		baselines[index] = baseline
	}

	state := func(mode string) transitioncost.StateIdentity {
		return transitioncost.StateIdentity{
			DeviceType: deviceType, Model: measurement.Spec.ModelSpec, PowerMode: mode,
		}
	}
	direction := func(sourceIndex, targetIndex int) PreparedDirection {
		sourceMode := measurement.Spec.Modes[sourceIndex]
		targetMode := measurement.Spec.Modes[targetIndex]
		baseline := baselines[targetIndex]
		reference := baseline.Reference()
		targetState := state(targetMode.PowerMode)
		return PreparedDirection{
			FrozenContract: transitioncost.FrozenContract{
				SourceState: state(sourceMode.PowerMode), TargetState: targetState,
				TargetIdleBaseline: transitioncost.IdleBaseline{
					EvidenceReference: reference.Key, State: targetState,
					MeanWallWatts: baseline.MeanWallWatts(),
				},
			},
			TargetIdleBaselineResult: reference,
			ActuationPolicy:          targetMode.ActuationPolicy,
		}
	}

	return PreparedMeasurement{
		Namespace: measurement.Namespace, TargetNode: measurement.Spec.TargetNode,
		ServingService: measurement.Spec.ServingService, ServingContainer: measurement.Spec.ServingContainer,
		PowerSource: powerSource, PowerRequest: observation.powerRequest,
		FactPollInterval: observation.factPollInterval, FactObservationTimeout: observation.factObservationTimeout,
		BoundaryMappingUncertainty: observation.boundaryMappingUncertainty, Policy: observation.policy,
		Directions: [2]PreparedDirection{direction(0, 1), direction(1, 0)},
	}, nil
}

func validatePreparationInput(measurement *edgev1alpha1.PowerModeTransitionCost) error {
	if measurement == nil {
		return errors.New("PowerModeTransitionCost is required")
	}
	if len(measurement.Spec.Modes) != 2 {
		return errors.New("PowerModeTransitionCost requires exactly two modes")
	}
	return nil
}

func preparePowerSource(resolved profiler.ResolvedMetricSource) (metricobserver.SourceIdentity, string, error) {
	if resolved.TransitionPower == nil {
		return metricobserver.SourceIdentity{}, "", errors.New("resolved metric source has no transition power source")
	}
	selected := resolved.TransitionPower
	_, hasHostname := selected.Labels["hostname"]
	deviceType, hasDeviceType := selected.Labels["device_type"]
	if len(selected.Labels) != 2 || !hasHostname || !hasDeviceType || !clean(deviceType) {
		return metricobserver.SourceIdentity{}, "", errors.New(
			"resolved transition power labels must contain exactly hostname and one clean device_type value",
		)
	}
	labels := maps.Clone(selected.Labels)
	return metricobserver.SourceIdentity{
		Kind: resolved.Kind, Endpoint: selected.Endpoint, Metric: selected.PowerMetric, MetricLabels: labels,
	}, deviceType, nil
}

func prepareObservation(
	spec edgev1alpha1.PowerModeTransitionCostObservationPolicySpec,
) (preparedObservation, error) {
	interval := time.Duration(spec.PowerIntervalMilliseconds) * time.Millisecond
	timeout := time.Duration(spec.PowerTimeoutMilliseconds) * time.Millisecond
	factTimeout := time.Duration(spec.MaximumTransitionDurationSeconds) * time.Second
	factPoll := time.Duration(spec.FactPollIntervalMilliseconds) * time.Millisecond
	boundaryMapping := time.Duration(spec.BoundaryMappingUncertaintyMilliseconds) * time.Millisecond
	maximumBoundaryUncertainty := time.Duration(spec.MaximumBoundaryUncertaintyMilliseconds) * time.Millisecond
	minimumCoverage, err := strconv.ParseFloat(spec.MinimumCoverage, 64)
	if err != nil {
		return preparedObservation{}, fmt.Errorf("parse minimum coverage: %w", err)
	}
	powerRequest, err := preparePowerRequest(interval, timeout, factTimeout+interval)
	if err != nil {
		return preparedObservation{}, err
	}
	return preparedObservation{
		powerRequest: powerRequest, factPollInterval: factPoll,
		factObservationTimeout: factTimeout, boundaryMappingUncertainty: boundaryMapping,
		policy: transitioncost.Policy{
			MaximumBoundaryDistance:      time.Duration(spec.MaximumBoundaryDistanceMilliseconds) * time.Millisecond,
			MinimumCoverage:              minimumCoverage,
			MaximumSampleGap:             time.Duration(spec.MaximumSampleGapMilliseconds) * time.Millisecond,
			MaximumBoundaryUncertainty:   maximumBoundaryUncertainty,
			MaximumReadyObservationDelay: time.Duration(spec.MaximumReadyObservationDelayMilliseconds) * time.Millisecond,
		},
	}, nil
}

func preparePowerRequest(interval, timeout, duration time.Duration) (metricobserver.Request, error) {
	events, err := retainedEventCount(duration, interval)
	if err != nil {
		return metricobserver.Request{}, err
	}
	return metricobserver.Request{
		Interval: interval, Duration: duration, RequestTimeout: timeout, MaxEvents: events,
	}, nil
}

func retainedEventCount(duration, interval time.Duration) (int, error) {
	events := uint64(duration/interval) + 2
	if events > uint64(math.MaxInt) {
		return 0, errors.New("power observation event count overflows int")
	}
	return int(events), nil
}

func storedObjectReference(reference edgev1alpha1.ProfilingRunEvidenceReference) batchsaturation.StoredObjectReference {
	return batchsaturation.StoredObjectReference{
		Store: reference.Store, Key: reference.Key, SchemaVersion: reference.SchemaVersion, Size: reference.Size,
	}
}

func clean(value string) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value
}
