package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

func TestLoadContractIsStrictBoundedAndConvertsUnits(t *testing.T) {
	wire := validAttemptWire()
	payload, _ := json.Marshal(wire)
	path := filepath.Join(t.TempDir(), "attempt.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	contract, err := loadContract(path, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if contract.powerRequest.Interval != 100*time.Millisecond ||
		contract.facts.ObservationTimeout != 4*time.Second ||
		contract.policy.MaximumSampleGap != time.Second {
		t.Fatalf("converted attempt = %#v", contract)
	}

	invalidSchedule, equalDuration, shortDuration := wire, wire, wire
	invalidSchedule.PowerObservation.RequestTimeoutMilliseconds = 100
	equalDuration.PowerObservation.DurationMilliseconds = 4000
	shortDuration.PowerObservation.DurationMilliseconds = 3999
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
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, test.payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadContract(path, test.limit); err == nil {
				t.Fatal("loadContract() unexpectedly succeeded")
			}
		})
	}
}

func encodeAttempt(wire attemptWire) []byte { payload, _ := json.Marshal(wire); return payload }

func validAttemptWire() attemptWire {
	source := transitioncost.StateIdentity{DeviceType: "jetson-orin", Model: "model", PowerMode: "MODE_15W"}
	target := transitioncost.StateIdentity{DeviceType: "jetson-orin", Model: "model", PowerMode: "MAXN"}
	return attemptWire{
		SchemaVersion: attemptSchemaVersion,
		FrozenContract: transitioncost.FrozenContract{
			SourceState: source, TargetState: target,
			TargetIdleBaseline: transitioncost.IdleBaseline{
				EvidenceReference: "evidence/idle", State: target, MeanWallWatts: 10,
			},
		},
		Policy: policyWire{
			MaximumBoundaryDistanceMilliseconds: 100, MinimumCoverage: 0.9,
			MaximumSampleGapMilliseconds: 1000, MaximumBoundaryUncertaintyMilliseconds: 50,
			MaximumReadyObservationDelayMilliseconds: 3000,
		},
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
			ServingService: "serving", ServingContainer: "runtime",
			PollIntervalMilliseconds: 50, ObservationTimeoutMilliseconds: 4000,
			BoundaryMappingUncertaintyMilliseconds: 50,
		},
		RawLocation: evidencestore.ObjectLocation{Store: "evidence", Key: "transition/raw.json"},
	}
}
