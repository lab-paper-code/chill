package profilederivation

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestConfirmatoryDerivationPolicyRoundTrip(t *testing.T) {
	discovery, sizing := testSizedDiscoveryPolicy(t)
	policy, err := FreezeConfirmatoryDerivationPolicy(discovery, sizing)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := policy.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeConfirmatoryDerivationPolicy(
		append([]byte("\n "), canonical...), discovery, sizing,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.record, policy.record) {
		t.Fatalf("confirmatory policy round trip drifted: %#v", decoded.record)
	}
	bsat, err := decoded.BSatPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if decoded.CurveFamilyName() != discovery.CurveFamilyName() ||
		decoded.CandidateBatch() != 3 || decoded.ConfirmatoryTrials() == nil ||
		*decoded.ConfirmatoryTrials() != 3 ||
		!slices.Equal(decoded.ConfirmatoryBatches(), []int{3, 4}) ||
		bsat.Version != discovery.spec().BatchSaturation.Name {
		t.Fatalf("confirmatory policy lost its frozen allocation: %+v", decoded.record)
	}
}

func TestFreezeConfirmatoryDerivationPolicyAcceptsSparseFamilyBudget(t *testing.T) {
	curve := testSparseBelowCandidateDiscoveryCurve(t)
	spec := testDiscoveryDerivationPolicySpecForFamily(curve.FamilyName())
	spec.PointPolicies[0].Name = "idle-point-v1"
	spec.PointPolicies[1].Name = "sparse-below-candidate-point"
	spec.BatchSaturation.KMax = 6
	spec.BatchSaturation.MaximumActiveRunBudget = 24
	discovery, err := FreezeDiscoveryDerivationPolicy(spec)
	if err != nil {
		t.Fatal(err)
	}
	sizing, err := DeriveBSatConfirmatorySizing(curve, 4, discovery)
	if err != nil {
		t.Fatal(err)
	}
	if sizing.ConfirmatoryTrials == nil || *sizing.ConfirmatoryTrials != 3 ||
		sizing.ProjectedTotalActiveRuns == nil || *sizing.ProjectedTotalActiveRuns != 24 {
		t.Fatalf("unexpected sparse-family sizing: %+v", sizing)
	}

	if _, err := FreezeConfirmatoryDerivationPolicy(discovery, sizing); err != nil {
		t.Fatalf("freeze sparse-family confirmatory policy: %v", err)
	}
}

func TestConfirmatoryDerivationPolicyRejectsDrift(t *testing.T) {
	discovery, sizing := testSizedDiscoveryPolicy(t)
	policy, err := FreezeConfirmatoryDerivationPolicy(discovery, sizing)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := policy.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"unknown field":    []byte(strings.Replace(string(canonical), `"confirmation":`, `"unknown":1,"confirmation":`, 1)),
		"trailing value":   append(append([]byte(nil), canonical...), []byte(` []`)...),
		"scientific field": []byte(strings.Replace(string(canonical), `"kmax":4`, `"kmax":5`, 1)),
		"candidate":        []byte(strings.Replace(string(canonical), `"candidateBatch":3`, `"candidateBatch":2`, 1)),
		"trial count":      []byte(strings.Replace(string(canonical), `"confirmatoryTrials":3`, `"confirmatoryTrials":4`, 1)),
		"batch allocation": []byte(strings.Replace(string(canonical), `"batch":4`, `"batch":5`, 1)),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeConfirmatoryDerivationPolicy(payload, discovery, sizing); err == nil {
				t.Fatal("expected confirmatory policy drift rejection")
			}
		})
	}
}

func TestFreezeConfirmatoryDerivationPolicyRetainsBudgetInsufficientSizing(t *testing.T) {
	discovery, sizing := testSizedDiscoveryPolicy(t)
	sizing.Status = BSatUnavailable
	sizing.Code = BSatCodeConfirmatoryBudgetInsufficient
	sizing.ConfirmatoryTrials = nil
	policy, err := FreezeConfirmatoryDerivationPolicy(discovery, sizing)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := policy.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeConfirmatoryDerivationPolicy(canonical, discovery, sizing)
	if err != nil {
		t.Fatal(err)
	}
	if policy.ConfirmatoryTrials() != nil || len(policy.ConfirmatoryBatches()) != 0 ||
		decoded.record.Confirmation.Status != BSatUnavailable ||
		decoded.record.Confirmation.Code != BSatCodeConfirmatoryBudgetInsufficient {
		t.Fatalf("budget-insufficient policy has a Run allocation: %+v", policy.record.Confirmation)
	}
}

func testSizedDiscoveryPolicy(t *testing.T) (DiscoveryDerivationPolicy, BSatSizingResult) {
	t.Helper()
	curve := testDiscoveryCurve(t, []float64{1, .97, .94, .90})
	spec := testDiscoveryDerivationPolicySpecForFamily(curve.FamilyName())
	discovery, err := FreezeDiscoveryDerivationPolicy(spec)
	if err != nil {
		t.Fatal(err)
	}
	bsat, err := discovery.BSatPolicy()
	if err != nil {
		t.Fatal(err)
	}
	sizing, err := deriveBSatConfirmatorySizing(curve, 3, bsat)
	if err != nil {
		t.Fatal(err)
	}
	return discovery, sizing
}
