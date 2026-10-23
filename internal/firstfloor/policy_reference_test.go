package firstfloor

import (
	"math/rand/v2"
	"sort"
	"strconv"
	"testing"

	"gonum.org/v1/gonum/stat"
)

// TestPolicyFastPathsMatchDeployed checks serviceCost against gonum Quantile
// and compatibleCount against the nextCompatible chain.
func TestPolicyFastPathsMatchDeployed(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	phases := []string{"", phaseQueued, phaseQueued, phaseDeferred, phaseRunning, phaseFinished}
	for c := range 20000 {
		samples := make([]float64, 1+rng.IntN(40))
		for i := range samples {
			samples[i] = float64(1 + rng.IntN(80))
		}
		sort.Float64s(samples)
		for _, p := range []float64{0, 1, 50, 90, 95, 99, 100} {
			if got, want := serviceCost(BatchProfile{ServiceSamplesMS: samples}, p), stat.Quantile(p/100, stat.Empirical, samples, nil); got != want {
				t.Fatalf("case %d p%g: serviceCost %g, Quantile %g", c, p, got, want)
			}
		}
		requests := make([]RequestState, 1+rng.IntN(12))
		for i := range requests {
			w := strconv.Itoa(rng.IntN(3))
			requests[i] = RequestState{Context: RequestContext{RequestID: "r" + strconv.Itoa(i), WorkloadUID: w, Definition: w + "/" + strconv.Itoa(rng.IntN(2)), SLO: SLO{Percentile: 95, TargetMS: float64(rng.IntN(150))}}, Phase: phases[rng.IntN(len(phases))], ElapsedMS: float64(rng.IntN(60))}
		}
		for h, r := range requests {
			if !normalRequest(r) && r.Phase != phaseDeferred {
				continue
			}
			want := 0
			for i := nextCompatible(requests, h, -1); i >= 0; i = nextCompatible(requests, h, i) {
				want++
			}
			if got := compatibleCount(requests, h); got != want {
				t.Fatalf("case %d head %d: compatibleCount %d, chain %d", c, h, got, want)
			}
		}
	}
}
