package operatorconsole

import (
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

			trial, err := profilederivation.IncrementalTrial(
				profilederivation.Trial{
					StateIdentity: "state", EnvironmentIdentity: "environment",
					MeasurementProtocolIdentity: "active-protocol", TrialID: "trial",
					Batch: 1, CompletedItems: 1, LatencyMilliseconds: latencies,
				},
				profilederivation.AdmissionResult{
					Verdict: profilederivation.VerdictAccepted, PolicyVersion: "active-admission",
					StateIdentity: "state", EnvironmentIdentity: "environment",
					MeasurementProtocolIdentity: "active-protocol", TrialID: "trial",
					WindowSeconds: 1, TotalWallJoules: float64Pointer(1),
				},
				profilederivation.IdleBaseline{
					StateIdentity: "state", EnvironmentIdentity: "environment",
					MeasurementProtocolIdentity: "idle-protocol", PolicyVersion: "idle-point",
					AdmissionPolicyVersion: "idle-admission", Trials: make([]profilederivation.IdleTrial, 3),
				},
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
