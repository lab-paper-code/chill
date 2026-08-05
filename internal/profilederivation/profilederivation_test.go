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
	if baseline.DegreesOfFreedom != 2 ||
		math.Abs(baseline.MeanWallWattsVariance-(.01/3)) > 1e-12 ||
		math.Abs(baseline.ConfidenceHalfWidth-studentT95(2)*math.Sqrt(.01/3)) > 1e-12 {
		t.Fatalf("idle sufficient statistics are incomplete: %#v", baseline)
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
	if math.Abs(admitted.IncrementalJoulesPerItem-.5) > 1e-9 ||
		math.Abs(admitted.ActiveWallJoulesPerItem-1) > 1e-9 ||
		math.Abs(admitted.BaselineCoefficientSecondsPerItem-.1) > 1e-9 ||
		math.Abs(admitted.BaselineMeanWallWattsVariance-baseline.MeanWallWattsVariance) > 1e-12 ||
		admitted.BaselineDegreesOfFreedom != baseline.DegreesOfFreedom {
		t.Fatalf("active sufficient statistics are incomplete: %#v", admitted)
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
	mismatchedCount := result
	mismatchedCount.CompletedItems--
	if _, err := IncrementalTrial(trial, mismatchedCount, baseline); err == nil {
		t.Fatal("expected completed-item count mismatch rejection")
	}
	invalidBaseline := baseline
	invalidBaseline.ConfidenceHalfWidth = math.NaN()
	if _, err := IncrementalTrial(trial, result, invalidBaseline); err == nil {
		t.Fatal("expected invalid baseline uncertainty rejection")
	}
	invalidBaseline = baseline
	invalidBaseline.MeanWallWattsVariance = math.NaN()
	if _, err := IncrementalTrial(trial, result, invalidBaseline); err == nil {
		t.Fatal("expected invalid baseline mean variance rejection")
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
			ActiveWallJoulesPerItem: 2, BaselineCoefficientSecondsPerItem: 1, BaselineDegreesOfFreedom: 2,
			IncrementalJoulesPerItem: 1, LatencyP99Milliseconds: 2,
		},
		{
			StateIdentity: "s", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "protocol",
			TrialID: "2", AdmissionPolicyVersion: "a", EstimatorIdentity: "e", Batch: 1,
			ActiveWallJoulesPerItem: 2.1, BaselineCoefficientSecondsPerItem: 1, BaselineDegreesOfFreedom: 2,
			IncrementalJoulesPerItem: 1.1, LatencyP99Milliseconds: 3,
		},
		{
			StateIdentity: "s", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "protocol",
			TrialID: "3", AdmissionPolicyVersion: "a", EstimatorIdentity: "e", Batch: 1,
			ActiveWallJoulesPerItem: 1.9, BaselineCoefficientSecondsPerItem: 1, BaselineDegreesOfFreedom: 2,
			IncrementalJoulesPerItem: .9, LatencyP99Milliseconds: 2.5,
		},
	}
	point, err := Aggregate(trials, pointPolicy("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if point.MeanIncrementalJoulesPerItem != 1 || point.Confidence.HalfWidth <= 0 ||
		point.PreviousConfidence.HalfWidth <= 0 || point.LatencyP99Milliseconds != 3 {
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
	mixed = append([]AdmittedTrial(nil), trials...)
	mixed[2].BaselineDegreesOfFreedom++
	if _, err := Aggregate(mixed, pointPolicy("v1")); err == nil {
		t.Fatal("expected mixed baseline sufficient-statistics rejection")
	}
	uncertain := append([]AdmittedTrial(nil), trials...)
	baselineVariance := math.Pow(.5/studentT95(2), 2)
	for index := range uncertain {
		uncertain[index].BaselineUncertaintyJoulesPerItem = .5
		uncertain[index].BaselineMeanWallWattsVariance = baselineVariance
	}
	uncertainPoint, err := Aggregate(uncertain, pointPolicy("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if uncertainPoint.Confidence.VarianceOfMean == nil ||
		*uncertainPoint.Confidence.VarianceOfMean < baselineVariance ||
		uncertainPoint.PreviousConfidence.HalfWidth < .5 {
		t.Fatal("baseline uncertainty was discarded")
	}
	uncertain[0].BaselineUncertaintyJoulesPerItem = math.NaN()
	if _, err := Aggregate(uncertain, pointPolicy("v1")); err == nil {
		t.Fatal("expected invalid uncertainty contribution rejection")
	}
}

func TestAggregateRebuildsAndVersionsPointConfidence(t *testing.T) {
	idleValues := []float64{4.929898, 4.893351, 4.897335}
	idleTrials := make([]IdleTrial, len(idleValues))
	for index, value := range idleValues {
		id := []string{"idle-a", "idle-b", "idle-c"}[index]
		idleTrials[index] = IdleTrial{
			StateIdentity: "state", EnvironmentIdentity: "environment",
			MeasurementProtocolIdentity: "idle-protocol", TrialID: id,
			RawEvidence: rawEvidence(id), AdmissionPolicyVersion: "idle-admission",
			MeanWallWatts: value,
		}
	}
	baseline, err := DeriveIdleBaseline(idleTrials, pointPolicy("idle-point"))
	if err != nil {
		t.Fatal(err)
	}

	incrementalValues := []float64{0.068805, 0.065637, 0.065410}
	completedItems := []int{5417, 5255, 5178}
	activeTrials := make([]AdmittedTrial, len(incrementalValues))
	for index, incremental := range incrementalValues {
		trialID := []string{"active-a", "active-b", "active-c"}[index]
		total := incremental*float64(completedItems[index]) + baseline.MeanWallWatts*30
		trial := Trial{
			StateIdentity: "state", EnvironmentIdentity: "environment",
			MeasurementProtocolIdentity: "active-protocol", TrialID: trialID,
			Batch: 1, CompletedItems: completedItems[index], LatencyMilliseconds: []float64{1},
		}
		activeTrials[index], err = IncrementalTrial(trial, AdmissionResult{
			Verdict: VerdictAccepted, PolicyVersion: "active-admission",
			StateIdentity: "state", EnvironmentIdentity: "environment",
			MeasurementProtocolIdentity: "active-protocol", TrialID: trialID,
			TotalWallJoules: &total, WindowSeconds: 30, CompletedItems: completedItems[index],
		}, baseline)
		if err != nil {
			t.Fatal(err)
		}
	}

	point, err := Aggregate(activeTrials, pointPolicy("active-point"))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(point.MeanIncrementalJoulesPerItem-0.06661733333333333) > 1e-12 {
		t.Fatalf("live point estimate moved: %.15f", point.MeanIncrementalJoulesPerItem)
	}
	if point.Confidence.EstimatorVersion != PointEstimatorVersion ||
		point.Confidence.Method != PointConfidenceMethod ||
		point.Confidence.VarianceOfMean == nil ||
		point.Confidence.EffectiveDegreesOfFreedom == nil ||
		point.Confidence.HalfWidth <= 0 {
		t.Fatalf("rebuilt confidence is incomplete: %+v", point.Confidence)
	}
	if point.PreviousConfidence.EstimatorVersion != PreviousPointEstimatorVersion ||
		point.PreviousConfidence.Method != PreviousPointConfidenceMethod ||
		math.Abs(point.PreviousConfidence.HalfWidth-0.004723645122343838) > 1e-12 {
		t.Fatalf("previous live interval was not retained: %+v", point.PreviousConfidence)
	}
	activeVariance := point.IncrementalSampleVariance / float64(len(activeTrials))
	baselineVariance := math.Pow(point.MeanBaselineCoefficientSecondsPerItem, 2) *
		baseline.MeanWallWattsVariance
	wantVariance := activeVariance + baselineVariance
	wantDegreesOfFreedom := wantVariance * wantVariance /
		(math.Pow(activeVariance, 2)/2 + math.Pow(baselineVariance, 2)/2)
	wantHalfWidth := studentTQuantile(0.975, wantDegreesOfFreedom) * math.Sqrt(wantVariance)
	if math.Abs(*point.Confidence.VarianceOfMean-wantVariance) > 1e-15 ||
		math.Abs(*point.Confidence.EffectiveDegreesOfFreedom-wantDegreesOfFreedom) > 1e-12 ||
		math.Abs(point.Confidence.HalfWidth-wantHalfWidth) > 1e-12 ||
		point.Confidence.HalfWidth == point.PreviousConfidence.HalfWidth {
		t.Fatalf("Welch components were not preserved: %+v", point.Confidence)
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
			Batch:                        batch,
			MeanIncrementalJoulesPerItem: mean,
			Confidence: PointConfidence{
				EstimatorVersion: PointEstimatorVersion,
				Method:           PointConfidenceMethod, Level: PointConfidenceLevel, HalfWidth: half,
			},
			PreviousConfidence: PointConfidence{
				EstimatorVersion: PreviousPointEstimatorVersion,
				Method:           PreviousPointConfidenceMethod, Level: PointConfidenceLevel, HalfWidth: half,
			},
		}
	}
	policy := LegacyBSatPolicy{Version: "v1", RelativeEquivalenceTolerance: .02}
	tests := []struct {
		name   string
		points []Point
		want   BSatStatus
		batch  int
	}{
		{"accepted", []Point{p(1, 1, .001), p(2, .8, .001), p(3, .795, .001), p(4, .8, .001)}, BSatAccepted, 2},
		{"candidate uncertainty", []Point{p(1, 1, .001), p(2, .8, .05), p(3, .795, .05)}, BSatCandidateOnly, 2},
		{"censored", []Point{p(1, 1, .001), p(2, .8, .001), p(3, .6, .001)}, BSatCensored, 0},
		{"ambiguous later drop", []Point{p(1, 1, .001), p(2, .99, .001), p(3, .7, .001)}, LegacyBSatAmbiguous, 1},
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
	return PointPolicy{Version: version, MinimumTrials: 3}
}

func rawEvidence(key string) RawEvidenceIdentity {
	return RawEvidenceIdentity{
		Store: "chill-evidence", Key: "runs/" + key + "/raw.json",
		Digest: "sha256:" + strings.Repeat("a", 64), Size: 1,
	}
}
