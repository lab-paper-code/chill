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
	const wantDigest = "sha256:4e1ae5f3574e76d7b1f475141ce26e2267e77b696060c971de61ad9cf6c6bd6b"
	const wantCanonical = `{"schemaVersion":"chill.dacs.io/confirmatory-derivation-policy.v1alpha2","name":"e1-batch-saturation","curveFamilyContractDigest":"chill.dacs.io/curve-family-contract.v1alpha1@sha256:08db9e76e50ecb6a20b588afc00dbfee4eec73780dd9d3e7345787ef23c10b0b","pointConfidenceLevel":0.95,"pointPolicies":[{"name":"idle-baseline","stage":"idle-baseline","minimumTrials":3},{"name":"incremental-point","stage":"incremental-point","minimumTrials":3}],"batchSaturation":{"name":"batch-saturation","kmax":4,"relativeImprovementTolerance":0.05,"discoveryTrials":3,"maximumConfirmatoryTrials":6,"maximumActiveRunBudget":36,"familyWiseAlpha":0.05},"discoveryPolicyDigest":"sha256:3b602899a8276671012455c123c3ef741f4defdcb36fd63ff45df7eca3eef1e0","confirmation":{"status":"CandidateOnly","code":"ConfirmatoryTrialsSized","candidateBatch":3,"confirmatoryTrials":3,"batches":[{"batch":3,"trials":3},{"batch":4,"trials":3}]}}`
	if policy.Digest() != wantDigest || string(canonical) != wantCanonical {
		t.Fatalf("confirmatory policy golden drifted: %s\n%s", policy.Digest(), canonical)
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

func replaceJSONIntOrString(t *testing.T, payload []byte, from, to string) []byte {
	t.Helper()
	result := bytes.Replace(payload, []byte(`"`+from+`"`), []byte(`"`+to+`"`), 1)
	if bytes.Equal(result, payload) {
		t.Fatalf("fixture did not contain %q", from)
	}
	return result
}
