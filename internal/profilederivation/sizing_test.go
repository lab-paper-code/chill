package profilederivation

import (
	"math"
	"strings"
	"testing"
)

func TestBSatConfirmatorySizingSelectsSmallestCount(t *testing.T) {
	zeroVariance := testDiscoveryCurve(t, []float64{1, .97, .94, .90})
	zeroResult, err := deriveBSatConfirmatorySizing(zeroVariance, 3, testBSatPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if zeroResult.Status != BSatCandidateOnly || zeroResult.Code != BSatCodeConfirmatoryTrialsSized ||
		zeroResult.ConfirmatoryTrials == nil || *zeroResult.ConfirmatoryTrials != 3 ||
		zeroResult.GlobalStatementCount != 72 || zeroResult.OneSidedQuantile <= .99 ||
		zeroResult.ProjectedTotalActiveRuns == nil || *zeroResult.ProjectedTotalActiveRuns != 18 {
		t.Fatalf("unexpected zero-variance sizing: %+v", zeroResult)
	}

	variable := testVariableDiscoveryCurve(t, []float64{1, .97, .94, .90}, .01)
	variableResult, err := deriveBSatConfirmatorySizing(variable, 3, testBSatPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if variableResult.ConfirmatoryTrials == nil || *variableResult.ConfirmatoryTrials != 4 ||
		variableResult.ProjectedTotalActiveRuns == nil || *variableResult.ProjectedTotalActiveRuns != 20 ||
		variableResult.ProjectedMaximumRelativeHalfWidth == nil ||
		*variableResult.ProjectedMaximumRelativeHalfWidth > .05 {
		t.Fatalf("unexpected variable sizing: %+v", variableResult)
	}
}

func TestBSatConfirmatorySizingReturnsBudgetInsufficient(t *testing.T) {
	policy := testBSatPolicy()
	policy.MaximumConfirmatoryTrials = 3
	curve := testVariableDiscoveryCurve(t, []float64{1, .97, .94, .90}, .5)
	result, err := deriveBSatConfirmatorySizing(curve, 3, policy)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BSatUnavailable || result.Code != BSatCodeConfirmatoryBudgetInsufficient ||
		result.ConfirmatoryTrials != nil || result.GlobalStatementCount != 9 {
		t.Fatalf("unexpected budget result: %+v", result)
	}
}

func TestBSatConfirmatorySizingUsesConfiguredFamilyWiseAlpha(t *testing.T) {
	curve := testDiscoveryCurve(t, []float64{1, .97, .94, .90})
	strictPolicy := testBSatPolicy()
	strict, err := deriveBSatConfirmatorySizing(curve, 3, strictPolicy)
	if err != nil {
		t.Fatal(err)
	}
	relaxedPolicy := strictPolicy
	relaxedPolicy.FamilyWiseAlpha = 0.1
	relaxed, err := deriveBSatConfirmatorySizing(curve, 3, relaxedPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if strict.GlobalStatementCount != relaxed.GlobalStatementCount ||
		strict.OneSidedQuantile <= relaxed.OneSidedQuantile ||
		!equalWithinTolerance(
			relaxed.OneSidedQuantile,
			1-relaxedPolicy.FamilyWiseAlpha/float64(relaxed.GlobalStatementCount),
		) {
		t.Fatalf("configured family-wise alpha was not applied: strict=%+v relaxed=%+v", strict, relaxed)
	}
}

func TestBSatConfirmatorySizingRejectsCandidateAndNumericDrift(t *testing.T) {
	curve := testDiscoveryCurve(t, []float64{1, .97, .94, .90})
	if _, err := deriveBSatConfirmatorySizing(curve, 2, testBSatPolicy()); err == nil {
		t.Fatal("expected non-discovery candidate rejection")
	}
	overflow := testBSatPolicy()
	overflow.MaximumConfirmatoryTrials = math.MaxInt
	overflow.MaximumActiveRunBudget = math.MaxInt
	if _, err := deriveBSatConfirmatorySizing(curve, 3, overflow); err == nil {
		t.Fatal("expected global statement overflow rejection")
	}
}

func TestBSatConfirmatorySizingRejectsSparseEligibleSpan(t *testing.T) {
	shared := validSharedContract()
	shared.EligibleBatches = []int{1, 2, 4, 8}
	contract, err := FreezeCurveFamilyContract(shared)
	if err != nil {
		t.Fatal(err)
	}
	family, err := VerifyCurveFamily(contract, testVerifiedState(contract))
	if err != nil {
		t.Fatal(err)
	}
	curve, err := DeriveCurve(
		[]Pass{testPass(t, family, []float64{1, .97, .94, .90}, "sparse-sizing")},
		family,
		testFamilyBaselines(t, family),
		pointPolicy("sparse-sizing-point"),
	)
	if err != nil {
		t.Fatal(err)
	}
	policy := testBSatPolicy()
	policy.KMax = 8

	_, err = deriveBSatConfirmatorySizing(curve, 4, policy)
	if err == nil {
		t.Fatal("expected the sparse confirmatory span to be rejected")
	}
	want := `confirmatory method "bonferroni-welch-relative-precision-v1" requires consecutive eligible batches from candidate 4 through KMax 8; gap at batch 5`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("sparse confirmatory error = %q, want it to contain %q", err, want)
	}
}

func TestBSatGlobalStatementCountStartsAtPointPolicyMinimum(t *testing.T) {
	policy := testBSatPolicy()
	policy.MinimumPointTrials = 4
	policy.DiscoveryTrials = 4
	count, err := bsatGlobalStatementCount(policy)
	if err != nil {
		t.Fatal(err)
	}
	if count != 63 {
		t.Fatalf("global statement count = %d, want 63", count)
	}
}

func TestProjectedContrastPreservesSharedBaselineCancellation(t *testing.T) {
	baseline := IdleBaseline{MeanWallWattsVariance: .04, DegreesOfFreedom: 2}
	reference := Point{
		StateIdentity: "shared", IncrementalSampleVariance: 0,
		MeanBaselineCoefficientSecondsPerItem: .2,
	}
	later := Point{
		StateIdentity: "shared", IncrementalSampleVariance: 0,
		MeanBaselineCoefficientSecondsPerItem: .2,
	}
	shared, err := contrastHalfWidthForTrials(reference, baseline, later, baseline, 3, .975)
	if err != nil {
		t.Fatal(err)
	}
	later.StateIdentity = "distinct"
	distinct, err := contrastHalfWidthForTrials(reference, baseline, later, baseline, 3, .975)
	if err != nil {
		t.Fatal(err)
	}
	if shared != 0 || distinct <= 0 {
		t.Fatalf("shared=%g distinct=%g", shared, distinct)
	}
}

func testVariableDiscoveryCurve(t *testing.T, means []float64, deviation float64) Curve {
	t.Helper()
	_, family := testVerifiedFamily(t, len(means))
	pass := testPass(t, family, means, "variable")
	offsets := []float64{-deviation, 0, deviation}
	for batchIndex := range pass.Batches {
		for trialIndex := range pass.Batches[batchIndex].Runs {
			total := means[batchIndex] + 1 + offsets[trialIndex]
			pass.Batches[batchIndex].Runs[trialIndex].Admission.TotalWallJoules = &total
		}
	}
	curve, err := DeriveCurve(
		[]Pass{pass}, family, testFamilyBaselines(t, family), pointPolicy("variable-point"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return curve
}
