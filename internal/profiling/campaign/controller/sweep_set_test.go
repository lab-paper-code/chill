package controller

import (
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/profiling/run"
)

// Kubernetes forbids uniqueItems on an array schema, so the CRD checks ascending
// order only. Strict increase is enforced here, and a duplicate batch would
// otherwise measure the same point twice and corrupt the recorded sweep.
func TestResolvePlannedBatchSizesRejectsARepeatedBatch(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		campaign []int32
		want     string
	}{
		{name: "repeated", campaign: []int32{1, 2, 2, 4}, want: "strictly increasing"},
		{name: "descending", campaign: []int32{4, 2}, want: "strictly increasing"},
		{name: "absent for a canonical artifact", campaign: nil, want: "are required"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := resolvePlannedBatchSizes(testCase.campaign, nil); err == nil ||
				!strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want one containing %q", err, testCase.want)
			}
		})
	}
}

func TestResolvePlannedBatchSizesKeepsADeclaredSweep(t *testing.T) {
	planned, err := resolvePlannedBatchSizes([]int32{1, 2, 4, 8, 16}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(planned) != 5 || planned[0] != 1 || planned[4] != 16 {
		t.Fatalf("planned = %v, want the declared sweep unchanged", planned)
	}
}

// A derived artifact already constrains its own set, so exactly one source
// applies and the two are never merged.
func TestResolvePlannedBatchSizesRefusesTwoSources(t *testing.T) {
	capability := &profilingrun.CandidateArtifactCapability{DeclaredBatchSizes: []int32{1, 2}}
	if _, err := resolvePlannedBatchSizes([]int32{1, 2, 4}, capability); err == nil {
		t.Fatal("a campaign-declared sweep was accepted against a declaring artifact")
	}
	planned, err := resolvePlannedBatchSizes(nil, capability)
	if err != nil {
		t.Fatal(err)
	}
	if len(planned) != 2 {
		t.Fatalf("planned = %v, want the artifact's declared set", planned)
	}
}
