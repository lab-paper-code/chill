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
	preparationRecordSchemaVersion = "chill.dacs.io/power-mode-transition-preparation.v1alpha4"
	preparationRecordDataKey       = "preparation.json"
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
	SchemaVersion string               `json:"schemaVersion"`
	Prepared      *PreparedMeasurement `json:"prepared"`
}

func encodePreparationRecord(prepared PreparedMeasurement) ([]byte, error) {
	return json.Marshal(preparationRecordWire{
		SchemaVersion: preparationRecordSchemaVersion, Prepared: &prepared,
	})
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
	if err := validateStoredPreparation(*record.Prepared); err != nil {
		return PreparedMeasurement{}, err
	}
	return *record.Prepared, nil
}

func validateStoredPreparation(prepared PreparedMeasurement) error {
	for name, value := range map[string]string{
		"namespace": prepared.Namespace, "target Node name": prepared.TargetNode.Name, "target Node UID": prepared.TargetNode.UID,
		"observer Node name": prepared.ObserverNodeName, "Evidence Store": prepared.EvidenceStore,
		"DeviceClass": prepared.DeviceClass, "execution path": prepared.ExecutionPath,
		"serving Service": prepared.ServingService, "serving container": prepared.ServingContainer,
		"power source kind": prepared.PowerSource.Kind, "power source endpoint": prepared.PowerSource.Endpoint,
		"power metric": prepared.PowerSource.Metric,
	} {
		if !clean(value) {
			return fmt.Errorf("transition preparation %s is required without surrounding whitespace", name)
		}
	}
	for role, name := range map[string]string{"target": prepared.TargetNode.Name, "observer": prepared.ObserverNodeName} {
		if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
			return fmt.Errorf("transition preparation %s Node name is invalid: %s", role, strings.Join(errs, "; "))
		}
	}
	if prepared.ObserverNodeName == prepared.TargetNode.Name {
		return errors.New("transition preparation observer must run away from the target Node")
	}
	if len(prepared.PowerSource.MetricLabels) == 0 {
		return errors.New("transition preparation power metric labels are required")
	}
	if len(prepared.Directions) != 2 {
		return errors.New("transition preparation requires exactly two directions")
	}
	for name, duration := range map[string]time.Duration{
		"power interval": prepared.PowerRequest.Interval, "power duration": prepared.PowerRequest.Duration,
		"power request timeout": prepared.PowerRequest.RequestTimeout,
		"fact poll interval":    prepared.FactPollInterval, "fact observation timeout": prepared.FactObservationTimeout,
		"maximum sample gap":              prepared.Policy.MaximumSampleGap,
		"maximum ready observation delay": prepared.Policy.MaximumReadyObservationDelay,
	} {
		if duration <= 0 || duration%time.Millisecond != 0 {
			return fmt.Errorf("stored %s must be a positive whole-millisecond duration", name)
		}
	}
	for _, duration := range []time.Duration{prepared.Policy.MaximumBoundaryDistance, prepared.Policy.MaximumBoundaryUncertainty} {
		if duration < 0 || duration%time.Millisecond != 0 {
			return errors.New("stored boundary bounds must be non-negative whole-millisecond durations")
		}
	}
	if prepared.Policy.MinimumCoverage <= 0 || prepared.Policy.MinimumCoverage > 1 {
		return errors.New("stored minimum coverage must be in (0, 1]")
	}
	if err := prepared.PowerRequest.Validate(); err != nil {
		return fmt.Errorf("validate stored power request: %w", err)
	}
	for _, direction := range prepared.Directions {
		for name, value := range map[string]string{
			"source device type": direction.FrozenContract.SourceState.DeviceType,
			"source model":       direction.FrozenContract.SourceState.Model, "source power mode": direction.FrozenContract.SourceState.PowerMode,
			"target device type": direction.FrozenContract.TargetState.DeviceType,
			"target model":       direction.FrozenContract.TargetState.Model, "target power mode": direction.FrozenContract.TargetState.PowerMode,
			"baseline evidence reference": direction.FrozenContract.TargetIdleBaseline.EvidenceReference,
			"baseline store":              direction.TargetIdleBaselineResult.Store, "baseline key": direction.TargetIdleBaselineResult.Key,
			"baseline schema": direction.TargetIdleBaselineResult.SchemaVersion,
		} {
			if !clean(value) {
				return fmt.Errorf("%s is required without surrounding whitespace", name)
			}
		}
		if direction.TargetIdleBaselineResult.Size < 1 {
			return errors.New("baseline result size must be positive")
		}
		if err := projectActuationTarget(direction.Actuator).Validate(); err != nil {
			return fmt.Errorf("validate actuator target: %w", err)
		}
		if err := direction.ActuationPolicy.Validate(); err != nil {
			return fmt.Errorf("validate frozen actuation policy: %w", err)
		}
	}
	return nil
}
