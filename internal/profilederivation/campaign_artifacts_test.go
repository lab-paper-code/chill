package profilederivation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommittedPlan5Artifacts(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "design", "profiling", "campaigns")
	contractBytes, err := os.ReadFile(filepath.Join(
		root, "rknn-mobilenet-v2-050-sequential-v1", "curve-family-contract.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := DecodeCurveFamilyContract(contractBytes)
	if err != nil {
		t.Fatal(err)
	}
	const wantContractDigest = "chill.dacs.io/curve-family-contract.v1alpha1@sha256:447c326bbdff27a3e6d750c70af714db739d27138c790c1ee9dbc5ffce4565d2"
	if contract.Digest() != wantContractDigest {
		t.Fatalf("committed curve-family digest = %q, want %q", contract.Digest(), wantContractDigest)
	}

	policyBytes, err := os.ReadFile(filepath.Join(
		root, "rknn-mobilenet-v2-050-bsat-v1", "discovery-policy.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := DecodeDiscoveryDerivationPolicy(policyBytes)
	if err != nil {
		t.Fatal(err)
	}
	if policy.CurveFamilyContractDigest() != contract.Digest() {
		t.Fatalf("discovery policy binds %q, want %q", policy.CurveFamilyContractDigest(), contract.Digest())
	}
	const wantPolicyDigest = "sha256:548bd2b91da51bdb22b5a5a126b2df13e0fc7b150535d572ab19d01d13248631"
	if policy.Digest() != wantPolicyDigest {
		t.Fatalf("committed discovery-policy digest = %q", policy.Digest())
	}

	sizing := BSatSizingResult{
		Status: BSatUnavailable, Code: BSatCodeConfirmatoryBudgetInsufficient,
		CandidateBatch: 1, PolicyVersion: policy.Digest(),
		CurveFamilyContractDigest: contract.Digest(),
	}
	confirmatoryBytes, err := os.ReadFile(filepath.Join(
		root, "rknn-mobilenet-v2-050-bsat-v1", "confirmatory-policy.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	confirmatory, err := DecodeConfirmatoryDerivationPolicy(confirmatoryBytes, policy, sizing)
	if err != nil {
		t.Fatal(err)
	}
	const wantConfirmatoryDigest = "sha256:7a914a05f41c62781fad14323f96614c32b92e1d3d6d3bf29f5dcce48ad062ee"
	if confirmatory.Digest() != wantConfirmatoryDigest {
		t.Fatalf("committed confirmatory-policy digest = %q", confirmatory.Digest())
	}
}
