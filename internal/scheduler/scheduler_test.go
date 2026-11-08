package scheduler_test

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler"
	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
	"github.com/lab-paper-code/chill/internal/scheduler/v1"
	v1 "github.com/lab-paper-code/chill/internal/scheduler/v1"
	"github.com/lab-paper-code/chill/internal/scheduler/v2"
	v2 "github.com/lab-paper-code/chill/internal/scheduler/v2"
)

type nativeResult struct {
	choice  []int
	joules  float64
	payload *nativeExact
}

type nativeExact struct{ reject bool }

func (*nativeExact) ResetObservations() {}
func (e *nativeExact) Evaluate(choice []int) nativeResult {
	joules, all := 0.0, true
	for _, j := range choice {
		all = all && j == 1
		joules += float64(j)
	}
	if all {
		joules = -10
	}
	return nativeResult{choice: choice, joules: joules, payload: e}
}
func (e *nativeExact) Bounded([]int) bool       { return !e.reject }
func (e *nativeExact) Admits(nativeResult) bool { return !e.reject }
func (*nativeExact) Better(a nativeResult, b *nativeResult) bool {
	return b == nil || a.joules < b.joules || a.joules == b.joules && slices.Compare(a.choice, b.choice) < 0
}
func (*nativeExact) Point(r nativeResult) protocol.Point {
	return protocol.Point{Joules: r.joules, Period: 1, Feasible: true, Final: true}
}
func (*nativeExact) Choice(r nativeResult) []int            { return r.choice }
func (*nativeExact) SeedKeep(nativeResult)                  {}
func (*nativeExact) Observe(nativeResult, int)              {}
func (*nativeExact) PostFilter(r nativeResult) nativeResult { return r }
func (*nativeExact) FilterKept(r nativeResult) bool {
	return slices.IndexFunc(r.choice, func(j int) bool { return j != 0 }) < 0
}

var _ protocol.Engine[nativeResult] = v1.Engine[nativeResult]{}
var _ protocol.Engine[nativeResult] = v2.Engine[nativeResult]{}

func TestPublicV1WorkerOrderAndDeadline(t *testing.T) {
	p := protocol.Problem{Context: context.Background(), Deadline: time.Now().Add(time.Hour)}
	for range 3 {
		p.Plans = append(p.Plans, []protocol.Plan{{Eligible: true}, {Eligible: true}, {Eligible: true, Empty: true}})
	}
	for _, workers := range []int{1, 3} {
		p.Workers = workers
		exact := &nativeExact{}
		got, err := (v1.Engine[nativeResult]{}).Search(p, exact)
		if err != nil || !got.Found || !reflect.DeepEqual(got.Value.choice, []int{1, 1, 1}) || got.Value.payload != exact {
			t.Fatalf("workers=%d result=%+v err=%v", workers, got, err)
		}
		if got.Diagnostics.Neighborhood != 3 || got.Diagnostics.Evaluated == 0 || got.Diagnostics.TerminationReason != protocol.NoImprovement {
			t.Fatalf("search diagnostics: %+v", got.Diagnostics)
		}
	}
	p.Deadline = time.Now().Add(-time.Second)
	got, err := (v1.Engine[nativeResult]{}).Search(p, &nativeExact{})
	if err != nil || !got.Diagnostics.BudgetCut || got.Diagnostics.Evaluated != 0 || got.Diagnostics.TerminationReason != protocol.BudgetCut {
		t.Fatalf("expired search: %+v %v", got.Diagnostics, err)
	}
}

func TestRootOwnsV2FallbackAndNativeResult(t *testing.T) {
	p := protocol.Problem{Context: context.Background(), Workers: 1, Deadline: time.Now().Add(-time.Second), Plans: [][]protocol.Plan{{{Eligible: true}}}}
	p.Table = &protocol.Table{ByPlan: [][]int{{0}}, Index: []int{0}, Options: []string{"keep"}, Modes: []string{"mode"}, PlanKeys: []string{"keep"}, KeyParts: []string{"n=keep"}, NodeUID: []string{"n"}, Count: [][]int{{}}, Kappa: [][]float64{{}}, Keep: []bool{true}, Searchable: []bool{true}}
	p.NewState = func(choice []int) protocol.State {
		return &oneNodeState{choice: slices.Clone(choice), result: make([]int, len(choice)), table: p.Table}
	}
	exact := &nativeExact{reject: true}
	got, err := scheduler.Select[nativeResult](protocol.V2).Search(p, exact)
	if err != nil || !got.Found || got.Value.payload != exact || !slices.Equal(got.Value.choice, []int{0}) || got.Diagnostics.Strategy != protocol.V2 || got.Diagnostics.Fallback != protocol.V1 {
		t.Fatalf("fallback lost native result: %+v err=%v", got, err)
	}
	if got.Diagnostics.TerminationReason != protocol.Deadline || !got.Diagnostics.BudgetCut {
		t.Fatalf("fallback lost v2 termination: %+v", got.Diagnostics)
	}
	if scheduler.Select[nativeResult]("unknown") != nil {
		t.Fatal("unknown scheduler selected a default")
	}
}

type oneNodeState struct {
	choice, result []int
	table          *protocol.Table
	route          [1]float64
}

func (s *oneNodeState) Choice() []int      { return s.choice }
func (s *oneNodeState) Reset(choice []int) { copy(s.choice, choice) }
func (s *oneNodeState) Apply(n, q int)     { s.choice[n] = q }
func (s *oneNodeState) Delta(n, q int) protocol.Rank {
	copy(s.result, s.choice)
	s.result[n] = q
	return protocol.Rank{Choice: s.result, Table: s.table, Feasible: true, Final: true, Bounded: true, Searchable: true}
}
func (*oneNodeState) MoveTerms(int, int, int) (protocol.Workload, float64, float64, float64) {
	return protocol.Workload{}, 0, 0, 0
}
func (s *oneNodeState) RouteTimes() []float64 { return s.route[:] }
func (*oneNodeState) Overloaded() bool        { return false }
