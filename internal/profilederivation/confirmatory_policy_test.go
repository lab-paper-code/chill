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
	if decoded.DiscoveryPolicyDigest() != discovery.Digest() ||
		decoded.CurveFamilyContractDigest() != discovery.CurveFamilyContractDigest() ||
		decoded.CandidateBatch() != 3 || decoded.ConfirmatoryTrials() == nil ||
		*decoded.ConfirmatoryTrials() != 3 ||
		!slices.Equal(decoded.ConfirmatoryBatches(), []int{3, 4}) ||
		bsat.Version != discovery.Digest() {
		t.Fatalf("confirmatory policy lost its frozen allocation: %+v", decoded.record)
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
		"discovery digest": replaceJSONString(t, canonical, discovery.Digest(), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
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
	spec := testDiscoveryDerivationPolicySpecForDigest(curve.ContractDigest())
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

func replaceJSONString(t *testing.T, payload []byte, from, to string) []byte {
	t.Helper()
	needle := `"` + from + `"`
	if !strings.Contains(string(payload), needle) {
		t.Fatalf("fixture did not contain %q", from)
	}
	return []byte(strings.Replace(string(payload), needle, `"`+to+`"`, 1))
}
