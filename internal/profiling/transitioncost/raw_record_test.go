package transitioncost_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

func TestRawRecordRoundTripRetainsOnlyObservedFacts(t *testing.T) {
	base := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	raw := transitioncost.RawObservation{
		ObservationStartedAt: base,
		ObservationEndedAt:   base.Add(5 * time.Second),
		SourceReadBack: transitioncost.StateReadBack{
			Fact: transitioncost.MappedFact{OccurredAt: base, MappedAt: base.Add(10 * time.Millisecond)},
			State: transitioncost.StateIdentity{
				DeviceType: "jetson-orin", Model: "mobilenet-v2", PowerMode: "MODE_15W",
			},
		},
		PowerSamples: []transitioncost.PowerSample{{
			ObservedAt: base.Add(time.Second), RequestLatencySeconds: 0.123456789, Watts: 20,
		}},
		MarkerBrackets: transitioncost.TransitionMarkerBrackets{
			ApplyStartedAt: transitioncost.ReceiptTimeBracket{
				LastAbsentReceivedAt: base.Add(1900 * time.Millisecond),
			},
		},
	}

	payload, err := transitioncost.EncodeRawRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := transitioncost.DecodeRawRecord(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.ObservationStartedAt.Equal(raw.ObservationStartedAt) ||
		decoded.SourceReadBack.State != raw.SourceReadBack.State ||
		decoded.MarkerBrackets != raw.MarkerBrackets ||
		len(decoded.PowerSamples) != 1 ||
		math.Abs(decoded.PowerSamples[0].RequestLatencySeconds-raw.PowerSamples[0].RequestLatencySeconds) > 1e-12 {
		t.Fatalf("decoded raw observation changed: %#v", decoded)
	}

	var record map[string]any
	if err := json.Unmarshal(payload, &record); err != nil {
		t.Fatal(err)
	}
	if len(record) != 2 || record["schemaVersion"] != transitioncost.RawRecordSchemaVersion {
		t.Fatalf("top-level raw record = %v, want schemaVersion and rawObservation only", record)
	}
	wireRaw, ok := record["rawObservation"].(map[string]any)
	if !ok {
		t.Fatalf("rawObservation = %#v", record["rawObservation"])
	}
	if _, found := wireRaw["boundaryMappingUncertaintyNanoseconds"]; found {
		t.Fatal("raw observation retains configured boundary uncertainty")
	}
	if wireRaw["markerBrackets"] == nil {
		t.Fatal("raw observation does not retain marker receipt-time brackets")
	}
}

func TestDecodeRawRecordRejectsUnknownTrailingAndRetiredContracts(t *testing.T) {
	payload, err := transitioncost.EncodeRawRecord(transitioncost.RawObservation{})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"unknown field": []byte(strings.Replace(
			string(payload), `{"schemaVersion"`, `{"unknown":true,"schemaVersion"`, 1,
		)),
		"trailing value": append(append([]byte(nil), payload...), []byte(` {}`)...),
		"retired schema": []byte(strings.Replace(
			string(payload), transitioncost.RawRecordSchemaVersion,
			"chill.dacs.io/transition-cost-raw-observation.v1alpha2", 1,
		)),
	}
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := transitioncost.DecodeRawRecord(candidate); err == nil {
				t.Fatal("DecodeRawRecord() unexpectedly accepted the candidate")
			}
		})
	}
}
