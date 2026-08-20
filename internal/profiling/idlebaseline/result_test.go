package idlebaseline

import (
	"bytes"
	"fmt"
	"testing"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

func TestResultRoundTripRetainsCompleteAggregateAuthority(t *testing.T) {
	baseline := resultBaseline(t)
	accepted := resultEvidence(len(baseline.Trials))
	result, err := NewResult(resultState(), baseline, accepted)
	if err != nil {
		t.Fatal(err)
	}
	accepted["trial-0"] = FinalEvidenceReference{
		Store: "evidence", Key: "caller-mutated", SchemaVersion: "final.v1", Size: 20,
	}
	baseline.Trials[0].TrialID = "caller-mutated"
	payload, err := result.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeResult(payload)
	if err != nil {
		t.Fatal(err)
	}
	if restored.SchemaVersion() != ResultSchemaVersion || restored.MeasurementState() != resultState() ||
		restored.IdleBaseline().ModelLoadedIdleMeanWallWatts != 11 ||
		restored.AcceptedFinalEvidence()["trial-0"].Key != "runs/0/final.json" ||
		restored.IdleBaseline().Trials[0].TrialID != "trial-0" {
		t.Fatalf("restored result drifted: %#v", restored)
	}
	gotEvidence := restored.AcceptedFinalEvidence()
	gotEvidence["trial-0"] = FinalEvidenceReference{
		Store: "evidence", Key: "returned-mutated", SchemaVersion: "final.v1", Size: 20,
	}
	gotBaseline := restored.IdleBaseline()
	gotBaseline.Trials[0].TrialID = "returned-mutated"
	if restored.AcceptedFinalEvidence()["trial-0"].Key != "runs/0/final.json" ||
		restored.IdleBaseline().Trials[0].TrialID != "trial-0" {
		t.Fatal("Result accessors leaked owned slices")
	}
}

func TestDecodeResultRejectsRetiredOrInvalidStoredResult(t *testing.T) {
	baseline := resultBaseline(t)
	result, err := NewResult(resultState(), baseline, resultEvidence(len(baseline.Trials)))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := result.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"retired schema": bytes.Replace(payload, []byte("result.v1alpha1"), []byte("result.v1alpha0"), 1),
		"unknown field":  append(bytes.TrimSuffix(payload, []byte("}")), []byte(`,"extra":true}`)...),
		"invalid mean":   bytes.Replace(payload, []byte(`"modelLoadedIdleMeanWallWatts":11`), []byte(`"modelLoadedIdleMeanWallWatts":-1`), 1),
		"mismatched trial ID": bytes.Replace(
			payload, []byte(`"trial-2":`), []byte(`"other-trial":`), 1,
		),
		"missing evidence": bytes.Replace(
			payload, []byte(`"acceptedFinalEvidence":{`), []byte(`"acceptedFinalEvidence":null,"removed":{`), 1,
		),
	}
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeResult(candidate); err == nil {
				t.Fatal("DecodeResult accepted invalid stored evidence")
			}
		})
	}
}

func TestNewResultRequiresOneFinalReferencePerDerivedTrial(t *testing.T) {
	baseline := resultBaseline(t)
	if _, err := NewResult(resultState(), baseline, resultEvidence(2)); err == nil {
		t.Fatal("NewResult accepted incomplete final evidence")
	}
	wrong := resultEvidence(3)
	wrong["other-trial"] = wrong["trial-2"]
	delete(wrong, "trial-2")
	if _, err := NewResult(resultState(), baseline, wrong); err == nil {
		t.Fatal("NewResult accepted a mismatched TrialID key set")
	}
}

func resultState() MeasurementState {
	return MeasurementState{
		TargetNode: "node-a", ModelSpec: "model-a", DeviceClass: "class-a",
		ExecutionPath: "runtime-a", PowerMode: "mode-a",
	}
}

func resultBaseline(t *testing.T) profilederivation.IdleBaseline {
	t.Helper()
	trials := make([]profilederivation.IdleTrial, 3)
	for index := range trials {
		trials[index] = profilederivation.IdleTrial{
			StateIdentity: "state", EnvironmentIdentity: "environment",
			MeasurementProtocolIdentity: "protocol", TrialID: fmt.Sprintf("trial-%d", index),
			RawEvidence: profilederivation.RawEvidenceIdentity{
				Store: "evidence", Key: fmt.Sprintf("runs/%d/raw.json", index), Size: 10,
			},
			AdmissionPolicyVersion: "idle-admission-v1", ModelLoadedIdleMeanWallWatts: float64(10 + index),
		}
	}
	baseline, err := profilederivation.DeriveIdleBaseline(trials, profilederivation.PointPolicy{
		Version: "idle-point-v1", MinimumTrials: 3, ConfidenceLevel: .95,
	})
	if err != nil {
		t.Fatal(err)
	}
	return baseline
}

func resultEvidence(count int) AcceptedFinalEvidence {
	references := make(AcceptedFinalEvidence, count)
	for index := 0; index < count; index++ {
		references[fmt.Sprintf("trial-%d", index)] = FinalEvidenceReference{
			Store: "evidence", Key: fmt.Sprintf("runs/%d/final.json", index),
			SchemaVersion: "final.v1", Size: 20,
		}
	}
	return references
}
