package profiler

import (
	"slices"
	"testing"

	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestPlannedBatchSizesUsesResolvedCapability(t *testing.T) {
	for _, test := range []struct {
		name     string
		cap      int32
		declared []int32
		want     []int
	}{
		{name: "non power cap", cap: 13, want: []int{1, 2, 4, 8}},
		{name: "sparse finite capability", cap: 16, declared: []int32{16, 1, 3, 4, 32}, want: []int{1, 4, 16}},
		{name: "one", cap: 1, want: []int{1}},
		{name: "requires one", cap: 16, declared: []int32{2, 4}},
		{name: "requires positive cap"},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := profilingrun.CandidateReport{Firstfloor: &modelcompat.FirstfloorSettings{MaxBatchSize: test.cap},
				ArtifactCapability: &profilingrun.CandidateArtifactCapability{DeclaredBatchSizes: test.declared}}
			got, err := PlannedBatchSizes(report)
			if test.want == nil {
				if err == nil {
					t.Fatal("unsupported capability accepted")
				}
				return
			}
			if err != nil || !slices.Equal(got, test.want) {
				t.Fatalf("batches=%v error=%v, want %v", got, err, test.want)
			}
		})
	}
}
