package profilederivation

import "testing"

func TestPlan5PolicyBoundDerivationRecordsBothDigests(t *testing.T) {
	curve, discoveryPolicy := testPolicyBoundTwoPassCurve(
		t, []float64{1, .97, .94, .90}, []float64{.94, .90}, 3, nil,
	)
	discoveryCurve, err := curve.discoveryOnly()
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := DeriveBSatDiscovery(discoveryCurve, discoveryPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if discovery.CandidateBatch == nil || *discovery.CandidateBatch != 3 ||
		discovery.PolicyVersion != discoveryPolicy.Digest() {
		t.Fatalf("unexpected policy-bound discovery: %+v", discovery)
	}
	sizing, err := DeriveBSatConfirmatorySizing(discoveryCurve, 3, discoveryPolicy)
	if err != nil {
		t.Fatal(err)
	}
	confirmatoryPolicy, err := FreezeConfirmatoryDerivationPolicy(discoveryPolicy, sizing)
	if err != nil {
		t.Fatal(err)
	}
	result, err := DeriveBSatConfirmatory(curve, 3, confirmatoryPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BSatAccepted ||
		result.DiscoveryPolicyDigest != discoveryPolicy.Digest() ||
		result.ConfirmatoryPolicyDigest != confirmatoryPolicy.Digest() ||
		result.CurveFamilyContractDigest != discoveryPolicy.CurveFamilyContractDigest() {
		t.Fatalf("final result lost policy provenance: %+v", result)
	}
}

func TestPlan5PolicyBoundDerivationRetainsBudgetInsufficientOutcome(t *testing.T) {
	curve, discoveryPolicy := testPolicyBoundTwoPassCurve(
		t, []float64{1, .97, .94, .90}, nil, 0,
		func(spec *DiscoveryDerivationPolicySpec) {
			spec.BatchSaturation.MaximumConfirmatoryTrials = 3
			spec.BatchSaturation.MaximumActiveRunBudget = 17
		},
	)
	sizing, err := DeriveBSatConfirmatorySizing(curve, 3, discoveryPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if sizing.Status != BSatUnavailable || sizing.Code != BSatCodeConfirmatoryBudgetInsufficient {
		t.Fatalf("expected budget-insufficient sizing: %+v", sizing)
	}
	confirmatoryPolicy, err := FreezeConfirmatoryDerivationPolicy(discoveryPolicy, sizing)
	if err != nil {
		t.Fatal(err)
	}
	result, err := DeriveBSatConfirmatory(curve, 3, confirmatoryPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BSatUnavailable || result.Code != BSatCodeConfirmatoryBudgetInsufficient ||
		result.DiscoveryPolicyDigest != discoveryPolicy.Digest() ||
		result.ConfirmatoryPolicyDigest != confirmatoryPolicy.Digest() {
		t.Fatalf("budget result lost policy provenance: %+v", result)
	}
}

func TestPlan5PolicyBoundaryRejectsCurvePolicyDrift(t *testing.T) {
	curve, discoveryPolicy := testPolicyBoundTwoPassCurve(
		t, []float64{1, .97, .94, .90}, []float64{.94, .90}, 3, nil,
	)
	discoveryCurve, err := curve.discoveryOnly()
	if err != nil {
		t.Fatal(err)
	}

	wrongFamilySpec := discoveryPolicy.spec()
	wrongFamilySpec.CurveFamilyContractDigest = curveFamilyContractSchemaVersion +
		"@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	wrongFamily, err := FreezeDiscoveryDerivationPolicy(wrongFamilySpec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveBSatDiscovery(discoveryCurve, wrongFamily); err == nil {
		t.Fatal("expected curve-family policy drift rejection")
	}

	pointDrift, err := curve.discoveryOnly()
	if err != nil {
		t.Fatal(err)
	}
	pointDrift.passes[0].points[0].PointPolicyVersion = "sha256:drift"
	if _, err := DeriveBSatDiscovery(pointDrift, discoveryPolicy); err == nil {
		t.Fatal("expected point-policy drift rejection")
	}

	baselineDrift, err := curve.discoveryOnly()
	if err != nil {
		t.Fatal(err)
	}
	for state, baseline := range baselineDrift.baselines {
		baseline.PolicyVersion = "sha256:drift"
		baselineDrift.baselines[state] = baseline
		break
	}
	if _, err := DeriveBSatDiscovery(baselineDrift, discoveryPolicy); err == nil {
		t.Fatal("expected idle-policy drift rejection")
	}
}

func TestPlan5PolicyBoundaryRejectsFrozenAllocationDrift(t *testing.T) {
	curve, discoveryPolicy := testPolicyBoundTwoPassCurve(
		t, []float64{1, .97, .94, .90}, []float64{.94, .90}, 3, nil,
	)
	discoveryCurve, err := curve.discoveryOnly()
	if err != nil {
		t.Fatal(err)
	}
	sizing, err := DeriveBSatConfirmatorySizing(discoveryCurve, 3, discoveryPolicy)
	if err != nil {
		t.Fatal(err)
	}
	trials := 4
	sizing.ConfirmatoryTrials = &trials
	driftedPolicy, err := FreezeConfirmatoryDerivationPolicy(discoveryPolicy, sizing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveBSatConfirmatory(curve, 3, driftedPolicy); err == nil {
		t.Fatal("expected recomputed sizing mismatch rejection")
	}
	validSizing, err := DeriveBSatConfirmatorySizing(discoveryCurve, 3, discoveryPolicy)
	if err != nil {
		t.Fatal(err)
	}
	validPolicy, err := FreezeConfirmatoryDerivationPolicy(discoveryPolicy, validSizing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveBSatConfirmatory(curve, 2, validPolicy); err == nil {
		t.Fatal("expected frozen candidate mismatch rejection")
	}
}

func testPolicyBoundTwoPassCurve(
	t *testing.T,
	discoveryMeans []float64,
	confirmationMeans []float64,
	confirmatoryTrials int,
	mutatePolicy func(*DiscoveryDerivationPolicySpec),
) (Curve, DiscoveryDerivationPolicy) {
	t.Helper()
	contract, family := testVerifiedFamily(t, len(discoveryMeans))
	spec := testDiscoveryDerivationPolicySpecForDigest(contract.Digest())
	if mutatePolicy != nil {
		mutatePolicy(&spec)
	}
	discoveryPolicy, err := FreezeDiscoveryDerivationPolicy(spec)
	if err != nil {
		t.Fatal(err)
	}
	idlePolicy, err := discoveryPolicy.PointPolicy(PointPolicyStageIdleBaseline)
	if err != nil {
		t.Fatal(err)
	}
	activePolicy, err := discoveryPolicy.PointPolicy(PointPolicyStageIncrementalPoint)
	if err != nil {
		t.Fatal(err)
	}
	baselines := testFamilyBaselines(t, family)
	for state, baseline := range baselines {
		baseline.PolicyVersion = idlePolicy.Version
		baselines[state] = baseline
	}
	passes := []Pass{testPass(t, family, discoveryMeans, "discovery")}
	if confirmationMeans != nil {
		passes = append(passes, testConfirmatoryPass(
			t, family, 3, confirmationMeans, "confirmation", confirmatoryTrials, 0,
		))
	}
	curve, err := DeriveCurve(passes, family, baselines, activePolicy)
	if err != nil {
		t.Fatal(err)
	}
	return curve, discoveryPolicy
}
