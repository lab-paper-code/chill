package profilederivation

import (
	"bytes"
	"strings"
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
	if policy.Digest() != second.Digest() || !strings.HasPrefix(policy.Digest(), "sha256:") {
		t.Fatalf("point-policy order changed policy identity: %q != %q", policy.Digest(), second.Digest())
	}
	canonical, err := policy.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDiscoveryDerivationPolicy(append([]byte(" \n"), canonical...))
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := decoded.CanonicalJSON()
	if err != nil || decoded.Digest() != policy.Digest() || !bytes.Equal(canonical, roundTrip) {
		t.Fatalf("canonical round trip drifted: %q %q %v", policy.Digest(), decoded.Digest(), err)
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
	if idle.Version != policy.Digest() || idle.MinimumTrials != 3 ||
		active.Version != policy.Digest() || active.MinimumTrials != 4 ||
		bsat.Version != policy.Digest() || bsat.KMax != 4 ||
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
		"invalid curve digest": func(spec *DiscoveryDerivationPolicySpec) {
			spec.CurveFamilyContractDigest = "sha256:" + strings.Repeat("a", 64)
		},
		"invalid bsat": func(spec *DiscoveryDerivationPolicySpec) {
			spec.BatchSaturation.KMax = 1
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
	digest := policy.Digest()
	spec.PointPolicies[0].MinimumTrials = 99
	if policy.Digest() != digest {
		t.Fatal("caller mutation changed frozen discovery policy")
	}
	if _, err := policy.CanonicalJSON(); err != nil {
		t.Fatal(err)
	}
}

func testDiscoveryDerivationPolicySpec(t *testing.T) DiscoveryDerivationPolicySpec {
	t.Helper()
	contract, _ := testVerifiedFamily(t, 4)
	return DiscoveryDerivationPolicySpec{
		Name:                      "e1-batch-saturation",
		CurveFamilyContractDigest: contract.Digest(),
		PointPolicies: []PointPolicyEntry{
			{Name: "idle-baseline", Stage: PointPolicyStageIdleBaseline, MinimumTrials: 3},
			{Name: "incremental-point", Stage: PointPolicyStageIncrementalPoint, MinimumTrials: 4},
		},
		BatchSaturation: BSatPolicyEntry{
			Name: "batch-saturation", KMax: 4, RelativeImprovementTolerance: .05,
			DiscoveryTrials: 3, MaximumConfirmatoryTrials: 6, MaximumActiveRunBudget: 36,
		},
	}
}
