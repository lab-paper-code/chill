package v2

import (
	"slices"
	"testing"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

type finiteExactResult struct {
	choice                              []int
	failure, latency, potential, joules float64
}

type finiteExact struct {
	results             []finiteExactResult
	evaluated, filtered int
}

func (*finiteExact) ResetObservations() {}
func (e *finiteExact) Evaluate(choice []int) finiteExactResult {
	e.evaluated++
	result := e.results[choice[0]]
	result.choice = slices.Clone(choice)
	return result
}
func (*finiteExact) Bounded([]int) bool            { return true }
func (*finiteExact) Admits(finiteExactResult) bool { return true }
func (*finiteExact) Better(a finiteExactResult, b *finiteExactResult) bool {
	if b == nil {
		return true
	}
	if a.failure != b.failure {
		return a.failure < b.failure
	}
	if a.latency != b.latency {
		return a.latency < b.latency
	}
	if a.potential != b.potential {
		return a.potential < b.potential
	}
	return a.joules < b.joules
}
func (*finiteExact) Point(r finiteExactResult) protocol.Point {
	return protocol.Point{Joules: r.joules, Feasible: r.failure == 0 && r.latency == 0}
}
func (*finiteExact) Choice(r finiteExactResult) []int { return r.choice }
func (*finiteExact) SeedKeep(finiteExactResult)       {}
func (*finiteExact) Observe(finiteExactResult, int)   {}
func (e *finiteExact) PostFilter(r finiteExactResult) finiteExactResult {
	e.filtered++
	return r
}
func (*finiteExact) FilterKept(finiteExactResult) bool { return false }

func TestFunnelUsesExactOutcomesForEveryHeuristicFinalist(t *testing.T) {
	for _, postfilter := range []bool{false, true} {
		exact := &finiteExact{results: []finiteExactResult{
			{failure: 1, potential: 0.9, joules: 1},
			{failure: 1, potential: 0.3, joules: 10},
			{failure: 1, potential: 0.6, joules: 2},
		}}
		// All actual outcomes fail. Equal failures must still allow conditional
		// compute improvement, regardless of the cheap feasibility/energy labels.
		candidates := []protocol.Candidate{
			{Choice: []int{0}, Feasible: true, Joules: 1, Start: "keep"},
			{Choice: []int{1}, Feasible: false, Joules: 10, Start: "empty"},
			{Choice: []int{2}, Feasible: false, Joules: 2, Start: "priced"},
		}
		stats := FunnelStats{}
		got := Funnel(protocol.Problem{PostFilter: postfilter}, exact, candidates, &stats)
		if got == nil || got.choice[0] != 1 || exact.evaluated != 3 || stats.Exact != 3 {
			t.Fatalf("postfilter=%v result=%+v evaluated=%d stats=%+v", postfilter, got, exact.evaluated, stats)
		}
		wantFilters := 0
		if postfilter {
			wantFilters = 3
		}
		if exact.filtered != wantFilters {
			t.Fatalf("postfilter=%v called disabled/excess postfilter %d times", postfilter, exact.filtered)
		}
	}
}
