package profilederivation

import (
	"math"
	"strings"
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
		StateIdentity:               "state",
		EnvironmentIdentity:         "environment",
		MeasurementProtocolIdentity: "active-protocol",
		TrialID:                     "active-1",
		Batch:                       1,
		WindowStart:                 start,
		WindowEnd:                   start.Add(10 * time.Second),
		CompletedCalls:              100,
		CompletedItems:              100,
		LatencyMilliseconds:         latencies,
		SteadyWindowVerified:        true,
		ThermalStateKnown:           true,
		WorkloadWindowKnown:         true,
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
		RejectConcurrentCHILLWorkloads: true,
		RejectExternalWorkloadChanges:  true,
	}
	result := Admit(trial, policy)
	if result.Verdict != VerdictAccepted || result.TotalWallJoules == nil ||
		math.Abs(*result.TotalWallJoules-100) > 1e-9 {
		t.Fatalf("unexpected admission: %+v", result)
	}
	baseline, err := DeriveIdleBaseline(
		[]IdleTrial{
			{StateIdentity: "state", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "idle-protocol", TrialID: "idle-1", RawEvidence: rawEvidence("1"), AdmissionPolicyVersion: "v1", MeanWallWatts: 5},
			{StateIdentity: "state", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "idle-protocol", TrialID: "idle-2", RawEvidence: rawEvidence("2"), AdmissionPolicyVersion: "v1", MeanWallWatts: 5.1},
			{StateIdentity: "state", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "idle-protocol", TrialID: "idle-3", RawEvidence: rawEvidence("3"), AdmissionPolicyVersion: "v1", MeanWallWatts: 4.9},
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
	mixedIdle = append([]IdleTrial(nil), baseline.Trials...)
	mixedIdle[2].MeasurementProtocolIdentity = otherPolicy
	if _, err := DeriveIdleBaseline(mixedIdle, pointPolicy("idle-v1")); err == nil {
		t.Fatal("expected mixed idle protocol rejection")
	}
	duplicateEvidence := append([]IdleTrial(nil), baseline.Trials...)
	duplicateEvidence[2].RawEvidence = duplicateEvidence[1].RawEvidence
	if _, err := DeriveIdleBaseline(duplicateEvidence, pointPolicy("idle-v1")); err == nil {
		t.Fatal("expected duplicate idle raw evidence rejection")
	}
	admitted, err := IncrementalTrial(trial, result, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(admitted.IncrementalJoulesPerItem-.5) > 1e-9 {
		t.Fatal(admitted.IncrementalJoulesPerItem)
	}
	if admitted.MeasurementProtocolIdentity != "active-protocol" ||
		!strings.Contains(admitted.EstimatorIdentity, "idleProtocol=idle-protocol") {
		t.Fatalf("estimator identity lost active/idle protocols: %#v", admitted)
	}
	mismatchedEnvironment := baseline
	mismatchedEnvironment.EnvironmentIdentity = "other-environment"
	if _, err := IncrementalTrial(trial, result, mismatchedEnvironment); err == nil {
		t.Fatal("expected active/idle environment mismatch rejection")
	}
	mismatchedAdmission := result
	mismatchedAdmission.MeasurementProtocolIdentity = "other-active-protocol"
	if _, err := IncrementalTrial(trial, mismatchedAdmission, baseline); err == nil {
		t.Fatal("expected active admission protocol mismatch rejection")
	}
	invalidBaseline := baseline
	invalidBaseline.ConfidenceHalfWidth = math.NaN()
	if _, err := IncrementalTrial(trial, result, invalidBaseline); err == nil {
		t.Fatal("expected invalid baseline uncertainty rejection")
	}
	trial.ConcurrentCHILLWorkloads = []string{"other-run"}
	result = Admit(trial, policy)
	if result.Verdict != VerdictRejected {
		t.Fatalf("expected rejection: %+v", result)
	}
	trial.ConcurrentCHILLWorkloads = nil
	trial.ExternalWorkloadChanges = []string{"Entered:external"}
	result = Admit(trial, policy)
	if result.Verdict != VerdictRejected {
		t.Fatalf("expected external-change rejection: %+v", result)
	}
	trial.ExternalWorkloadChanges = nil
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
	trial.WorkloadWindowKnown = false
	result = Admit(trial, policy)
	if result.Verdict != VerdictInsufficient {
		t.Fatal("unknown workload window must not be accepted")
	}
}

func TestIdleAdmissionUsesSharedWindowQualityAndIdleCompleteness(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	observation := IdleObservation{
		StateIdentity: "state", EnvironmentIdentity: "environment",
		MeasurementProtocolIdentity: "idle-protocol", TrialID: "idle-1",
		RawEvidence: rawEvidence("idle-1"), WindowStart: start, WindowEnd: start.Add(10 * time.Second),
		ModelLoadedBeforeSettling: true, SettlingWindowVerified: true, ModelResidentAtEnd: true,
		ThermalStateKnown: true, WorkloadWindowKnown: true,
		Samples: []Sample{{start.Add(time.Second), 5}, {start.Add(5 * time.Second), 5}, {start.Add(9 * time.Second), 5}},
	}
	policy := AdmissionPolicy{
		Version: "idle-v1", MinimumSamples: 3, MaximumGapSeconds: 5,
		MaximumBoundaryDistanceSeconds: 1, MinimumCoverage: .8, AllowReceiptTimestamps: true,
		RejectConcurrentCHILLWorkloads: true, RejectExternalWorkloadChanges: true,
	}
	result := AdmitIdle(observation, policy)
	if result.Verdict != VerdictAccepted || result.TotalWallJoules == nil || result.MeanWallWatts == nil ||
		math.Abs(*result.TotalWallJoules-50) > 1e-9 || math.Abs(*result.MeanWallWatts-5) > 1e-9 ||
		result.RawEvidence != observation.RawEvidence {
		t.Fatalf("unexpected idle admission: %+v", result)
	}

	for _, test := range []struct {
		name string
		want Verdict
		edit func(*IdleObservation)
	}{
		{name: "inference", want: VerdictRejected, edit: func(candidate *IdleObservation) { candidate.InferenceCountAfterModelLoad = 1 }},
		{name: "settling", want: VerdictRejected, edit: func(candidate *IdleObservation) { candidate.SettlingWindowVerified = false }},
		{name: "provenance", want: VerdictRejected, edit: func(candidate *IdleObservation) { candidate.RawEvidence.Digest = "bad" }},
		{name: "coverage", want: VerdictInsufficient, edit: func(candidate *IdleObservation) { candidate.Samples = candidate.Samples[:1] }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := observation
			test.edit(&candidate)
			if got := AdmitIdle(candidate, policy); got.Verdict != test.want {
				t.Fatalf("verdict = %s, want %s: %+v", got.Verdict, test.want, got)
			}
		})
	}
}

func TestAggregateRequiresIndependentTrials(t *testing.T) {
	trials := []AdmittedTrial{
		{
			StateIdentity: "s", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "protocol",
			TrialID: "1", AdmissionPolicyVersion: "a", EstimatorIdentity: "e", Batch: 1,
			IncrementalJoulesPerItem: 1, LatencyP99Milliseconds: 2,
		},
		{
			StateIdentity: "s", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "protocol",
			TrialID: "2", AdmissionPolicyVersion: "a", EstimatorIdentity: "e", Batch: 1,
			IncrementalJoulesPerItem: 1.1, LatencyP99Milliseconds: 3,
		},
		{
			StateIdentity: "s", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "protocol",
			TrialID: "3", AdmissionPolicyVersion: "a", EstimatorIdentity: "e", Batch: 1,
			IncrementalJoulesPerItem: .9, LatencyP99Milliseconds: 2.5,
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
	mixed = append([]AdmittedTrial(nil), trials...)
	mixed[2].EnvironmentIdentity = otherPolicy
	if _, err := Aggregate(mixed, pointPolicy("v1")); err == nil {
		t.Fatal("expected mixed environment identity rejection")
	}
	mixed = append([]AdmittedTrial(nil), trials...)
	mixed[2].MeasurementProtocolIdentity = otherPolicy
	if _, err := Aggregate(mixed, pointPolicy("v1")); err == nil {
		t.Fatal("expected mixed measurement protocol rejection")
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
			EnvironmentIdentity:          "environment",
			MeasurementProtocolIdentity:  "protocol",
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
		{"mixed environment", func() []Point {
			values := []Point{p(1, 1, .001), p(2, .99, .001)}
			values[1].EnvironmentIdentity = otherPolicy
			return values
		}(), BSatUnavailable, 0},
		{"mixed measurement protocol", func() []Point {
			values := []Point{p(1, 1, .001), p(2, .99, .001)}
			values[1].MeasurementProtocolIdentity = otherPolicy
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

func rawEvidence(key string) RawEvidenceIdentity {
	return RawEvidenceIdentity{
		Store: "chill-evidence", Key: "runs/" + key + "/raw.json",
		Digest: "sha256:" + strings.Repeat("a", 64), Size: 1,
	}
}
