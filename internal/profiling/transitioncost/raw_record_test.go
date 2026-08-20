package transitioncost_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

func TestRawRecordRoundTripRetainsAcceptedCommonWindow(t *testing.T) {
	frozen, raw, policy := validInputs()
	payload, encoded, err := transitioncost.EncodeRawRecord(frozen, raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := transitioncost.DecodeRawRecord(payload)
	if err != nil {
		t.Fatal(err)
	}

	encodedAccepted, ok := encoded.AcceptedObservation()
	if !ok {
		t.Fatalf("encoded result = %s/%s", encoded.Verdict(), encoded.Reason())
	}
	decodedAccepted, ok := decoded.AcceptedObservation()
	if !ok {
		t.Fatalf("decoded result = %s/%s", decoded.Verdict(), decoded.Reason())
	}
	if decodedAccepted.TauMilliseconds() != encodedAccepted.TauMilliseconds() ||
		decodedAccepted.TransitionWallJoules() != encodedAccepted.TransitionWallJoules() ||
		decodedAccepted.EtaJoules() != encodedAccepted.EtaJoules() {
		t.Fatalf(
			"decoded common-window values = tau %v ms, wall %v J, eta %v J; want %v, %v, %v",
			decodedAccepted.TauMilliseconds(), decodedAccepted.TransitionWallJoules(), decodedAccepted.EtaJoules(),
			encodedAccepted.TauMilliseconds(), encodedAccepted.TransitionWallJoules(), encodedAccepted.EtaJoules(),
		)
	}
	if decoded.RawObservation().PowerSamples[0].RequestLatencySeconds !=
		raw.PowerSamples[0].RequestLatencySeconds {
		t.Fatal("request latency changed across the raw-record boundary")
	}
}

func TestRawRecordRoundTripRetainsTypedNonAcceptedOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*transitioncost.RawObservation)
		verdict transitioncost.Verdict
		reason  transitioncost.Reason
	}{
		{
			name: "invalid observation window",
			mutate: func(raw *transitioncost.RawObservation) {
				raw.ObservationEndedAt = raw.ObservationStartedAt
			},
			verdict: transitioncost.VerdictInvalid,
			reason:  transitioncost.ReasonObservationMalformed,
		},
		{
			name: "failed actuation",
			mutate: func(raw *transitioncost.RawObservation) {
				raw.TransitionStart = nil
				raw.TargetReadBack = nil
				raw.Readiness = nil
				raw.PostTransitionProcess = nil
				raw.ActuationFailure = &transitioncost.Failure{
					ObservedAt: testTime(2), Message: "apply failed",
				}
			},
			verdict: transitioncost.VerdictFailed,
			reason:  transitioncost.ReasonActuationFailed,
		},
		{
			name: "insufficient power window",
			mutate: func(raw *transitioncost.RawObservation) {
				raw.PowerSamples = nil
			},
			verdict: transitioncost.VerdictInsufficient,
			reason:  transitioncost.ReasonPowerWindowInsufficient,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frozen, raw, policy := validInputs()
			test.mutate(&raw)
			payload, encoded, err := transitioncost.EncodeRawRecord(frozen, raw, policy)
			if err != nil {
				t.Fatal(err)
			}
			assertOutcome(t, encoded, test.verdict, test.reason)
			decoded, err := transitioncost.DecodeRawRecord(payload)
			if err != nil {
				t.Fatal(err)
			}
			assertOutcome(t, decoded, test.verdict, test.reason)
		})
	}
}

func TestDecodeRawRecordRejectsUnknownTrailingAndRetiredContracts(t *testing.T) {
	frozen, raw, policy := validInputs()
	payload, _, err := transitioncost.EncodeRawRecord(frozen, raw, policy)
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
			"chill.dacs.io/transition-cost-raw-observation.v1alpha0", 1,
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

func TestRawRecordDurationFieldsDeclareTheirWireUnits(t *testing.T) {
	frozen, raw, policy := validInputs()
	payload, _, err := transitioncost.EncodeRawRecord(frozen, raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(payload, &record); err != nil {
		t.Fatal(err)
	}
	if len(record) != 4 {
		t.Fatalf("top-level raw record fields = %v, want exactly four", record)
	}
	wirePolicy := record["policy"].(map[string]any)
	wantDurations := map[string]time.Duration{
		"maximumBoundaryDistanceNanoseconds":      policy.MaximumBoundaryDistance,
		"maximumSampleGapNanoseconds":             policy.MaximumSampleGap,
		"maximumBoundaryUncertaintyNanoseconds":   policy.MaximumBoundaryUncertainty,
		"maximumReadyObservationDelayNanoseconds": policy.MaximumReadyObservationDelay,
	}
	for field, duration := range wantDurations {
		if got := wirePolicy[field]; got != float64(duration) {
			t.Fatalf("policy.%s = %v, want %d", field, got, duration)
		}
	}
	wireRaw := record["rawObservation"].(map[string]any)
	if got := wireRaw["boundaryMappingUncertaintyNanoseconds"]; got != float64(raw.BoundaryMappingUncertainty) {
		t.Fatalf("raw boundary uncertainty = %v, want %d", got, raw.BoundaryMappingUncertainty)
	}
	wireSamples := wireRaw["powerSamples"].([]any)
	wireSample := wireSamples[0].(map[string]any)
	if got := wireSample["requestLatencySeconds"]; math.Abs(got.(float64)-raw.PowerSamples[0].RequestLatencySeconds) > 1e-12 {
		t.Fatalf("sample request latency = %v, want %v", got, raw.PowerSamples[0].RequestLatencySeconds)
	}
}
