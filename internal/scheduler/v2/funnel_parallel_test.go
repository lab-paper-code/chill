package v2

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

type orderedFunnelExact struct {
	finiteExact
	calls       atomic.Int32
	observed    []int
	slots       []int
	filterOrder []int
	started     chan int
	release     chan struct{}
	thirdDone   chan struct{}
	completedMu sync.Mutex
	completed   []int
}

func newOrderedFunnelExact() *orderedFunnelExact {
	return &orderedFunnelExact{finiteExact: finiteExact{results: []finiteExactResult{
		{joules: 20}, {joules: 1}, {joules: 4}, {joules: 4},
		{joules: 1}, {joules: 0}, {joules: 0},
	}}}
}

func (e *orderedFunnelExact) Evaluate(choice []int) finiteExactResult {
	e.calls.Add(1)
	id := choice[0]
	if e.started != nil {
		e.started <- id
		<-e.release
		if id == 1 {
			<-e.thirdDone
		}
	}
	e.completedMu.Lock()
	e.completed = append(e.completed, id)
	e.completedMu.Unlock()
	if e.started != nil && id == 3 {
		close(e.thirdDone)
	}
	result := e.results[id]
	result.choice = slices.Clone(choice)
	return result
}
func (*orderedFunnelExact) Bounded(choice []int) bool { return choice[0] != 5 }
func (*orderedFunnelExact) Admits(r finiteExactResult) bool {
	return r.choice[0] != 6
}
func (*orderedFunnelExact) Point(r finiteExactResult) protocol.Point {
	return protocol.Point{Key: fmt.Sprint(r.choice[0]), Joules: r.joules, Feasible: true}
}
func (e *orderedFunnelExact) Observe(r finiteExactResult, worker int) {
	e.observed = append(e.observed, r.choice[0])
	e.slots = append(e.slots, worker)
}
func (e *orderedFunnelExact) PostFilter(r finiteExactResult) finiteExactResult {
	e.filterOrder = append(e.filterOrder, r.choice[0])
	if r.choice[0] == 1 || r.choice[0] == 4 {
		return finiteExactResult{choice: []int{0}, joules: 20}
	}
	return r
}
func (*orderedFunnelExact) FilterKept(r finiteExactResult) bool { return r.choice[0] == 0 }

func orderedFunnelCandidates() []protocol.Candidate {
	candidates := make([]protocol.Candidate, 6)
	for i := range candidates {
		id := i + 1
		candidates[i] = protocol.Candidate{Choice: []int{id}, Joules: float64(10 + id), Start: fmt.Sprint("start", id)}
	}
	return candidates
}

func awaitFunnelWorkers(t *testing.T, exact *orderedFunnelExact) {
	t.Helper()
	for range 3 {
		select {
		case <-exact.started:
		case <-time.After(5 * time.Second):
			close(exact.release)
			t.Fatal("three exact workers did not start concurrently")
		}
	}
}

func TestFunnelParallelPreservesCandidateOrder(t *testing.T) {
	for _, postfilter := range []bool{false, true} {
		t.Run(fmt.Sprint("postfilter=", postfilter), func(t *testing.T) {
			var baseline *finiteExactResult
			var baselineStats FunnelStats
			for _, workers := range []int{1, 3} {
				exact := newOrderedFunnelExact()
				problem := protocol.Problem{Context: context.Background(), Workers: workers, PostFilter: postfilter}
				stats := FunnelStats{}
				var got *finiteExactResult
				if workers == 1 {
					got = Funnel(problem, exact, orderedFunnelCandidates(), &stats, nil)
				} else {
					exact.started, exact.release, exact.thirdDone = make(chan int, 6), make(chan struct{}), make(chan struct{})
					done := make(chan *finiteExactResult, 1)
					go func() { done <- Funnel(problem, exact, orderedFunnelCandidates(), &stats, nil) }()
					awaitFunnelWorkers(t, exact)
					close(exact.release)
					select {
					case got = <-done:
					case <-time.After(5 * time.Second):
						t.Fatal("parallel funnel did not finish")
					}
					if slices.Index(exact.completed, 3) > slices.Index(exact.completed, 1) {
						t.Fatalf("fixture did not reverse replay completion: %v", exact.completed)
					}
				}
				wantID, wantGap, wantReverts := 1, 10.0, 0
				if postfilter {
					wantID, wantGap, wantReverts = 2, 8, 1
				}
				wantStats := FunnelStats{Exact: 5, Gap: wantGap, Reverts: wantReverts, Start: fmt.Sprint("start", wantID), RawKey: fmt.Sprint(wantID), RawJoules: exact.results[wantID].joules}
				if got == nil || got.choice[0] != wantID || stats != wantStats || exact.calls.Load() != 5 {
					t.Fatalf("workers=%d result=%+v stats=%+v want=%+v calls=%d", workers, got, stats, wantStats, exact.calls.Load())
				}
				if !slices.Equal(exact.observed, []int{1, 2, 3, 4, 6}) {
					t.Fatalf("workers=%d observation order=%v", workers, exact.observed)
				}
				for i, id := range exact.observed {
					if exact.slots[i] < 0 || exact.slots[i] >= workers {
						t.Fatalf("candidate=%d slot=%d workers=%d", id, exact.slots[i], workers)
					}
				}
				if postfilter && !slices.Equal(exact.filterOrder, []int{1, 2, 3, 4}) || !postfilter && len(exact.filterOrder) != 0 {
					t.Fatalf("workers=%d filter order=%v", workers, exact.filterOrder)
				}
				if workers == 1 {
					baseline, baselineStats = got, stats
				} else if !reflect.DeepEqual(got, baseline) || stats != baselineStats {
					t.Fatalf("parallel result=%+v stats=%+v sequential=%+v stats=%+v", got, stats, baseline, baselineStats)
				}
			}
		})
	}
}

func TestFunnelParallelCancellation(t *testing.T) {
	for _, postfilter := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		exact := newOrderedFunnelExact()
		exact.started, exact.release, exact.thirdDone = make(chan int, 6), make(chan struct{}), make(chan struct{})
		stats := FunnelStats{}
		done := make(chan *finiteExactResult, 1)
		go func() {
			done <- Funnel(protocol.Problem{Context: ctx, Workers: 3, PostFilter: postfilter}, exact, orderedFunnelCandidates(), &stats, nil)
		}()
		awaitFunnelWorkers(t, exact)
		cancel()
		close(exact.release)
		select {
		case got := <-done:
			if got != nil || exact.calls.Load() != 3 || len(exact.observed) != 0 || len(exact.filterOrder) != 0 {
				t.Fatalf("cancelled funnel result=%+v calls=%d observed=%v filtered=%v", got, exact.calls.Load(), exact.observed, exact.filterOrder)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled funnel did not join exact workers")
		}
	}
}

type cancelFunnelExact struct {
	*orderedFunnelExact
	cancel context.CancelFunc
}

func (e *cancelFunnelExact) Evaluate(choice []int) finiteExactResult {
	if choice[0] == 1 {
		e.cancel()
	}
	return e.orderedFunnelExact.Evaluate(choice)
}

func TestSearchCancellationDoesNotEnterExactFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := finiteProblem(1, []protocol.Workload{{Rate: 1, TargetNanos: 1}},
		[][]float64{{1}, {2}}, [][]float64{{1}, {1}}, []float64{20, 1}, 0, nil)
	p.Context, p.Workers, p.Evaluations, p.Finalists, p.PostFilter = ctx, 1, 20, 2, true
	exact := &cancelFunnelExact{orderedFunnelExact: newOrderedFunnelExact(), cancel: cancel}
	out, err := (Engine[finiteExactResult]{}).Search(p, exact)
	if !errors.Is(err, context.Canceled) || out.Found || len(exact.filterOrder) != 0 {
		t.Fatalf("cancelled search outcome=%+v err=%v filters=%v", out, err, exact.filterOrder)
	}
}

func TestFunnelCancelledBeforeReplay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, workers := range []int{1, 3} {
		for _, postfilter := range []bool{false, true} {
			exact := newOrderedFunnelExact()
			stats := FunnelStats{}
			got := Funnel(protocol.Problem{Context: ctx, Workers: workers, PostFilter: postfilter}, exact, orderedFunnelCandidates(), &stats, nil)
			if got != nil || stats != (FunnelStats{}) || exact.calls.Load() != 0 || len(exact.observed) != 0 || len(exact.filterOrder) != 0 {
				t.Fatalf("workers=%d postfilter=%v result=%+v stats=%+v calls=%d", workers, postfilter, got, stats, exact.calls.Load())
			}
		}
	}
}
