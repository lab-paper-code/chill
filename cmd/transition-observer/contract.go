package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	transitionkube "github.com/lab-paper-code/chill/internal/profiling/transitioncost/kube"
)

const attemptSchemaVersion = "chill.dacs.io/transition-observer-attempt.v1alpha2"

type attemptContract struct {
	powerSource  metricobserver.SourceIdentity
	powerRequest metricobserver.Request
	facts        transitionkube.Request
	rawLocation  evidencestore.ObjectLocation
}

type attemptWire struct {
	SchemaVersion    string                       `json:"schemaVersion"`
	PowerSource      powerSourceWire              `json:"powerSource"`
	PowerObservation powerObservationWire         `json:"powerObservation"`
	TransitionFacts  transitionFactsWire          `json:"transitionFacts"`
	RawLocation      evidencestore.ObjectLocation `json:"rawLocation"`
}

type powerSourceWire struct {
	Kind         string            `json:"kind"`
	Endpoint     string            `json:"endpoint"`
	Metric       string            `json:"metric"`
	MetricLabels map[string]string `json:"metricLabels"`
}

type powerObservationWire struct {
	IntervalMilliseconds       milliseconds `json:"intervalMilliseconds"`
	DurationMilliseconds       milliseconds `json:"durationMilliseconds"`
	RequestTimeoutMilliseconds milliseconds `json:"requestTimeoutMilliseconds"`
	MaxEvents                  int          `json:"maxEvents"`
}

type transitionFactsWire struct {
	Namespace                              string       `json:"namespace"`
	NodeActuationName                      string       `json:"nodeActuationName"`
	TargetNode                             string       `json:"targetNode"`
	DeviceType                             string       `json:"deviceType"`
	Model                                  string       `json:"model"`
	ServingService                         string       `json:"servingService"`
	ServingContainer                       string       `json:"servingContainer"`
	PollIntervalMilliseconds               milliseconds `json:"pollIntervalMilliseconds"`
	ObservationTimeoutMilliseconds         milliseconds `json:"observationTimeoutMilliseconds"`
	BoundaryMappingUncertaintyMilliseconds milliseconds `json:"boundaryMappingUncertaintyMilliseconds"`
}

type milliseconds time.Duration

func (m *milliseconds) UnmarshalJSON(payload []byte) error {
	var value int64
	if err := json.Unmarshal(payload, &value); err != nil {
		return err
	}
	const maximum = int64(^uint64(0)>>1) / int64(time.Millisecond)
	if value > maximum || value < -maximum {
		return errors.New("millisecond duration overflows time.Duration")
	}
	*m = milliseconds(time.Duration(value) * time.Millisecond)
	return nil
}

func loadContract(payload []byte, maximumBytes int64) (attemptContract, error) {
	if maximumBytes < 1 || maximumBytes == int64(^uint64(0)>>1) {
		return attemptContract{}, errors.New("maximum transition attempt contract bytes must be positive")
	}
	if int64(len(payload)) > maximumBytes {
		return attemptContract{}, fmt.Errorf("transition attempt contract exceeds %d-byte limit", maximumBytes)
	}

	var wire attemptWire
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return attemptContract{}, fmt.Errorf("decode transition attempt contract: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return attemptContract{}, errors.New("transition attempt contract contains a trailing JSON value")
	}
	if wire.SchemaVersion != attemptSchemaVersion {
		return attemptContract{}, fmt.Errorf("unsupported transition attempt schemaVersion %q", wire.SchemaVersion)
	}
	return wire.contract()
}

func (w attemptWire) contract() (attemptContract, error) {
	powerRequest := metricobserver.Request{
		Interval:       time.Duration(w.PowerObservation.IntervalMilliseconds),
		Duration:       time.Duration(w.PowerObservation.DurationMilliseconds),
		RequestTimeout: time.Duration(w.PowerObservation.RequestTimeoutMilliseconds),
		MaxEvents:      w.PowerObservation.MaxEvents,
	}
	if err := powerRequest.Validate(); err != nil {
		return attemptContract{}, fmt.Errorf("validate power observation schedule: %w", err)
	}
	if powerRequest.Duration <= time.Duration(w.TransitionFacts.ObservationTimeoutMilliseconds) {
		return attemptContract{}, errors.New("power observation must outlast transition fact observation")
	}
	if err := w.RawLocation.Validate(); err != nil {
		return attemptContract{}, fmt.Errorf("validate raw evidence location: %w", err)
	}
	return attemptContract{
		powerSource: metricobserver.SourceIdentity{
			Kind: w.PowerSource.Kind, Endpoint: w.PowerSource.Endpoint,
			Metric: w.PowerSource.Metric, MetricLabels: w.PowerSource.MetricLabels,
		},
		powerRequest: powerRequest,
		facts: transitionkube.Request{
			Namespace: w.TransitionFacts.Namespace, NodeActuationName: w.TransitionFacts.NodeActuationName,
			TargetNode: w.TransitionFacts.TargetNode, ServingService: w.TransitionFacts.ServingService,
			ServingContainer: w.TransitionFacts.ServingContainer,
			DeviceType:       w.TransitionFacts.DeviceType, Model: w.TransitionFacts.Model,
			PollInterval:               time.Duration(w.TransitionFacts.PollIntervalMilliseconds),
			ObservationTimeout:         time.Duration(w.TransitionFacts.ObservationTimeoutMilliseconds),
			BoundaryMappingUncertainty: time.Duration(w.TransitionFacts.BoundaryMappingUncertaintyMilliseconds),
		},
		rawLocation: w.RawLocation,
	}, nil
}
