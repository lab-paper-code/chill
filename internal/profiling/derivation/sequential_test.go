package profilederivation

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDecodeSequentialResultValidatesRetainedIdleBaselines(t *testing.T) {
	baseline := sequentialResultBaseline(t)
	valid := SequentialResult{
		IdleBaselinesByState: map[string]IdleBaseline{baseline.StateIdentity: baseline},
	}
	payload := marshalSequentialResult(t, valid)
	decoded, err := DecodeSequentialResult(payload)
	if err != nil {
		t.Fatalf("decode sequential result: %v", err)
	}
	if got := decoded.IdleBaselinesByState[baseline.StateIdentity]; got.ModelLoadedIdleMeanWallWatts != baseline.ModelLoadedIdleMeanWallWatts {
		t.Fatalf("decoded baseline = %+v, want %+v", got, baseline)
	}

	mismatchedKey := valid
	mismatchedKey.IdleBaselinesByState = map[string]IdleBaseline{"other-state": baseline}
	invalidBaseline := baseline
	invalidBaseline.Trials = nil
	tests := map[string][]byte{
		"unknown field":  bytes.Replace(payload, []byte(`{"status":`), []byte(`{"unknown":true,"status":`), 1),
		"trailing value": append(bytes.Clone(payload), []byte(` {}`)...),
		"mismatched key": marshalSequentialResult(t, mismatchedKey),
		"invalid baseline": marshalSequentialResult(t, SequentialResult{
			IdleBaselinesByState: map[string]IdleBaseline{invalidBaseline.StateIdentity: invalidBaseline},
		}),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeSequentialResult(payload); err == nil {
				t.Fatal("invalid sequential result was accepted")
			}
		})
	}
}

func sequentialResultBaseline(t *testing.T) IdleBaseline {
	t.Helper()
	trials := make([]IdleTrial, 3)
	for index := range trials {
		trials[index] = IdleTrial{
			StateIdentity: "state", EnvironmentIdentity: "environment",
			MeasurementProtocolIdentity: "idle-protocol", TrialID: "idle-" + string(rune('1'+index)),
			RawEvidence: RawEvidenceIdentity{
				Store: "evidence", Key: "idle/" + string(rune('1'+index)), Size: int64(index + 1),
			},
			AdmissionPolicyVersion: "idle-admission-v1", ModelLoadedIdleMeanWallWatts: 8 + float64(index),
		}
	}
	baseline, err := DeriveIdleBaseline(trials, PointPolicy{
		Version: "idle-point-v1", MinimumTrials: 3, ConfidenceLevel: .95,
	})
	if err != nil {
		t.Fatal(err)
	}
	return baseline
}

func marshalSequentialResult(t *testing.T, result SequentialResult) []byte {
	t.Helper()
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
