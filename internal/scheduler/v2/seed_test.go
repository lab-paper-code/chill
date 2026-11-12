package v2

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

type seedAdmissionExact struct {
	finiteExact
	admitted   map[int]bool
	observed   []int
	admissions int
}

func (e *seedAdmissionExact) Admits(r finiteExactResult) bool {
	e.admissions++
	return e.admitted[r.choice[0]]
}
func (e *seedAdmissionExact) Observe(r finiteExactResult, _ int) {
	e.observed = append(e.observed, r.choice[0])
}

func TestFunnelAdmittedKeepSeedPreventsFallback(t *testing.T) {
	for _, postfilter := range []bool{false, true} {
		t.Run(fmt.Sprint(postfilter), func(t *testing.T) {
			p := finiteProblem(1, []protocol.Workload{{Rate: 1, TargetNanos: 1e9}}, [][]float64{{10}, {20}, {30}}, [][]float64{{1e6}, {1e6}, {1e6}}, []float64{30, 20, 10}, 0, nil)
			p.Evaluations, p.Finalists, p.Workers, p.PostFilter = 100, 1, 1, postfilter
			candidates, _ := Candidates(p, p.Table, p.Evaluations, p.Finalists, p.Active, time.Time{})
			for _, candidate := range candidates {
				if candidate.Choice[0] == 0 {
					t.Fatal("fixture shortlist must omit keep")
				}
			}
			exact := &seedAdmissionExact{finiteExact: finiteExact{results: []finiteExactResult{{joules: 30}, {joules: 20}, {joules: 10}}}, admitted: map[int]bool{0: true}}
			got, err := (Engine[finiteExactResult]{}).Search(p, exact)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Found || got.Value.choice[0] != 0 || got.Diagnostics.Fallback != "" || exact.evaluated != 1+len(candidates) || got.Diagnostics.ExactEvaluated != exact.evaluated {
				t.Fatalf("keep seed lost: result=%+v calls=%d candidates=%d", got, exact.evaluated, len(candidates))
			}
			if len(exact.observed) == 0 || exact.observed[0] != 0 {
				t.Fatalf("keep did not reduce first: %v", exact.observed)
			}
			wantFilters := 0
			if postfilter {
				wantFilters = 1
			}
			if exact.filtered != wantFilters {
				t.Fatalf("seed postfilters=%d want=%d", exact.filtered, wantFilters)
			}
		})
	}
}

func TestFunnelNonAdmittedKeepSeedRetainsFallback(t *testing.T) {
	p := finiteProblem(1, []protocol.Workload{{Rate: 1, TargetNanos: 1e9}}, [][]float64{{10}, {20}, {30}}, [][]float64{{1e6}, {1e6}, {1e6}}, []float64{30, 20, 10}, 0, nil)
	p.Evaluations, p.Finalists, p.Workers = 100, 1, 1
	exact := &seedAdmissionExact{finiteExact: finiteExact{results: []finiteExactResult{{joules: 30}, {joules: 20}, {joules: 10}}}, admitted: map[int]bool{1: true}}
	got, err := (Engine[finiteExactResult]{}).Search(p, exact)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.Value.choice[0] != 1 || got.Diagnostics.Fallback != "exactK1" {
		t.Fatalf("non-admitted seed blocked fallback: %+v", got)
	}
}

type cancellingSeedExact struct {
	*seedAdmissionExact
	cancel context.CancelFunc
}

func (e cancellingSeedExact) Evaluate(choice []int) finiteExactResult {
	e.cancel()
	return e.seedAdmissionExact.Evaluate(choice)
}

func TestFunnelCancelledKeepSeedDoesNotReduce(t *testing.T) {
	for _, duringReplay := range []bool{false, true} {
		t.Run(fmt.Sprint("duringReplay=", duringReplay), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			exact := &seedAdmissionExact{
				finiteExact: finiteExact{results: []finiteExactResult{{joules: 30}, {joules: 20}}},
				admitted:    map[int]bool{0: true, 1: true},
			}
			var port protocol.Exact[finiteExactResult] = exact
			var candidates []protocol.Candidate
			if duringReplay {
				port = cancellingSeedExact{exact, cancel}
				candidates = []protocol.Candidate{{Choice: []int{1}}}
			} else {
				cancel()
			}
			seed := finiteExactResult{choice: []int{0}, joules: 30}
			stats := FunnelStats{}
			got := Funnel(protocol.Problem{Context: ctx, Workers: 1, PostFilter: true}, port, candidates, &stats, &seed)
			if got != nil || stats != (FunnelStats{}) || len(exact.observed) != 0 || exact.admissions != 0 || exact.filtered != 0 {
				t.Fatalf("cancelled seed reduced: result=%+v stats=%+v observed=%v admissions=%d filters=%d", got, stats, exact.observed, exact.admissions, exact.filtered)
			}
		})
	}
}
