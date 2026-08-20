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
	"github.com/lab-paper-code/chill/internal/deviceclass"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	idlebaseline "github.com/lab-paper-code/chill/internal/profiling/idlebaseline"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IdleBaselineReader resolves one admitted target-mode idle aggregate.
type IdleBaselineReader interface {
	Read(
		context.Context,
		idlebaseline.MeasurementState,
		idlebaseline.StoredObjectReference,
	) (ResolvedIdleBaseline, error)
}

// Preparer resolves the external facts needed before materializing transition
// observation and actuation objects.
type Preparer struct {
	deviceClasses client.Reader
	nodes         profiler.NodeResolver
	metrics       profiler.MetricSourceResolver
	baselines     IdleBaselineReader
	actuation     profiler.ActuationPolicyResolver
	observerNode  string
}

func NewPreparer(
	deviceClasses client.Reader,
	nodes profiler.NodeResolver,
	metrics profiler.MetricSourceResolver,
	baselines IdleBaselineReader,
	actuation profiler.ActuationPolicyResolver,
	observerNode string,
) (*Preparer, error) {
	if deviceClasses == nil || nodes == nil || metrics == nil || baselines == nil || actuation == nil {
		return nil, errors.New(
			"DeviceClass reader, Node, transition metric source, idle baseline, and actuation-policy resolvers are required",
		)
	}
	if errs := validation.IsDNS1123Subdomain(observerNode); len(errs) > 0 {
		return nil, fmt.Errorf("transition observer Node name is invalid: %s", strings.Join(errs, "; "))
	}
	return &Preparer{
		deviceClasses: deviceClasses, nodes: nodes, metrics: metrics, baselines: baselines, actuation: actuation,
		observerNode: observerNode,
	}, nil
}

// PreparedMeasurement contains only the selections consumed by the next Job
// and NodeActuation materialization boundary.
type PreparedMeasurement struct {
	Namespace                  string
	TargetNode                 edgev1alpha1.ProfilingRunObjectReference
	ObserverNodeName           string
	DeviceClass                string
	ExecutionPath              string
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

// PreparedDirection is one ordered transition with its target-mode baseline,
// typed actuator target, and frozen configured policy needed to reach it.
type PreparedDirection struct {
	FrozenContract           transitioncost.FrozenContract
	TargetIdleBaselineResult idlebaseline.StoredObjectReference
	Actuator                 edgev1alpha1.PowerModeActuator
	ActuationPolicy          profiler.FrozenActuationPolicy
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
	if p.observerNode == measurement.Spec.TargetNode {
		return PreparedMeasurement{}, errors.New("transition observer must run away from the target Node")
	}
	observation, err := prepareObservation(measurement.Spec.ObservationPolicy)
	if err != nil {
		return PreparedMeasurement{}, err
	}

	class := &edgev1alpha1.DeviceClass{}
	if err := p.deviceClasses.Get(
		ctx,
		types.NamespacedName{Name: measurement.Spec.DeviceClass},
		class,
	); err != nil {
		return PreparedMeasurement{}, fmt.Errorf("get DeviceClass %q: %w", measurement.Spec.DeviceClass, err)
	}
	if errs := deviceclass.ValidatePowerModes(class.Spec.PowerModes, field.NewPath("powerModes")); len(errs) > 0 {
		return PreparedMeasurement{}, fmt.Errorf(
			"validate DeviceClass %q power modes: %w",
			measurement.Spec.DeviceClass,
			errs.ToAggregate(),
		)
	}
	var modes [2]edgev1alpha1.PowerMode
	var policies [2]profiler.FrozenActuationPolicy
	for index, requested := range measurement.Spec.Modes {
		mode, found := selectPowerMode(class.Spec.PowerModes, requested.PowerMode)
		if !found {
			return PreparedMeasurement{}, fmt.Errorf(
				"DeviceClass %q does not declare power mode %q",
				measurement.Spec.DeviceClass,
				requested.PowerMode,
			)
		}
		modes[index] = mode
	}
	node, err := p.nodes.ResolveNode(ctx, profiler.NodeSelection{
		Name: measurement.Spec.TargetNode, DeviceArchitecture: class.Spec.Architecture,
		DeviceClassSelector: maps.Clone(class.Spec.NodeSelector),
	})
	if err != nil {
		return PreparedMeasurement{}, fmt.Errorf("resolve transition target Node: %w", err)
	}
	for index, mode := range modes {
		selection := profiler.ActuationSelection{
			DeviceClass: measurement.Spec.DeviceClass,
			PowerMode:   mode.Name,
			Actuator:    projectActuationTarget(mode.Actuator),
		}
		policy, err := p.actuation.ResolveActuationPolicy(ctx, selection)
		if err != nil {
			return PreparedMeasurement{}, fmt.Errorf(
				"resolve actuation policy for power mode %q: %w",
				mode.Name,
				err,
			)
		}
		if policy.Policy.Name != measurement.Spec.Modes[index].ActuationPolicy {
			return PreparedMeasurement{}, fmt.Errorf(
				"resolved actuation policy %q does not match configured policy %q for power mode %q",
				policy.Policy.Name,
				measurement.Spec.Modes[index].ActuationPolicy,
				mode.Name,
			)
		}
		policies[index] = policy
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
		expected := idlebaseline.MeasurementState{
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
			Actuator:                 *modes[targetIndex].Actuator.DeepCopy(),
			ActuationPolicy:          policies[targetIndex],
		}
	}

	return PreparedMeasurement{
		Namespace:        measurement.Namespace,
		TargetNode:       edgev1alpha1.ProfilingRunObjectReference{Name: node.Name, UID: node.UID},
		ObserverNodeName: p.observerNode,
		DeviceClass:      measurement.Spec.DeviceClass, ExecutionPath: measurement.Spec.ExecutionPath,
		ServingService: measurement.Spec.ServingService, ServingContainer: measurement.Spec.ServingContainer,
		PowerSource: powerSource, PowerRequest: observation.powerRequest,
		FactPollInterval: observation.factPollInterval, FactObservationTimeout: observation.factObservationTimeout,
		BoundaryMappingUncertainty: observation.boundaryMappingUncertainty, Policy: observation.policy,
		Directions: [2]PreparedDirection{direction(0, 1), direction(1, 0)},
	}, nil
}

func selectPowerMode(modes []edgev1alpha1.PowerMode, name string) (edgev1alpha1.PowerMode, bool) {
	for index := range modes {
		if modes[index].Name == name {
			return *modes[index].DeepCopy(), true
		}
	}
	return edgev1alpha1.PowerMode{}, false
}

func projectActuationTarget(actuator edgev1alpha1.PowerModeActuator) profilingrun.ActuatorTarget {
	projected := profilingrun.ActuatorTarget{Kind: string(actuator.Kind)}
	if actuator.JetsonNVPModel != nil {
		projected.JetsonNVPModel = &profilingrun.JetsonNVPModelTarget{ID: actuator.JetsonNVPModel.ID}
	}
	if actuator.NvidiaLockedGPUClock != nil {
		projected.NvidiaLockedGPUClock = &profilingrun.NvidiaLockedGPUClockTarget{
			MinMHz: actuator.NvidiaLockedGPUClock.MinMHz,
			MaxMHz: actuator.NvidiaLockedGPUClock.MaxMHz,
		}
		if actuator.NvidiaLockedGPUClock.Mode != nil {
			mode := *actuator.NvidiaLockedGPUClock.Mode
			projected.NvidiaLockedGPUClock.Mode = &mode
		}
	}
	return projected
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
	_, hasHostname := resolved.MetricLabels["hostname"]
	deviceType, hasDeviceType := resolved.MetricLabels["device_type"]
	if len(resolved.MetricLabels) != 2 || !hasHostname || !hasDeviceType || !clean(deviceType) {
		return metricobserver.SourceIdentity{}, "", errors.New(
			"resolved power labels must contain exactly hostname and one clean device_type value",
		)
	}
	labels := maps.Clone(resolved.MetricLabels)
	return metricobserver.SourceIdentity{
		Kind: resolved.Kind, Endpoint: resolved.Endpoint, Metric: resolved.PowerMetric, MetricLabels: labels,
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

func storedObjectReference(reference edgev1alpha1.ProfilingRunEvidenceReference) idlebaseline.StoredObjectReference {
	return idlebaseline.StoredObjectReference{
		Store: reference.Store, Key: reference.Key, SchemaVersion: reference.SchemaVersion, Size: reference.Size,
	}
}

func clean(value string) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value
}
