package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPreparationRecordsCreateThenRecoverWithoutPreparingAgain(t *testing.T) {
	measurement, prepared, scheme := preparationRecordFixture(t)
	preparer := &countingMeasurementPreparer{result: prepared}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	records := mustPreparationRecords(t, kubeClient, scheme, preparer)

	createdPreparation, created, err := records.Resolve(context.Background(), measurement)
	if err != nil {
		t.Fatal(err)
	}
	if !created || preparer.calls != 1 || createdPreparation.ObserverNodeName != prepared.ObserverNodeName {
		t.Fatalf("first resolution = created:%t calls:%d prepared:%#v", created, preparer.calls, createdPreparation)
	}
	record := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: measurement.Namespace, Name: measurement.Name}
	if err := kubeClient.Get(context.Background(), key, record); err != nil {
		t.Fatal(err)
	}
	if record.Immutable == nil || !*record.Immutable || len(record.BinaryData) != 1 ||
		len(record.Data) != 0 || !metav1.IsControlledBy(record, measurement) {
		t.Fatalf("preparation ConfigMap = %#v", record)
	}

	restartPreparer := &countingMeasurementPreparer{err: errors.New("mutable sources must not be read")}
	restarted := mustPreparationRecords(t, kubeClient, scheme, restartPreparer)
	recovered, created, err := restarted.Resolve(context.Background(), measurement)
	if err != nil {
		t.Fatal(err)
	}
	if created || restartPreparer.calls != 0 {
		t.Fatalf("restart resolution = created:%t preparer calls:%d", created, restartPreparer.calls)
	}
	assertRecoveredPreparation(t, recovered, prepared)
}

func TestPreparationRecordsAlreadyExistsUsesWinnerWithoutComparison(t *testing.T) {
	measurement, winner, scheme := preparationRecordFixture(t)
	winnerRecord, err := buildPreparationRecord(measurement, winner, scheme)
	if err != nil {
		t.Fatal(err)
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(winnerRecord).Build()
	racing := &alreadyExistsPreparationClient{Client: base}
	loser := winner
	loser.ObserverNodeName = "other-observer"
	preparer := &countingMeasurementPreparer{result: loser}

	resolved, created, err := mustPreparationRecords(t, racing, scheme, preparer).
		Resolve(context.Background(), measurement)
	if err != nil {
		t.Fatal(err)
	}
	if created || preparer.calls != 1 || racing.creates != 1 || resolved.ObserverNodeName != winner.ObserverNodeName {
		t.Fatalf("race resolution = created:%t prepare:%d create:%d observer:%q",
			created, preparer.calls, racing.creates, resolved.ObserverNodeName)
	}
}

func TestPreparationRecordRejectsMalformedCurrentRecord(t *testing.T) {
	measurement, prepared, scheme := preparationRecordFixture(t)
	desired, err := buildPreparationRecord(measurement, prepared, scheme)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*corev1.ConfigMap)
	}{
		{name: "mutable", mutate: func(record *corev1.ConfigMap) { record.Immutable = nil }},
		{name: "foreign owner", mutate: func(record *corev1.ConfigMap) { record.OwnerReferences[0].UID = "foreign" }},
		{name: "unknown field", mutate: func(record *corev1.ConfigMap) {
			record.BinaryData[preparationRecordDataKey] = []byte(strings.Replace(
				string(record.BinaryData[preparationRecordDataKey]), `"prepared":`, `"unknown":true,"prepared":`, 1,
			))
		}},
		{name: "old schema", mutate: func(record *corev1.ConfigMap) {
			record.BinaryData[preparationRecordDataKey] = []byte(strings.Replace(
				string(record.BinaryData[preparationRecordDataKey]), preparationRecordSchemaVersion,
				"chill.dacs.io/power-mode-transition-preparation.v1alpha2", 1,
			))
		}},
		{name: "missing Evidence Store", mutate: func(record *corev1.ConfigMap) {
			mutatePreparationWire(t, record, func(wire *preparationRecordWire) {
				wire.Prepared.EvidenceStore = ""
			})
		}},
		{name: "trailing value", mutate: func(record *corev1.ConfigMap) {
			record.BinaryData[preparationRecordDataKey] = append(record.BinaryData[preparationRecordDataKey], []byte(" {}")...)
		}},
		{name: "direction count", mutate: func(record *corev1.ConfigMap) {
			mutatePreparationWire(t, record, func(wire *preparationRecordWire) {
				wire.Prepared.Directions = wire.Prepared.Directions[:1]
			})
		}},
		{name: "duration overflow", mutate: func(record *corev1.ConfigMap) {
			mutatePreparationWire(t, record, func(wire *preparationRecordWire) {
				wire.Prepared.FactObservationTimeoutMilliseconds = maximumDurationMilliseconds + 1
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := desired.DeepCopy()
			test.mutate(record)
			if _, err := decodePreparationConfigMap(measurement, record); err == nil {
				t.Fatal("malformed preparation record was accepted")
			}
		})
	}
}

func assertRecoveredPreparation(t *testing.T, recovered, expected PreparedMeasurement) {
	t.Helper()
	if recovered.Namespace != expected.Namespace || recovered.TargetNode != expected.TargetNode ||
		recovered.ObserverNodeName != expected.ObserverNodeName || recovered.EvidenceStore != expected.EvidenceStore ||
		recovered.DeviceClass != expected.DeviceClass ||
		recovered.ExecutionPath != expected.ExecutionPath || recovered.ServingService != expected.ServingService ||
		recovered.ServingContainer != expected.ServingContainer ||
		recovered.PowerSource.Kind != expected.PowerSource.Kind ||
		recovered.PowerSource.Endpoint != expected.PowerSource.Endpoint ||
		recovered.PowerSource.Metric != expected.PowerSource.Metric ||
		recovered.PowerSource.MetricLabels["device_type"] != expected.PowerSource.MetricLabels["device_type"] ||
		recovered.PowerRequest != expected.PowerRequest || recovered.FactPollInterval != expected.FactPollInterval ||
		recovered.FactObservationTimeout != expected.FactObservationTimeout ||
		recovered.Policy != expected.Policy {
		t.Fatalf("recovered enclosing preparation = %#v", recovered)
	}
	for index := range recovered.Directions {
		got := recovered.Directions[index]
		want := expected.Directions[index]
		if got.FrozenContract != want.FrozenContract || got.TargetIdleBaselineResult != want.TargetIdleBaselineResult ||
			got.Actuator.Kind != want.Actuator.Kind || got.Actuator.JetsonNVPModel == nil ||
			got.Actuator.JetsonNVPModel.ID != want.Actuator.JetsonNVPModel.ID ||
			got.ActuationPolicy.Policy != want.ActuationPolicy.Policy ||
			got.ActuationPolicy.Source != want.ActuationPolicy.Source {
			t.Fatalf("recovered direction %d = %#v", index, got)
		}
	}
}

func preparationRecordFixture(
	t *testing.T,
) (*edgev1alpha1.PowerModeTransitionCost, PreparedMeasurement, *runtime.Scheme) {
	t.Helper()
	measurement := validTransitionMeasurement()
	measurement.Name = "transition-cost"
	measurement.UID = types.UID("transition-cost-uid")
	lowReference := baselineStoredReference("low")
	highReference := baselineStoredReference("high")
	baselines := &recordingIdleBaselineReader{results: map[string]ResolvedIdleBaseline{
		"low": {meanWallWatts: 8, reference: lowReference}, "high": {meanWallWatts: 12, reference: highReference},
	}}
	prepared, err := mustPreparer(t, validTransitionMetricResolver(), baselines).
		Prepare(context.Background(), measurement)
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return measurement, prepared, scheme
}

func mustPreparationRecords(
	t *testing.T,
	kubeClient client.Client,
	scheme *runtime.Scheme,
	preparer MeasurementPreparer,
) *PreparationRecords {
	t.Helper()
	records, err := NewPreparationRecords(kubeClient, scheme, preparer)
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func mutatePreparationWire(
	t *testing.T,
	record *corev1.ConfigMap,
	mutate func(*preparationRecordWire),
) {
	t.Helper()
	var wire preparationRecordWire
	if err := json.Unmarshal(record.BinaryData[preparationRecordDataKey], &wire); err != nil {
		t.Fatal(err)
	}
	mutate(&wire)
	payload, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	record.BinaryData[preparationRecordDataKey] = payload
}

type countingMeasurementPreparer struct {
	result PreparedMeasurement
	err    error
	calls  int
}

func (p *countingMeasurementPreparer) Prepare(
	context.Context,
	*edgev1alpha1.PowerModeTransitionCost,
) (PreparedMeasurement, error) {
	p.calls++
	return p.result, p.err
}

type alreadyExistsPreparationClient struct {
	client.Client
	firstGet bool
	creates  int
}

func (c *alreadyExistsPreparationClient) Get(
	ctx context.Context,
	key client.ObjectKey,
	object client.Object,
	options ...client.GetOption,
) error {
	if _, ok := object.(*corev1.ConfigMap); ok && !c.firstGet {
		c.firstGet = true
		return apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, key.Name)
	}
	return c.Client.Get(ctx, key, object, options...)
}

func (c *alreadyExistsPreparationClient) Create(
	context.Context,
	client.Object,
	...client.CreateOption,
) error {
	c.creates++
	return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "configmaps"}, "transition-cost")
}
