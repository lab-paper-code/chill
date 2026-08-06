package profilederivation

import (
	"bytes"
	"reflect"
	"testing"
)

func TestDiscoveryDerivationPolicyCanonicalRoundTrip(t *testing.T) {
	spec := testDiscoveryDerivationPolicySpec(t)
	policy, err := FreezeDiscoveryDerivationPolicy(spec)
	if err != nil {
		t.Fatal(err)
	}
	reordered := spec
	reordered.PointPolicies = []PointPolicyEntry{spec.PointPolicies[1], spec.PointPolicies[0]}
	second, err := FreezeDiscoveryDerivationPolicy(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(policy.spec(), second.spec()) {
		t.Fatal("point-policy order changed configured policy semantics")
	}
	canonical, err := policy.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDiscoveryDerivationPolicy(append([]byte(" \n"), canonical...))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.spec(), policy.spec()) {
		t.Fatal("decoded policy lost configured semantics")
	}

	idle, err := decoded.PointPolicy(PointPolicyStageIdleBaseline)
	if err != nil {
		t.Fatal(err)
	}
	active, err := decoded.PointPolicy(PointPolicyStageIncrementalPoint)
	if err != nil {
		t.Fatal(err)
	}
	bsat, err := decoded.BSatPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if idle.Version == "" || idle.MinimumTrials != 3 ||
		idle.ConfidenceLevel != 0.95 ||
		active.Version == "" || active.MinimumTrials != 3 ||
		active.ConfidenceLevel != 0.95 ||
		bsat.Version == "" || bsat.KMax != 4 ||
		bsat.MinimumPointTrials != 3 || bsat.FamilyWiseAlpha != 0.05 ||
		decoded.Name() != "e1-batch-saturation" ||
		decoded.CurveFamilyContractDigest() != spec.CurveFamilyContractDigest {
		t.Fatalf("decoded policy lost configured semantics: %+v %+v %+v", idle, active, bsat)
	}
}

func TestDiscoveryDerivationPolicyRejectsInvalidFiles(t *testing.T) {
	policy, err := FreezeDiscoveryDerivationPolicy(testDiscoveryDerivationPolicySpec(t))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := policy.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"unknown field":  bytes.Replace(canonical, []byte(`"name":`), []byte(`"unknown":1,"name":`), 1),
		"trailing value": append(append([]byte(nil), canonical...), []byte(` {}`)...),
		"wrong schema": bytes.Replace(
			canonical,
			[]byte(DiscoveryDerivationPolicySchemaVersion),
			[]byte("chill.dacs.io/discovery-derivation-policy.v9"),
			1,
		),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeDiscoveryDerivationPolicy(payload); err == nil {
				t.Fatal("expected strict policy-file rejection")
			}
		})
	}
}

func TestFreezeDiscoveryDerivationPolicyRejectsInvalidSemantics(t *testing.T) {
	valid := testDiscoveryDerivationPolicySpec(t)
	tests := map[string]func(*DiscoveryDerivationPolicySpec){
		"duplicate stage": func(spec *DiscoveryDerivationPolicySpec) {
			spec.PointPolicies[1].Stage = spec.PointPolicies[0].Stage
		},
		"duplicate name": func(spec *DiscoveryDerivationPolicySpec) {
			spec.PointPolicies[1].Name = spec.PointPolicies[0].Name
		},
		"unsupported stage": func(spec *DiscoveryDerivationPolicySpec) {
			spec.PointPolicies[1].Stage = "reporting"
		},
		"too few trials": func(spec *DiscoveryDerivationPolicySpec) {
			spec.PointPolicies[0].MinimumTrials = 2
		},
		"discovery below point minimum": func(spec *DiscoveryDerivationPolicySpec) {
			spec.PointPolicies[1].MinimumTrials = 4
		},
		"invalid curve identity": func(spec *DiscoveryDerivationPolicySpec) {
			spec.CurveFamilyContractDigest = " has-whitespace"
		},
		"invalid bsat": func(spec *DiscoveryDerivationPolicySpec) {
			spec.BatchSaturation.KMax = 1
		},
		"invalid point confidence": func(spec *DiscoveryDerivationPolicySpec) {
			spec.PointConfidenceLevel = 1
		},
		"invalid family-wise alpha": func(spec *DiscoveryDerivationPolicySpec) {
			spec.BatchSaturation.FamilyWiseAlpha = 0.5
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			spec := valid
			spec.PointPolicies = append([]PointPolicyEntry(nil), valid.PointPolicies...)
			mutate(&spec)
			if _, err := FreezeDiscoveryDerivationPolicy(spec); err == nil {
				t.Fatal("expected invalid policy rejection")
			}
		})
	}
}

func TestDiscoveryDerivationPolicyOwnsInputCopies(t *testing.T) {
	spec := testDiscoveryDerivationPolicySpec(t)
	policy, err := FreezeDiscoveryDerivationPolicy(spec)
	if err != nil {
		t.Fatal(err)
	}
	frozen := policy.spec()
	spec.PointPolicies[0].MinimumTrials = 99
	if !reflect.DeepEqual(policy.spec(), frozen) {
		t.Fatal("caller mutation changed frozen discovery policy")
	}
	if _, err := policy.CanonicalJSON(); err != nil {
		t.Fatal(err)
	}
}

func TestFrozenDerivationTypesRejectZeroValues(t *testing.T) {
	if _, err := (CurveFamilyContract{}).CanonicalJSON(); err == nil {
		t.Fatal("expected zero curve-family contract rejection")
	}
	if _, err := (DiscoveryDerivationPolicy{}).BSatPolicy(); err == nil {
		t.Fatal("expected zero discovery policy rejection")
	}
	if _, err := (ConfirmatoryDerivationPolicy{}).BSatPolicy(); err == nil {
		t.Fatal("expected zero confirmatory policy rejection")
	}
}

func testDiscoveryDerivationPolicySpec(t *testing.T) DiscoveryDerivationPolicySpec {
	t.Helper()
	contract, _ := testVerifiedFamily(t, 4)
	return testDiscoveryDerivationPolicySpecForDigest(contract.Digest())
}

func testDiscoveryDerivationPolicySpecForDigest(contractDigest string) DiscoveryDerivationPolicySpec {
	return DiscoveryDerivationPolicySpec{
		Name:                      "e1-batch-saturation",
		CurveFamilyContractDigest: contractDigest,
		PointConfidenceLevel:      0.95,
		PointPolicies: []PointPolicyEntry{
			{Name: "idle-baseline", Stage: PointPolicyStageIdleBaseline, MinimumTrials: 3},
			{Name: "incremental-point", Stage: PointPolicyStageIncrementalPoint, MinimumTrials: 3},
		},
		BatchSaturation: BSatPolicyEntry{
			Name: "batch-saturation", KMax: 4, RelativeImprovementTolerance: .05,
			DiscoveryTrials: 3, MaximumConfirmatoryTrials: 6, MaximumActiveRunBudget: 36,
			FamilyWiseAlpha: 0.05,
		},
	}
}
