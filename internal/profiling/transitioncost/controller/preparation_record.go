package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	batchsaturation "github.com/lab-paper-code/chill/internal/profiling/campaign"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	preparationRecordSchemaVersion = "chill.dacs.io/power-mode-transition-preparation.v1alpha1"
	preparationRecordDataKey       = "preparation.json"
	maximumDurationMilliseconds    = int64((1<<63 - 1) / time.Millisecond)
)

type MeasurementPreparer interface {
	Prepare(context.Context, *edgev1alpha1.PowerModeTransitionCost) (PreparedMeasurement, error)
}

// PreparationRecords resolves mutable inputs only when the campaign has no
// immutable enclosing preparation record. Existing records are decoded as the
// authority and are never compared with newly resolved inputs.
type PreparationRecords struct {
	client   client.Client
	scheme   *runtime.Scheme
	preparer MeasurementPreparer
}

func NewPreparationRecords(kubeClient client.Client, scheme *runtime.Scheme, preparer MeasurementPreparer) (*PreparationRecords, error) {
	if kubeClient == nil || scheme == nil || preparer == nil {
		return nil, errors.New("Kubernetes client, scheme, and transition measurement preparer are required")
	}
	return &PreparationRecords{client: kubeClient, scheme: scheme, preparer: preparer}, nil
}

func (r *PreparationRecords) Resolve(ctx context.Context, measurement *edgev1alpha1.PowerModeTransitionCost) (PreparedMeasurement, bool, error) {
	prepared, err := r.load(ctx, measurement)
	if err == nil {
		return prepared, false, nil
	}
	if !apierrors.IsNotFound(err) {
		return PreparedMeasurement{}, false, err
	}

	prepared, err = r.preparer.Prepare(ctx, measurement)
	if err != nil {
		return PreparedMeasurement{}, false, fmt.Errorf("prepare transition measurement: %w", err)
	}
	record, err := buildPreparationRecord(measurement, prepared, r.scheme)
	if err != nil {
		return PreparedMeasurement{}, false, err
	}
	if err := r.client.Create(ctx, record); err == nil {
		return prepared, true, nil
	} else if !apierrors.IsAlreadyExists(err) {
		return PreparedMeasurement{}, false, fmt.Errorf("create transition preparation ConfigMap: %w", err)
	}
	prepared, err = r.load(ctx, measurement)
	if err != nil {
		return PreparedMeasurement{}, false, fmt.Errorf("load concurrently created transition preparation: %w", err)
	}
	return prepared, false, nil
}

func (r *PreparationRecords) load(ctx context.Context, measurement *edgev1alpha1.PowerModeTransitionCost) (PreparedMeasurement, error) {
	key, err := preparationRecordKey(measurement)
	if err != nil {
		return PreparedMeasurement{}, err
	}
	record := &corev1.ConfigMap{}
	if err := r.client.Get(ctx, key, record); err != nil {
		return PreparedMeasurement{}, fmt.Errorf("get transition preparation ConfigMap %s: %w", key, err)
	}
	return decodePreparationConfigMap(measurement, record)
}

func buildPreparationRecord(measurement *edgev1alpha1.PowerModeTransitionCost, prepared PreparedMeasurement, scheme *runtime.Scheme) (*corev1.ConfigMap, error) {
	key, err := preparationRecordKey(measurement)
	if err != nil {
		return nil, err
	}
	if scheme == nil {
		return nil, errors.New("Kubernetes scheme is required")
	}
	payload, err := encodePreparationRecord(prepared)
	if err != nil {
		return nil, err
	}
	record := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name},
		Immutable:  ptr.To(true),
		BinaryData: map[string][]byte{preparationRecordDataKey: payload},
	}
	if err := controllerutil.SetControllerReference(measurement, record, scheme); err != nil {
		return nil, fmt.Errorf("set transition preparation ConfigMap owner: %w", err)
	}
	return record, nil
}

func preparationRecordKey(measurement *edgev1alpha1.PowerModeTransitionCost) (types.NamespacedName, error) {
	if measurement == nil || !clean(measurement.Namespace) || !clean(measurement.Name) || measurement.UID == "" {
		return types.NamespacedName{}, errors.New("PowerModeTransitionCost namespace, name, and UID are required")
	}
	return types.NamespacedName{Namespace: measurement.Namespace, Name: measurement.Name}, nil
}

func decodePreparationConfigMap(measurement *edgev1alpha1.PowerModeTransitionCost, record *corev1.ConfigMap) (PreparedMeasurement, error) {
	key, err := preparationRecordKey(measurement)
	if err != nil {
		return PreparedMeasurement{}, err
	}
	if record == nil || record.Namespace != key.Namespace || record.Name != key.Name ||
		!metav1.IsControlledBy(record, measurement) {
		return PreparedMeasurement{}, errors.New("transition preparation ConfigMap ownership does not match its campaign")
	}
	if record.Immutable == nil || !*record.Immutable {
		return PreparedMeasurement{}, errors.New("transition preparation ConfigMap is not immutable")
	}
	if len(record.Data) != 0 || len(record.BinaryData) != 1 {
		return PreparedMeasurement{}, errors.New("transition preparation ConfigMap has unexpected data keys")
	}
	payload := record.BinaryData[preparationRecordDataKey]
	if len(payload) == 0 {
		return PreparedMeasurement{}, errors.New("transition preparation payload is missing")
	}
	return decodePreparationRecord(payload)
}

type preparationRecordWire struct {
	SchemaVersion string                   `json:"schemaVersion"`
	Prepared      *preparedMeasurementWire `json:"prepared"`
}

type preparedMeasurementWire struct {
	Namespace                              string                                   `json:"namespace"`
	TargetNode                             edgev1alpha1.ProfilingRunObjectReference `json:"targetNode"`
	ObserverNodeName                       string                                   `json:"observerNodeName"`
	DeviceClass                            string                                   `json:"deviceClass"`
	ExecutionPath                          string                                   `json:"executionPath"`
	ServingService                         string                                   `json:"servingService"`
	ServingContainer                       string                                   `json:"servingContainer"`
	PowerSource                            preparationPowerSourceWire               `json:"powerSource"`
	PowerRequest                           preparationPowerRequestWire              `json:"powerRequest"`
	FactPollIntervalMilliseconds           int64                                    `json:"factPollIntervalMilliseconds"`
	FactObservationTimeoutMilliseconds     int64                                    `json:"factObservationTimeoutMilliseconds"`
	BoundaryMappingUncertaintyMilliseconds int64                                    `json:"boundaryMappingUncertaintyMilliseconds"`
	Policy                                 preparationPolicyWire                    `json:"policy"`
	Directions                             []preparationDirectionWire               `json:"directions"`
}

type preparationPowerSourceWire struct {
	Kind         string            `json:"kind"`
	Endpoint     string            `json:"endpoint"`
	Metric       string            `json:"metric"`
	MetricLabels map[string]string `json:"metricLabels"`
}

type preparationPowerRequestWire struct {
	IntervalMilliseconds       int64 `json:"intervalMilliseconds"`
	DurationMilliseconds       int64 `json:"durationMilliseconds"`
	RequestTimeoutMilliseconds int64 `json:"requestTimeoutMilliseconds"`
	MaxEvents                  int   `json:"maxEvents"`
}

type preparationPolicyWire struct {
	MaximumBoundaryDistanceMilliseconds      int64   `json:"maximumBoundaryDistanceMilliseconds"`
	MinimumCoverage                          float64 `json:"minimumCoverage"`
	MaximumSampleGapMilliseconds             int64   `json:"maximumSampleGapMilliseconds"`
	MaximumBoundaryUncertaintyMilliseconds   int64   `json:"maximumBoundaryUncertaintyMilliseconds"`
	MaximumReadyObservationDelayMilliseconds int64   `json:"maximumReadyObservationDelayMilliseconds"`
}

type preparationDirectionWire struct {
	FrozenContract           transitioncost.FrozenContract         `json:"frozenContract"`
	TargetIdleBaselineResult batchsaturation.StoredObjectReference `json:"targetIdleBaselineResult"`
	Actuator                 edgev1alpha1.PowerModeActuator        `json:"actuator"`
	ActuationPolicy          profiler.FrozenActuationPolicy        `json:"actuationPolicy"`
}

func encodePreparationRecord(prepared PreparedMeasurement) ([]byte, error) {
	wire, err := projectPreparationWire(prepared)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(preparationRecordWire{
		SchemaVersion: preparationRecordSchemaVersion,
		Prepared:      &wire,
	})
	if err != nil {
		return nil, fmt.Errorf("encode transition preparation: %w", err)
	}
	return payload, nil
}

func projectPreparationWire(prepared PreparedMeasurement) (preparedMeasurementWire, error) {
	directions := make([]preparationDirectionWire, len(prepared.Directions))
	for index := range prepared.Directions {
		direction := prepared.Directions[index]
		directions[index] = preparationDirectionWire{
			FrozenContract: direction.FrozenContract, TargetIdleBaselineResult: direction.TargetIdleBaselineResult,
			Actuator: *direction.Actuator.DeepCopy(), ActuationPolicy: direction.ActuationPolicy,
		}
	}
	wire := preparedMeasurementWire{
		Namespace: prepared.Namespace, TargetNode: prepared.TargetNode, ObserverNodeName: prepared.ObserverNodeName,
		DeviceClass: prepared.DeviceClass, ExecutionPath: prepared.ExecutionPath,
		ServingService: prepared.ServingService, ServingContainer: prepared.ServingContainer,
		PowerSource: preparationPowerSourceWire{
			Kind: prepared.PowerSource.Kind, Endpoint: prepared.PowerSource.Endpoint, Metric: prepared.PowerSource.Metric,
			MetricLabels: prepared.PowerSource.MetricLabels,
		},
		PowerRequest: preparationPowerRequestWire{
			MaxEvents: prepared.PowerRequest.MaxEvents,
		},
		Policy: preparationPolicyWire{MinimumCoverage: prepared.Policy.MinimumCoverage}, Directions: directions,
	}
	var durationErr error
	projectDuration := func(name string, value time.Duration, output *int64) {
		if durationErr == nil {
			*output, durationErr = exactMilliseconds(name, value)
		}
	}
	projectDuration("power interval", prepared.PowerRequest.Interval, &wire.PowerRequest.IntervalMilliseconds)
	projectDuration("power duration", prepared.PowerRequest.Duration, &wire.PowerRequest.DurationMilliseconds)
	projectDuration("power request timeout", prepared.PowerRequest.RequestTimeout, &wire.PowerRequest.RequestTimeoutMilliseconds)
	projectDuration("fact poll interval", prepared.FactPollInterval, &wire.FactPollIntervalMilliseconds)
	projectDuration("fact observation timeout", prepared.FactObservationTimeout, &wire.FactObservationTimeoutMilliseconds)
	projectDuration("boundary mapping uncertainty", prepared.BoundaryMappingUncertainty, &wire.BoundaryMappingUncertaintyMilliseconds)
	projectDuration("maximum boundary distance", prepared.Policy.MaximumBoundaryDistance, &wire.Policy.MaximumBoundaryDistanceMilliseconds)
	projectDuration("maximum sample gap", prepared.Policy.MaximumSampleGap, &wire.Policy.MaximumSampleGapMilliseconds)
	projectDuration("maximum boundary uncertainty", prepared.Policy.MaximumBoundaryUncertainty, &wire.Policy.MaximumBoundaryUncertaintyMilliseconds)
	projectDuration("maximum ready observation delay", prepared.Policy.MaximumReadyObservationDelay, &wire.Policy.MaximumReadyObservationDelayMilliseconds)
	if durationErr != nil {
		return preparedMeasurementWire{}, durationErr
	}
	return wire, nil
}

func exactMilliseconds(name string, duration time.Duration) (int64, error) {
	if duration%time.Millisecond != 0 {
		return 0, fmt.Errorf("%s must be an exact millisecond duration", name)
	}
	return duration.Milliseconds(), nil
}

func decodePreparationRecord(payload []byte) (PreparedMeasurement, error) {
	var record preparationRecordWire
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return PreparedMeasurement{}, fmt.Errorf("decode transition preparation: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return PreparedMeasurement{}, errors.New("transition preparation contains a trailing JSON value")
	}
	if record.SchemaVersion != preparationRecordSchemaVersion {
		return PreparedMeasurement{}, fmt.Errorf("unsupported transition preparation schema %q", record.SchemaVersion)
	}
	if record.Prepared == nil {
		return PreparedMeasurement{}, errors.New("transition preparation is missing prepared material")
	}
	return restorePreparedMeasurement(*record.Prepared)
}

func restorePreparedMeasurement(wire preparedMeasurementWire) (PreparedMeasurement, error) {
	for name, value := range map[string]string{
		"namespace": wire.Namespace, "target Node name": wire.TargetNode.Name, "target Node UID": wire.TargetNode.UID,
		"observer Node name": wire.ObserverNodeName, "DeviceClass": wire.DeviceClass,
		"execution path": wire.ExecutionPath, "serving Service": wire.ServingService,
		"serving container": wire.ServingContainer, "power source kind": wire.PowerSource.Kind,
		"power source endpoint": wire.PowerSource.Endpoint, "power metric": wire.PowerSource.Metric,
	} {
		if !clean(value) {
			return PreparedMeasurement{}, fmt.Errorf("transition preparation %s is required without surrounding whitespace", name)
		}
	}
	for role, name := range map[string]string{"target": wire.TargetNode.Name, "observer": wire.ObserverNodeName} {
		if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
			return PreparedMeasurement{}, fmt.Errorf("transition preparation %s Node name is invalid: %s", role, strings.Join(errs, "; "))
		}
	}
	if wire.ObserverNodeName == wire.TargetNode.Name {
		return PreparedMeasurement{}, errors.New("transition preparation observer must run away from the target Node")
	}
	if len(wire.PowerSource.MetricLabels) == 0 {
		return PreparedMeasurement{}, errors.New("transition preparation power metric labels are required")
	}
	if len(wire.Directions) != 2 {
		return PreparedMeasurement{}, errors.New("transition preparation requires exactly two directions")
	}

	prepared := PreparedMeasurement{
		Namespace: wire.Namespace, TargetNode: wire.TargetNode, ObserverNodeName: wire.ObserverNodeName,
		DeviceClass: wire.DeviceClass, ExecutionPath: wire.ExecutionPath,
		ServingService: wire.ServingService, ServingContainer: wire.ServingContainer,
		PowerSource: metricobserver.SourceIdentity{
			Kind: wire.PowerSource.Kind, Endpoint: wire.PowerSource.Endpoint, Metric: wire.PowerSource.Metric,
			MetricLabels: wire.PowerSource.MetricLabels,
		},
		PowerRequest: metricobserver.Request{MaxEvents: wire.PowerRequest.MaxEvents},
		Policy:       transitioncost.Policy{MinimumCoverage: wire.Policy.MinimumCoverage},
	}
	var durationErr error
	restoreDuration := func(name string, value int64, allowZero bool, output *time.Duration) {
		if durationErr == nil {
			*output, durationErr = restoreMilliseconds(name, value, allowZero)
		}
	}
	restoreDuration("power interval", wire.PowerRequest.IntervalMilliseconds, false, &prepared.PowerRequest.Interval)
	restoreDuration("power duration", wire.PowerRequest.DurationMilliseconds, false, &prepared.PowerRequest.Duration)
	restoreDuration("power request timeout", wire.PowerRequest.RequestTimeoutMilliseconds, false, &prepared.PowerRequest.RequestTimeout)
	restoreDuration("fact poll interval", wire.FactPollIntervalMilliseconds, false, &prepared.FactPollInterval)
	restoreDuration("fact observation timeout", wire.FactObservationTimeoutMilliseconds, false, &prepared.FactObservationTimeout)
	restoreDuration("boundary mapping uncertainty", wire.BoundaryMappingUncertaintyMilliseconds, true, &prepared.BoundaryMappingUncertainty)
	restoreDuration("maximum boundary distance", wire.Policy.MaximumBoundaryDistanceMilliseconds, true, &prepared.Policy.MaximumBoundaryDistance)
	restoreDuration("maximum sample gap", wire.Policy.MaximumSampleGapMilliseconds, false, &prepared.Policy.MaximumSampleGap)
	restoreDuration("maximum boundary uncertainty", wire.Policy.MaximumBoundaryUncertaintyMilliseconds, true, &prepared.Policy.MaximumBoundaryUncertainty)
	restoreDuration("maximum ready observation delay", wire.Policy.MaximumReadyObservationDelayMilliseconds, false, &prepared.Policy.MaximumReadyObservationDelay)
	if durationErr != nil {
		return PreparedMeasurement{}, durationErr
	}
	if prepared.Policy.MinimumCoverage <= 0 || prepared.Policy.MinimumCoverage > 1 {
		return PreparedMeasurement{}, errors.New("stored minimum coverage must be in (0, 1]")
	}
	if err := prepared.PowerRequest.Validate(); err != nil {
		return PreparedMeasurement{}, fmt.Errorf("validate stored power request: %w", err)
	}

	var directions [2]PreparedDirection
	for index := range wire.Directions {
		direction, err := restorePreparationDirection(wire.Directions[index])
		if err != nil {
			return PreparedMeasurement{}, fmt.Errorf("validate stored direction %d: %w", index, err)
		}
		directions[index] = direction
	}
	prepared.Directions = directions
	return prepared, nil
}

func restorePreparationDirection(wire preparationDirectionWire) (PreparedDirection, error) {
	for name, value := range map[string]string{
		"source device type": wire.FrozenContract.SourceState.DeviceType,
		"source model":       wire.FrozenContract.SourceState.Model, "source power mode": wire.FrozenContract.SourceState.PowerMode,
		"target device type": wire.FrozenContract.TargetState.DeviceType,
		"target model":       wire.FrozenContract.TargetState.Model, "target power mode": wire.FrozenContract.TargetState.PowerMode,
		"baseline evidence reference": wire.FrozenContract.TargetIdleBaseline.EvidenceReference,
		"baseline store":              wire.TargetIdleBaselineResult.Store, "baseline key": wire.TargetIdleBaselineResult.Key,
		"baseline schema": wire.TargetIdleBaselineResult.SchemaVersion,
	} {
		if !clean(value) {
			return PreparedDirection{}, fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	if wire.TargetIdleBaselineResult.Size < 1 {
		return PreparedDirection{}, errors.New("baseline result size must be positive")
	}
	if err := projectActuationTarget(wire.Actuator).Validate(); err != nil {
		return PreparedDirection{}, fmt.Errorf("validate actuator target: %w", err)
	}
	if err := wire.ActuationPolicy.Validate(); err != nil {
		return PreparedDirection{}, fmt.Errorf("validate frozen actuation policy: %w", err)
	}
	return PreparedDirection{
		FrozenContract: wire.FrozenContract, TargetIdleBaselineResult: wire.TargetIdleBaselineResult,
		Actuator: *wire.Actuator.DeepCopy(), ActuationPolicy: wire.ActuationPolicy,
	}, nil
}

func restoreMilliseconds(name string, value int64, allowZero bool) (time.Duration, error) {
	if value < 0 || (!allowZero && value == 0) {
		return 0, fmt.Errorf("stored %s milliseconds are invalid", name)
	}
	if value > maximumDurationMilliseconds {
		return 0, fmt.Errorf("stored %s milliseconds overflow time.Duration", name)
	}
	return time.Duration(value) * time.Millisecond, nil
}
