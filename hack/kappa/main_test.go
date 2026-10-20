package main

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
)

// TestSolveMatchesBruteForce checks the route-time DP against enumerating
// every plan combination on random fixtures (request energy zero): the least
// transition backlog first, then the least energy, and keep on ties.
func TestSolveMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := range 600 {
		nodes := make([][]*plan, 1+rng.IntN(4))
		for n := range nodes {
			for j := range 1 + rng.IntN(4) {
				p := &plan{keep: j == 0, before: 1 + 10*rng.Float64()}
				if rng.IntN(3) > 0 {
					p.instances, p.kappa = []serving.SchedulerInstance{{}}, 10*rng.Float64()
				}
				if j > 0 {
					p.pre = op{mean: 60 * rng.Float64(), joules: 100 * rng.Float64()}
					p.post = op{mean: 5 * rng.Float64(), joules: 10 * rng.Float64()}
					p.mid, p.after = 1+10*rng.Float64(), 1+10*rng.Float64()
				}
				nodes[n] = append(nodes[n], p)
			}
		}
		load, period := []float64{0, 3, 8, 15}[trial%4], 80.0
		keepKappa := 0.0
		for _, ps := range nodes {
			keepKappa += ps[0].kappa
		}
		feasible := func(index []int) bool {
			units := 0
			for n, j := range index {
				if p := nodes[n][j]; len(p.instances) > 0 && p.kappa > 0 {
					if load == 0 {
						return true
					}
					units += min(50, int(p.kappa/(load/50)))
				}
			}
			return load > 0 && units >= 50
		}
		cost := func(index []int) (float64, float64) {
			route, e := 0.0, 0.0
			for n, j := range index {
				route = max(route, nodes[n][j].pre.mean)
			}
			for n, j := range index {
				e += nodes[n][j].energy(route, period)
			}
			return max(0, load-keepKappa) * route, e
		}
		wantBacklog, want := math.Inf(1), math.Inf(1)
		index := make([]int, len(nodes))
		for {
			if feasible(index) {
				if b, e := cost(index); b < wantBacklog-1e-9 || b < wantBacklog+1e-9 && e < want {
					wantBacklog, want = b, e
				}
			}
			n := 0
			for ; n < len(index); n++ {
				if index[n]++; index[n] < len(nodes[n]) {
					break
				}
				index[n] = 0
			}
			if n == len(index) {
				break
			}
		}
		picks, joules, _ := solve(nodes, load, period)
		if math.IsInf(want, 1) != (picks == nil) || picks != nil && math.Abs(joules-want) > 1e-6 {
			t.Fatalf("trial %d: solve %v %.6f, brute force %.6f", trial, picks, joules, want)
		}
		keep := make([]int, len(nodes))
		if _, keepJ := cost(keep); feasible(keep) && keepJ <= want+1e-6 && joules != keepJ {
			t.Fatalf("trial %d: keep is optimal but solve picked %v", trial, picks)
		}
	}
}
