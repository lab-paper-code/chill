package profilederivation

import (
	"math"
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

func TestBSatDiscoveryReturnsTypedCensoring(t *testing.T) {
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

	_, family := testVerifiedFamily(t, 4)
	pass := testPass(t, family, []float64{1, .8}, "boundary")
	pass.Batches = append(pass.Batches, PassBatch{Batch: 3, CapabilityBoundary: true})
	curve, err := DeriveCurve(
		[]Pass{pass}, family, testFamilyBaselines(t, family), pointPolicy("discovery-point"),
	)
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := deriveBSatDiscovery(curve, testBSatPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if boundary.Status != BSatCensored || boundary.Code != BSatCodeCapabilityBoundary ||
		boundary.BoundaryBatch == nil || *boundary.BoundaryBatch != 3 ||
		len(boundary.MeasuredBatches) != 2 {
		t.Fatalf("unexpected capability result: %+v", boundary)
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
