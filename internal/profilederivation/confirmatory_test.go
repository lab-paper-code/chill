package profilederivation

import (
	"math"
	"slices"
	"testing"
)

func TestBSatConfirmatoryGoldenOutcomes(t *testing.T) {
	policy := testBSatPolicy()
	tests := []struct {
		name              string
		discoveryMeans    []float64
		confirmationMeans []float64
		deviation         float64
		wantStatus        BSatStatus
		wantCode          string
	}{
		{
			name:           "domain no improvement confirmed",
			discoveryMeans: []float64{1, .97, .94, .90}, confirmationMeans: []float64{.94, .90},
			wantStatus: BSatAccepted, wantCode: BSatCodeDomainNoImprovementConfirmed,
		},
		{
			name:           "candidate only",
			discoveryMeans: []float64{1, .97, .94, .90}, confirmationMeans: []float64{.94, .88},
			wantStatus: BSatCandidateOnly, wantCode: BSatCodeDomainNoImprovementNotConfirmed,
		},
		{
			name:           "non-positive reference lower bound",
			discoveryMeans: []float64{1, .97, .01, .0098}, confirmationMeans: []float64{.01, .0098},
			deviation: .009, wantStatus: BSatUnavailable, wantCode: BSatCodeNonPositiveReferenceLowerBound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			curve := testTwoPassCurve(
				t, test.discoveryMeans, test.confirmationMeans, 3, test.deviation,
			)
			result, err := deriveBSatConfirmatory(curve, 3, policy)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != test.wantStatus || result.Code != test.wantCode ||
				result.DiscoverySelectedBatch != 3 || result.MinimalityConfidenceConfirmed ||
				result.ConfirmatoryTrials != 3 || result.GlobalStatementCount != 72 ||
				result.ConfirmatoryEstimatorVersion != BSatConfirmatoryEstimatorVersion ||
				result.ConfidenceLevel != .95 ||
				len(result.DiscoveryPoints) != 4 || len(result.ConfirmatoryPoints) != 2 ||
				len(result.IdleBaselinesByState) != 1 {
				t.Fatalf("unexpected confirmatory result: %+v", result)
			}
			if test.wantStatus == BSatAccepted {
				if !result.DomainNoImprovementConfirmed || len(result.ContrastBounds) != 1 ||
					len(result.ContrastCovariances) != 1 ||
					result.MaximumRemainingImprovementUpperBound == nil ||
					math.Abs(*result.MaximumRemainingImprovementUpperBound-(.04/.94)) > 1e-12 {
					t.Fatalf("accepted result lost its domain bound: %+v", result)
				}
			}
		})
	}
}

func TestContiguousConfirmatoryCurvePlanStillSizesAndDerives(t *testing.T) {
	policy := testBSatPolicy()
	curve := testTwoPassCurve(t, []float64{1, .97, .94, .90}, []float64{.94, .90}, 3, 0)
	discovery, err := curve.discoveryOnly()
	if err != nil {
		t.Fatal(err)
	}
	sizing, err := deriveBSatConfirmatorySizing(discovery, 3, policy)
	if err != nil {
		t.Fatal(err)
	}
	if sizing.ConfirmatoryTrials == nil || *sizing.ConfirmatoryTrials != 3 ||
		!slices.Equal(sizing.ConfirmatoryBatchSizes, []int{3, 4}) ||
		sizing.ProjectedTotalActiveRuns == nil || *sizing.ProjectedTotalActiveRuns != 18 {
		t.Fatalf("contiguous confirmatory sizing changed: %+v", sizing)
	}

	result, err := deriveBSatConfirmatory(curve, 3, policy)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BSatAccepted || result.Code != BSatCodeDomainNoImprovementConfirmed ||
		!result.DomainNoImprovementConfirmed ||
		!slices.Equal(result.ConfirmatoryMeasuredBatchSizes, []int{3, 4}) {
		t.Fatalf("contiguous confirmatory derivation changed: %+v", result)
	}
}

func TestConfirmatoryContrastCovarianceUsesSharedActiveAndBaselineTerms(t *testing.T) {
	points := []Point{
		{
			Batch: 2, StateIdentity: "shared", IncrementalWorkSampleVariance: .03,
			MeanIdleBaselineSecondsPerItem: .2,
		},
		{
			Batch: 3, StateIdentity: "shared", IncrementalWorkSampleVariance: .06,
			MeanIdleBaselineSecondsPerItem: .1,
		},
		{
			Batch: 4, StateIdentity: "distinct", IncrementalWorkSampleVariance: .09,
			MeanIdleBaselineSecondsPerItem: .3,
		},
	}
	baselines := map[string]IdleBaseline{
		"shared":   {MeanWallWattsVariance: .04},
		"distinct": {MeanWallWattsVariance: .01},
	}
	covariances, err := confirmatoryContrastCovariances(points, baselines, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(covariances) != 3 ||
		math.Abs(covariances[0].CovarianceJoulesSquaredPerItemSquared-.0304) > 1e-12 ||
		math.Abs(covariances[1].CovarianceJoulesSquaredPerItemSquared-.0108) > 1e-12 ||
		math.Abs(covariances[2].CovarianceJoulesSquaredPerItemSquared-.0425) > 1e-12 {
		t.Fatalf("unexpected contrast covariance matrix: %+v", covariances)
	}
}

func TestBSatConfirmatoryReturnsBudgetInsufficientWithoutSecondPass(t *testing.T) {
	policy := testBSatPolicy()
	policy.MaximumConfirmatoryTrials = 3
	curve := testVariableDiscoveryCurve(t, []float64{1, .97, .94, .90}, .5)
	result, err := deriveBSatConfirmatory(curve, 3, policy)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BSatUnavailable || result.Code != BSatCodeConfirmatoryBudgetInsufficient ||
		result.ConfirmatoryTrials != 0 || len(result.ConfirmatoryPoints) != 0 {
		t.Fatalf("unexpected budget result: %+v", result)
	}
}

func TestBSatConfirmatoryRejectsAllocationDrift(t *testing.T) {
	curve := testTwoPassCurve(t, []float64{1, .97, .94, .90}, []float64{.94, .90}, 3, 0)
	if _, err := deriveBSatConfirmatory(curve, 2, testBSatPolicy()); err == nil {
		t.Fatal("expected candidate mismatch rejection")
	}
	wrongTrials := testTwoPassCurve(t, []float64{1, .97, .94, .90}, []float64{.94, .90}, 4, 0)
	if _, err := deriveBSatConfirmatory(wrongTrials, 3, testBSatPolicy()); err == nil {
		t.Fatal("expected confirmatory trial-count mismatch rejection")
	}
	discoveryOnly := testDiscoveryCurve(t, []float64{1, .97, .94, .90})
	if _, err := deriveBSatConfirmatory(discoveryOnly, 3, testBSatPolicy()); err == nil {
		t.Fatal("expected missing confirmatory pass rejection")
	}
}

func testTwoPassCurve(
	t *testing.T,
	discoveryMeans []float64,
	confirmationMeans []float64,
	confirmatoryTrials int,
	deviation float64,
) Curve {
	t.Helper()
	_, plan := testVerifiedCurvePlan(t, len(discoveryMeans))
	discovery := testPass(t, plan, discoveryMeans, "discovery")
	confirmation := testConfirmatoryPass(
		t, plan, 3, confirmationMeans, "confirmation", confirmatoryTrials, deviation,
	)
	curve, err := DeriveCurve(
		[]Pass{discovery, confirmation}, plan, testCurveBaselines(t, plan), pointPolicy("point"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return curve
}

func testConfirmatoryPass(
	t *testing.T,
	plan VerifiedCurvePlan,
	startBatch int,
	means []float64,
	idPrefix string,
	trials int,
	deviation float64,
) Pass {
	t.Helper()
	state := plan.state
	pass := Pass{Role: PassConfirmatory}
	for index, mean := range means {
		batch := startBatch + index
		batchInput := PassBatch{Batch: batch}
		for trialIndex := 0; trialIndex < trials; trialIndex++ {
			trialID := idPrefix + "-b" + string(rune('0'+batch)) + "-" + string(rune('1'+trialIndex))
			offset := (float64(trialIndex) - float64(trials-1)/2) * deviation
			total := mean + 1 + offset
			trial := Trial{
				StateIdentity: state.StateIdentity, EnvironmentIdentity: state.EnvironmentIdentity,
				MeasurementProtocolIdentity: state.ActiveProtocolIdentity,
				TrialID:                     trialID, Batch: batch, CompletedItems: 1,
				LatencyMilliseconds: []float64{1},
			}
			admission := AdmissionResult{
				Verdict: VerdictAccepted, PolicyVersion: "active-admission-v1",
				StateIdentity: trial.StateIdentity, EnvironmentIdentity: trial.EnvironmentIdentity,
				MeasurementProtocolIdentity: trial.MeasurementProtocolIdentity,
				TrialID:                     trialID, TotalWallJoules: &total, WindowSeconds: 1, CompletedItems: 1,
			}
			batchInput.Runs = append(batchInput.Runs, CurveRun{Trial: trial, Admission: admission})
		}
		pass.Batches = append(pass.Batches, batchInput)
	}
	return pass
}
