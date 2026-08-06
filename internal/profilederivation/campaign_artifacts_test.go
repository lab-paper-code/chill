package profilederivation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommittedCurveFamilyArtifact(t *testing.T) {
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
}
