package operatorconsole

import (
	"fmt"
	"sort"
	"testing"

	"github.com/lab-paper-code/chill/internal/profilederivation"
)

// The Console cannot depend on the pure derivation package in production, but
// both surfaces publish a nearest-rank p99. Keep the two public results pinned
// to the same definition without adding a cross-layer runtime dependency.
func TestLatencyP99MatchesProfileDerivationNearestRank(t *testing.T) {
	t.Parallel()

	tests := map[string][]float64{
		"single":          {7},
		"two":             {2, 1},
		"one-hundred":     descendingLatencies(100),
		"one-hundred-one": descendingLatencies(101),
	}
	for name, latencies := range tests {
		t.Run(name, func(t *testing.T) {
			ordered := append([]float64(nil), latencies...)
			sort.Float64s(ordered)
			consoleP99 := nearestRank(ordered, 0.99)
			baseline := zeroIdleBaseline(t)

			trial, err := profilederivation.IncrementalWorkTrial(
				profilederivation.Trial{
					StateIdentity: "state", EnvironmentIdentity: "environment",
					MeasurementProtocolIdentity: "active-protocol", TrialID: "trial",
					Batch: 1, CompletedItems: 1, LatencyMilliseconds: latencies,
				},
				profilederivation.AdmissionResult{
					Verdict: profilederivation.VerdictAccepted, PolicyVersion: "active-admission",
					StateIdentity: "state", EnvironmentIdentity: "environment",
					MeasurementProtocolIdentity: "active-protocol", TrialID: "trial",
					WindowSeconds: 1, CompletedItems: 1, TotalWallJoules: float64Pointer(1),
				},
				baseline,
			)
			if err != nil {
				t.Fatalf("derive active trial: %v", err)
			}
			if trial.LatencyP99Milliseconds != consoleP99 {
				t.Fatalf("p99 mismatch: derivation=%v console=%v", trial.LatencyP99Milliseconds, consoleP99)
			}
		})
	}
}

func zeroIdleBaseline(t *testing.T) profilederivation.IdleBaseline {
	t.Helper()
	trials := make([]profilederivation.IdleTrial, 3)
	for index := range trials {
		trials[index] = profilederivation.IdleTrial{
			StateIdentity: "state", EnvironmentIdentity: "environment",
			MeasurementProtocolIdentity: "idle-protocol",
			TrialID:                     fmt.Sprintf("idle-%d", index),
			RawEvidence: profilederivation.RawEvidenceIdentity{
				Store: "store", Key: fmt.Sprintf("idle/%d", index),
				Size: 1,
			},
			AdmissionPolicyVersion: "idle-admission",
		}
	}
	baseline, err := profilederivation.DeriveIdleBaseline(
		trials,
		profilederivation.PointPolicy{Version: "idle-point", MinimumTrials: 3, ConfidenceLevel: 0.95},
	)
	if err != nil {
		t.Fatal(err)
	}
	return baseline
}

func descendingLatencies(count int) []float64 {
	values := make([]float64, count)
	for index := range values {
		values[index] = float64(count - index)
	}
	return values
}

func float64Pointer(value float64) *float64 {
	return &value
}
