package profilederivation

import (
	"math"
	"reflect"
	"testing"
)

func TestBSatDiscoverySelectsSmallestDomainWideCandidate(t *testing.T) {
	curve := testDiscoveryCurve(t, []float64{1, .97, .94, .90})
	result, err := deriveBSatDiscovery(curve, testBSatPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BSatCandidateOnly || result.Code != BSatCodeDiscoveryCandidateSelected ||
		result.CandidateBatch == nil || *result.CandidateBatch != 3 ||
		result.CandidateMaximumRemainingImprovement == nil ||
		math.Abs(*result.CandidateMaximumRemainingImprovement-(.04/.94)) > 1e-12 ||
		len(result.MeasuredBatches) != 4 || result.CurveFamilyName == "" {
		t.Fatalf("unexpected discovery result: %+v", result)
	}
}

func TestBSatDiscoveryTreatsKMaxAsLargestEligibleBatch(t *testing.T) {
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
		[]Pass{testPass(t, family, []float64{1, .97, .94, .90}, "sparse-discovery")},
		family,
		testFamilyBaselines(t, family),
		pointPolicy("discovery-point"),
	)
	if err != nil {
		t.Fatal(err)
	}
	policy := testBSatPolicy()
	policy.KMax = 8
	result, err := deriveBSatDiscovery(curve, policy)
	if err != nil {
		t.Fatal(err)
	}
	if result.KMax != 8 || result.CandidateBatch == nil || *result.CandidateBatch != 4 ||
		!reflect.DeepEqual(result.MeasuredBatches, []int{1, 2, 4, 8}) {
		t.Fatalf("unexpected sparse discovery result: %+v", result)
	}

	policy.KMax = 4
	if _, err := deriveBSatDiscovery(curve, policy); err == nil {
		t.Fatal("expected KMax equal to eligible cardinality but not its largest batch to be rejected")
	}
}

func TestBSatDiscoveryReturnsNoSaturationCensoring(t *testing.T) {
	noCandidate, err := deriveBSatDiscovery(
		testDiscoveryCurve(t, []float64{1, .8, .6, .4}),
		testBSatPolicy(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if noCandidate.Status != BSatCensored || noCandidate.Code != BSatCodeNoSaturationObserved ||
		noCandidate.CandidateBatch != nil {
		t.Fatalf("unexpected no-candidate result: %+v", noCandidate)
	}
}

func TestBSatDiscoveryRejectsUnfrozenOrMixedScope(t *testing.T) {
	curve := testDiscoveryCurve(t, []float64{1, .9, .89, .88})
	policy := testBSatPolicy()

	invalid := policy
	invalid.KMax = 1
	if _, err := deriveBSatDiscovery(curve, invalid); err == nil {
		t.Fatal("expected invalid kmax rejection")
	}
	mismatchedFamily := policy
	mismatchedFamily.KMax = 3
	mismatchedFamily.MaximumActiveRunBudget = 30
	if _, err := deriveBSatDiscovery(curve, mismatchedFamily); err == nil {
		t.Fatal("expected curve-family domain mismatch rejection")
	}
	mismatchedTrials := policy
	mismatchedTrials.DiscoveryTrials = 4
	if _, err := deriveBSatDiscovery(curve, mismatchedTrials); err == nil {
		t.Fatal("expected discovery trial-count mismatch rejection")
	}

	_, family := testVerifiedFamily(t, 4)
	discovery := testPass(t, family, []float64{1, .9, .89, .88}, "discovery")
	confirmation := testPass(t, family, []float64{1, .9, .89, .88}, "confirmation")
	confirmation.Role = PassConfirmatory
	twoPass, err := DeriveCurve(
		[]Pass{discovery, confirmation}, family, testFamilyBaselines(t, family), pointPolicy("point"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deriveBSatDiscovery(twoPass, policy); err == nil {
		t.Fatal("expected confirmatory pass rejection at discovery boundary")
	}
}

func testDiscoveryCurve(t *testing.T, means []float64) Curve {
	t.Helper()
	_, family := testVerifiedFamily(t, len(means))
	curve, err := DeriveCurve(
		[]Pass{testPass(t, family, means, "discovery")},
		family,
		testFamilyBaselines(t, family),
		pointPolicy("discovery-point"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return curve
}

func testBSatPolicy() BSatPolicy {
	return BSatPolicy{
		Version: "bsat-discovery-v1", KMax: 4,
		RelativeImprovementTolerance: .05, MinimumPointTrials: 3, DiscoveryTrials: 3,
		MaximumConfirmatoryTrials: 10, MaximumActiveRunBudget: 100,
		FamilyWiseAlpha: 0.05,
	}
}
