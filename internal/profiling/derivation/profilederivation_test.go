package profilederivation

import (
	"math"
	"testing"
	"time"
)

const otherPolicy = "other"

func TestMeasurementIntegratesWindowWithoutScientificVerdicts(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	samples := []Sample{
		{start.Add(time.Second), 10},
		{start.Add(5 * time.Second), 10},
		{start.Add(9 * time.Second), 10},
	}
	joules, err := measurePower(start, start.Add(10*time.Second), samples)
	if err != nil || math.Abs(joules-100) > 1e-9 {
		t.Fatalf("energy = %v, error = %v", joules, err)
	}
	baseline, err := DeriveIdleBaseline(
		[]IdleTrial{
			{StateIdentity: "state", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "idle-protocol", TrialID: "idle-1", RawEvidence: rawEvidence("1"), ModelLoadedIdleMeanWallWatts: 5},
			{StateIdentity: "state", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "idle-protocol", TrialID: "idle-2", RawEvidence: rawEvidence("2"), ModelLoadedIdleMeanWallWatts: 5.1},
			{StateIdentity: "state", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "idle-protocol", TrialID: "idle-3", RawEvidence: rawEvidence("3"), ModelLoadedIdleMeanWallWatts: 4.9},
		},
		idlePointPolicy(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.DegreesOfFreedom != 2 ||
		math.Abs(baseline.MeanWallWattsVariance-(.01/3)) > 1e-12 ||
		math.Abs(baseline.ConfidenceHalfWidth-studentTCritical(0.95, 2)*math.Sqrt(.01/3)) > 1e-12 {
		t.Fatalf("idle sufficient statistics are incomplete: %#v", baseline)
	}
	mixedIdle := append([]IdleTrial(nil), baseline.Trials...)
	mixedIdle[2].EnvironmentIdentity = otherPolicy
	if _, err := DeriveIdleBaseline(mixedIdle, idlePointPolicy()); err != nil {
		t.Fatalf("environment observations blocked idle calculation: %v", err)
	}
	duplicateEvidence := append([]IdleTrial(nil), baseline.Trials...)
	duplicateEvidence[2].RawEvidence = duplicateEvidence[1].RawEvidence
	if _, err := DeriveIdleBaseline(duplicateEvidence, idlePointPolicy()); err == nil {
		t.Fatal("expected duplicate idle raw evidence rejection")
	}
}

func TestRestoreIdleBaselineValidatesStoredAggregateAndOwnsTrials(t *testing.T) {
	baseline, err := DeriveIdleBaseline([]IdleTrial{
		{StateIdentity: "state", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "idle", TrialID: "idle-1", RawEvidence: rawEvidence("1"), ModelLoadedIdleMeanWallWatts: 5},
		{StateIdentity: "state", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "idle", TrialID: "idle-2", RawEvidence: rawEvidence("2"), ModelLoadedIdleMeanWallWatts: 5.1},
		{StateIdentity: "state", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "idle", TrialID: "idle-3", RawEvidence: rawEvidence("3"), ModelLoadedIdleMeanWallWatts: 4.9},
	}, idlePointPolicy())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreIdleBaseline(baseline)
	if err != nil {
		t.Fatal(err)
	}
	baseline.Trials[0].TrialID = "caller-mutated"
	if restored.Trials[0].TrialID == "caller-mutated" {
		t.Fatal("RestoreIdleBaseline retained the caller's trial slice")
	}
	restored.ModelLoadedIdleMeanWallWatts = -1
	if _, err := RestoreIdleBaseline(restored); err == nil {
		t.Fatal("RestoreIdleBaseline accepted an invalid stored summary")
	}
}

func idlePointPolicy() PointPolicy {
	return PointPolicy{Version: "idle-v1", MinimumTrials: 3, ConfidenceLevel: 0.95}
}

func rawEvidence(key string) RawEvidenceIdentity {
	return RawEvidenceIdentity{
		Store: "chill-evidence", Key: "runs/" + key + "/raw.json",
		Size: 1,
	}
}
