package profilederivation

import (
	"bytes"
	"slices"
	"testing"
)

func TestConfirmatoryDerivationPolicyCanonicalRoundTrip(t *testing.T) {
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
	roundTrip, err := decoded.CanonicalJSON()
	if err != nil || !bytes.Equal(canonical, roundTrip) || decoded.Digest() != policy.Digest() {
		t.Fatalf("confirmatory policy round trip drifted: %q %q %v", policy.Digest(), decoded.Digest(), err)
	}
	bsat, err := decoded.BSatPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if decoded.DiscoveryPolicyDigest() != discovery.Digest() ||
		decoded.CurveFamilyContractDigest() != discovery.CurveFamilyContractDigest() ||
		decoded.CandidateBatch() != 3 || decoded.ConfirmatoryTrials() != 3 ||
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
		"unknown field":    bytes.Replace(canonical, []byte(`"confirmation":`), []byte(`"unknown":1,"confirmation":`), 1),
		"trailing value":   append(append([]byte(nil), canonical...), []byte(` []`)...),
		"discovery digest": replaceJSONIntOrString(t, canonical, discovery.Digest(), "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		"scientific field": bytes.Replace(canonical, []byte(`"kmax":4`), []byte(`"kmax":5`), 1),
		"candidate":        bytes.Replace(canonical, []byte(`"candidateBatch":3`), []byte(`"candidateBatch":2`), 1),
		"trial count":      bytes.Replace(canonical, []byte(`"confirmatoryTrials":3`), []byte(`"confirmatoryTrials":4`), 1),
		"batch allocation": bytes.Replace(canonical, []byte(`"batch":4`), []byte(`"batch":5`), 1),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeConfirmatoryDerivationPolicy(payload, discovery, sizing); err == nil {
				t.Fatal("expected confirmatory policy drift rejection")
			}
		})
	}
}

func TestFreezeConfirmatoryDerivationPolicyRejectsUnavailableSizing(t *testing.T) {
	discovery, sizing := testSizedDiscoveryPolicy(t)
	sizing.Status = BSatUnavailable
	sizing.Code = BSatCodeConfirmatoryBudgetInsufficient
	sizing.ConfirmatoryTrials = nil
	if _, err := FreezeConfirmatoryDerivationPolicy(discovery, sizing); err == nil {
		t.Fatal("expected unavailable sizing rejection")
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
	sizing, err := DeriveBSatConfirmatorySizing(curve, 3, bsat)
	if err != nil {
		t.Fatal(err)
	}
	return discovery, sizing
}

func replaceJSONIntOrString(t *testing.T, payload []byte, from, to string) []byte {
	t.Helper()
	result := bytes.Replace(payload, []byte(`"`+from+`"`), []byte(`"`+to+`"`), 1)
	if bytes.Equal(result, payload) {
		t.Fatalf("fixture did not contain %q", from)
	}
	return result
}
