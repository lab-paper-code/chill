package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	transitioncontroller "github.com/lab-paper-code/chill/internal/profiling/transitioncost/controller"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestLoadContractIsStrictBoundedAndConvertsUnits(t *testing.T) {
	wire := validAttemptWire()
	payload, _ := json.Marshal(wire)
	contract, err := loadContract(payload, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if contract.powerRequest.Interval != 100*time.Millisecond ||
		contract.facts.ObservationTimeout != 4*time.Second ||
		contract.facts.DeviceType != "jetson-orin" || contract.facts.Model != "model" {
		t.Fatalf("converted attempt = %#v", contract)
	}

	invalidSchedule, equalDuration, shortDuration, retired := wire, wire, wire, wire
	invalidSchedule.PowerObservation.RequestTimeoutMilliseconds = 100
	equalDuration.PowerObservation.DurationMilliseconds = 4000
	shortDuration.PowerObservation.DurationMilliseconds = 3999
	retired.SchemaVersion = "chill.dacs.io/transition-observer-attempt.v1alpha1"
	for _, test := range []struct {
		name    string
		payload []byte
		limit   int64
	}{
		{name: "unknown", payload: bytes.Replace(
			payload, []byte("{"), []byte(`{"unknown":true,`), 1,
		), limit: int64(len(payload) + 32)},
		{name: "trailing", payload: append(
			append([]byte(nil), payload...), []byte(` {}`)...,
		), limit: int64(len(payload) + 3)},
		{name: "oversized", payload: payload, limit: int64(len(payload) - 1)},
		{name: "invalid schedule", payload: encodeAttempt(invalidSchedule), limit: 1 << 20},
		{name: "equal observer durations", payload: encodeAttempt(equalDuration), limit: 1 << 20},
		{name: "short power duration", payload: encodeAttempt(shortDuration), limit: 1 << 20},
		{name: "retired schema", payload: encodeAttempt(retired), limit: 1 << 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := loadContract(test.payload, test.limit); err == nil {
				t.Fatal("loadContract() unexpectedly succeeded")
			}
		})
	}
}

func encodeAttempt(wire attemptWire) []byte { payload, _ := json.Marshal(wire); return payload }

func TestAttemptJobInlineContractLoadsThroughCurrentConsumer(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	measurement := &edgev1alpha1.PowerModeTransitionCost{
		ObjectMeta: metav1.ObjectMeta{Namespace: "measure", Name: "transition-cost", UID: types.UID("measurement-uid")},
		Spec:       edgev1alpha1.PowerModeTransitionCostSpec{TargetNode: "target", EvidenceStore: "evidence"},
	}
	directions := [2]transitioncontroller.PreparedDirection{}
	directions[1].FrozenContract = transitioncost.FrozenContract{
		TargetState: transitioncost.StateIdentity{DeviceType: "device", Model: "model", PowerMode: "high"},
	}
	prepared := transitioncontroller.PreparedMeasurement{
		Namespace: "measure", TargetNode: edgev1alpha1.ProfilingRunObjectReference{Name: "target", UID: "target-uid"},
		ServingService: "serving", ServingContainer: "runtime",
		PowerSource: metricobserver.SourceIdentity{
			Kind: "edge-metrics", Endpoint: "http://metrics/prometheus", Metric: "power_watts",
			MetricLabels: map[string]string{"hostname": "target", "device_type": "device"},
		},
		PowerRequest: metricobserver.Request{
			Interval: 100 * time.Millisecond, Duration: 5 * time.Second,
			RequestTimeout: 50 * time.Millisecond, MaxEvents: 52,
		},
		FactPollInterval: 50 * time.Millisecond, FactObservationTimeout: 4 * time.Second,
		BoundaryMappingUncertainty: 50 * time.Millisecond, Directions: directions,
	}
	job, err := transitioncontroller.BuildAttemptJob(
		transitioncontroller.AttemptJobRequest{
			Measurement: measurement, Prepared: prepared, DirectionIndex: 1,
			NodeActuationName: "transition-1",
			RawLocation:       evidencestore.ObjectLocation{Store: "evidence", Key: "transition/raw.json"},
			StoreBinding: storekube.Binding{
				Store: "evidence", Endpoint: "http://store", Bucket: "bucket", Region: "region",
				CredentialsSecretName: "credentials",
			},
		},
		transitioncontroller.AttemptJobOptions{
			ObserverImage: "registry/transition-observer@sha256:fixture", ImagePullPolicy: corev1.PullIfNotPresent,
			ServiceAccountName: "transition-observer", ObserverNodeName: "observer",
			ActiveDeadline: 6 * time.Second, CoordinationBytes: 1024, WorkspaceBytes: 4096,
		},
		scheme,
	)
	if err != nil {
		t.Fatal(err)
	}
	var payload string
	for _, variable := range job.Spec.Template.Spec.Containers[0].Env {
		if variable.Name == "CHILL_TRANSITION_ATTEMPT_JSON" {
			payload = variable.Value
		}
	}
	contract, err := loadContract([]byte(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if contract.facts.NodeActuationName != "transition-1" || contract.facts.DeviceType != "device" ||
		contract.facts.Model != "model" || contract.rawLocation.Key != "transition/raw.json" ||
		contract.powerRequest != prepared.PowerRequest {
		t.Fatalf("decoded Job attempt = %#v", contract)
	}
}

func validAttemptWire() attemptWire {
	return attemptWire{
		SchemaVersion: attemptSchemaVersion,
		PowerSource: powerSourceWire{
			Kind: "edge-metrics", Endpoint: "http://metrics/prometheus", Metric: "power_watts",
			MetricLabels: map[string]string{"device": "target"},
		},
		PowerObservation: powerObservationWire{
			IntervalMilliseconds: 100, DurationMilliseconds: 5000,
			RequestTimeoutMilliseconds: 50, MaxEvents: 52,
		},
		TransitionFacts: transitionFactsWire{
			Namespace: "measure", NodeActuationName: "transition-1", TargetNode: "target",
			DeviceType: "jetson-orin", Model: "model",
			ServingService: "serving", ServingContainer: "runtime",
			PollIntervalMilliseconds: 50, ObservationTimeoutMilliseconds: 4000,
			BoundaryMappingUncertaintyMilliseconds: 50,
		},
		RawLocation: evidencestore.ObjectLocation{Store: "evidence", Key: "transition/raw.json"},
	}
}
