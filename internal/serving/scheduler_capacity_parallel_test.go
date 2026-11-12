package serving

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
	v2 "github.com/lab-paper-code/chill/internal/scheduler/v2"
)

func TestCapacityFunnelParallelDiagnostics(t *testing.T) {
	for _, reserve := range []float64{0, .1} {
		for _, postfilter := range []bool{false, true} {
			t.Run(fmt.Sprintf("reserve=%g/postfilter=%v", reserve, postfilter), func(t *testing.T) {
				var baseline capacityResult
				var baselineStats v2.FunnelStats
				var baselineSearch SchedulerPlanSearch
				for _, workers := range []int{1, 3} {
					p := capacityV2MultiOptionFixture()
					p.settings.PlanSearchWorkers = workers
					p.settings.PlanPostFilterFinalists = postfilter
					p.settings.TransitionReserveFraction = reserve
					exact := newCapacityExact(p)
					exact.SeedKeep(exact.Evaluate(make([]int, len(p.plans))))
					var candidates []protocol.Candidate
					for a := range p.plans[0] {
						for b := range p.plans[1] {
							for c := range p.plans[2] {
								candidates = append(candidates, protocol.Candidate{Choice: []int{a, b, c}, Joules: float64(len(candidates)), Start: fmt.Sprint(a, b, c)})
							}
						}
					}
					// The duplicate exact result must retain the first candidate's gap/start.
					tied := candidates[0]
					tied.Joules, tied.Start = -100, "later-tie"
					candidates = append(candidates, tied)
					stats := v2.FunnelStats{}
					got := v2.Funnel(p.searchProblem(time.Time{}), exact, candidates, &stats, nil)
					if got == nil || stats.Exact != len(candidates) {
						t.Fatalf("workers=%d result=%+v stats=%+v candidates=%d", workers, got, stats, len(candidates))
					}
					exact.record(protocol.Diagnostics{Strategy: protocol.V2})
					if reserve > 0 && (p.searched.Excluded[schedulerTransitionReserveInsufficient] == 0 || len(p.searched.ReserveRejections) == 0) {
						t.Fatalf("fixture did not exercise rejected reserve observations: %+v", p.searched)
					}
					if workers == 1 {
						baseline, baselineStats, baselineSearch = *got, stats, p.searched
						continue
					}
					if got.key != baseline.key || got.joules != baseline.joules || !slices.Equal(got.choice, baseline.choice) ||
						!reflect.DeepEqual(got.feedback, baseline.feedback) || !reflect.DeepEqual(got.weights, baseline.weights) ||
						stats != baselineStats || !reflect.DeepEqual(p.searched, baselineSearch) {
						t.Fatalf("parallel result=%+v stats=%+v search=%+v sequential=%+v stats=%+v search=%+v", got, stats, p.searched, baseline, baselineStats, baselineSearch)
					}
				}
			})
		}
	}
}
