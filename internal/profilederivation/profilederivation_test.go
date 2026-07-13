package profilederivation

import (
	"math"
	"testing"
	"time"
)

const otherPolicy = "other"

func TestAdmissionIntegratesExactWindowAndFailsClosed(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	latencies := make([]float64, 100)
	for index := range latencies {
		latencies[index] = 1
	}
	trial := Trial{
		StateIdentity:         "state",
		TrialID:               "active-1",
		Batch:                 1,
		WindowStart:           start,
		WindowEnd:             start.Add(10 * time.Second),
		CompletedCalls:        100,
		CompletedItems:        100,
		LatencyMilliseconds:   latencies,
		SteadyWindowVerified:  true,
		ThermalStateKnown:     true,
		CoResidentWindowKnown: true,
		Samples: []Sample{
			{start.Add(time.Second), 10},
			{start.Add(5 * time.Second), 10},
			{start.Add(9 * time.Second), 10},
		},
	}
	policy := AdmissionPolicy{
		Version:                        "v1",
		MinimumSamples:                 3,
		MaximumGapSeconds:              5,
		MaximumBoundaryDistanceSeconds: 1,
		MinimumCoverage:                .8,
		AllowReceiptTimestamps:         true,
		RejectUnexpectedCoResidents:    true,
	}
	result := Admit(trial, policy)
	if result.Verdict != VerdictAccepted || result.TotalWallJoules == nil ||
		math.Abs(*result.TotalWallJoules-100) > 1e-9 {
		t.Fatalf("unexpected admission: %+v", result)
	}
	baseline, err := DeriveIdleBaseline(
		[]IdleTrial{
			{StateIdentity: "state", TrialID: "idle-1", AdmissionPolicyVersion: "v1", MeanWallWatts: 5},
			{StateIdentity: "state", TrialID: "idle-2", AdmissionPolicyVersion: "v1", MeanWallWatts: 5.1},
			{StateIdentity: "state", TrialID: "idle-3", AdmissionPolicyVersion: "v1", MeanWallWatts: 4.9},
		},
		pointPolicy("idle-v1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	mixedIdle := append([]IdleTrial(nil), baseline.Trials...)
	mixedIdle[2].AdmissionPolicyVersion = otherPolicy
	if _, err := DeriveIdleBaseline(mixedIdle, pointPolicy("idle-v1")); err == nil {
		t.Fatal("expected mixed idle policy rejection")
	}
	admitted, err := IncrementalTrial(trial, result, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(admitted.IncrementalJoulesPerItem-.5) > 1e-9 {
		t.Fatal(admitted.IncrementalJoulesPerItem)
	}
	invalidBaseline := baseline
	invalidBaseline.ConfidenceHalfWidth = math.NaN()
	if _, err := IncrementalTrial(trial, result, invalidBaseline); err == nil {
		t.Fatal("expected invalid baseline uncertainty rejection")
	}
	trial.UnexpectedCoResidents = []string{"foreign"}
	result = Admit(trial, policy)
	if result.Verdict != VerdictRejected {
		t.Fatalf("expected rejection: %+v", result)
	}
	trial.UnexpectedCoResidents = nil
	trial.Samples = trial.Samples[:1]
	result = Admit(trial, policy)
	if result.Verdict != VerdictInsufficient {
		t.Fatalf("expected insufficient: %+v", result)
	}
	trial.Samples = []Sample{
		{start.Add(time.Second), 10},
		{start.Add(5 * time.Second), 10},
		{start.Add(9 * time.Second), 10},
	}
	trial.CoResidentWindowKnown = false
	result = Admit(trial, policy)
	if result.Verdict != VerdictInsufficient {
		t.Fatal("unknown co-resident window must not be accepted")
	}
}

func TestAggregateRequiresIndependentTrials(t *testing.T) {
	trials := []AdmittedTrial{
		{
			StateIdentity:            "s",
			TrialID:                  "1",
			AdmissionPolicyVersion:   "a",
			EstimatorIdentity:        "e",
			Batch:                    1,
			IncrementalJoulesPerItem: 1,
			LatencyP99Milliseconds:   2,
		},
		{
			StateIdentity:            "s",
			TrialID:                  "2",
			AdmissionPolicyVersion:   "a",
			EstimatorIdentity:        "e",
			Batch:                    1,
			IncrementalJoulesPerItem: 1.1,
			LatencyP99Milliseconds:   3,
		},
		{
			StateIdentity:            "s",
			TrialID:                  "3",
			AdmissionPolicyVersion:   "a",
			EstimatorIdentity:        "e",
			Batch:                    1,
			IncrementalJoulesPerItem: .9,
			LatencyP99Milliseconds:   2.5,
		},
	}
	point, err := Aggregate(trials, pointPolicy("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if point.MeanIncrementalJoulesPerItem != 1 || point.ConfidenceHalfWidth <= 0 || point.LatencyP99Milliseconds != 3 {
		t.Fatalf("unexpected point: %+v", point)
	}
	if _, err := Aggregate(trials[:1], pointPolicy("v1")); err == nil {
		t.Fatal("expected repetition rejection")
	}
	duplicate := append([]AdmittedTrial(nil), trials...)
	duplicate[2].TrialID = "2"
	if _, err := Aggregate(duplicate, pointPolicy("v1")); err == nil {
		t.Fatal("expected duplicate trial rejection")
	}
	mixed := append([]AdmittedTrial(nil), trials...)
	mixed[2].AdmissionPolicyVersion = otherPolicy
	if _, err := Aggregate(mixed, pointPolicy("v1")); err == nil {
		t.Fatal("expected mixed admission policy rejection")
	}
	uncertain := append([]AdmittedTrial(nil), trials...)
	for index := range uncertain {
		uncertain[index].BaselineUncertaintyJoulesPerItem = .5
	}
	uncertainPoint, err := Aggregate(uncertain, pointPolicy("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if uncertainPoint.ConfidenceHalfWidth < .5 {
		t.Fatal("baseline uncertainty was discarded")
	}
	uncertain[0].BaselineUncertaintyJoulesPerItem = math.NaN()
	if _, err := Aggregate(uncertain, pointPolicy("v1")); err == nil {
		t.Fatal("expected invalid uncertainty contribution rejection")
	}
}

func TestBSatGoldenOutcomes(t *testing.T) {
	p := func(batch int, mean, half float64) Point {
		return Point{
			StateIdentity:                "s",
			EstimatorIdentity:            "e",
			PointPolicyVersion:           "p",
			ConfidenceMethod:             "student-t",
			ConfidenceLevel:              .95,
			Batch:                        batch,
			MeanIncrementalJoulesPerItem: mean,
			ConfidenceHalfWidth:          half,
		}
	}
	policy := BSatPolicy{Version: "v1", RelativeEquivalenceTolerance: .02}
	tests := []struct {
		name   string
		points []Point
		want   BSatStatus
		batch  int
	}{
		{"accepted", []Point{p(1, 1, .001), p(2, .8, .001), p(3, .795, .001), p(4, .8, .001)}, BSatAccepted, 2},
		{"candidate uncertainty", []Point{p(1, 1, .001), p(2, .8, .05), p(3, .795, .05)}, BSatCandidateOnly, 2},
		{"censored", []Point{p(1, 1, .001), p(2, .8, .001), p(3, .6, .001)}, BSatCensored, 0},
		{"ambiguous later drop", []Point{p(1, 1, .001), p(2, .99, .001), p(3, .7, .001)}, BSatAmbiguous, 1},
		{"missing adjacent", []Point{p(1, 1, .001), p(2, .8, .001), p(4, .79, .001)}, BSatUnavailable, 0},
		{"gap after plateau", []Point{p(1, 1, .001), p(2, .99, .001), p(4, 1, .001)}, BSatUnavailable, 0},
		{"missing lower domain", []Point{p(2, 1, .001), p(3, .99, .001)}, BSatUnavailable, 0},
		{"mixed point policy", func() []Point {
			values := []Point{p(1, 1, .001), p(2, .99, .001)}
			values[1].PointPolicyVersion = otherPolicy
			return values
		}(), BSatUnavailable, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := DeriveBSat(test.points, policy)
			if got.Status != test.want {
				t.Fatalf("got %+v", got)
			}
			if test.batch > 0 && (got.Batch == nil || *got.Batch != test.batch) {
				t.Fatalf("batch %+v", got.Batch)
			}
		})
	}
}

func pointPolicy(version string) PointPolicy {
	return PointPolicy{Version: version, MinimumTrials: 3, ConfidenceMethod: "student-t", ConfidenceLevel: .95}
}
