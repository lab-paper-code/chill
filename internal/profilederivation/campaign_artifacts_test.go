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
	members := contract.Members()
	if contract.Shared().LogicalModel == "" || len(members) != 2 ||
		members[0].Batch != 1 || members[1].Batch != 2 {
		t.Fatalf("unexpected committed curve-family contract: %+v %+v", contract.Shared(), members)
	}
}
