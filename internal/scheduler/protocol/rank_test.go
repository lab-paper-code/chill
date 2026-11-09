package protocol

import (
	"cmp"
	"slices"
	"testing"
)

func TestRankFiniteChoiceOrder(t *testing.T) {
	table := &Table{KeyParts: []string{"a", "b", "c"}}
	var ranks []Rank
	for _, feasible := range []bool{false, true} {
		for _, final := range []bool{false, true} {
			for _, excess := range [][]float64{{0.3, 0.1}, {0.9, 0}, {0.3, 0.2}} {
				for _, energy := range []float64{2, 5} {
					for _, keep := range []bool{false, true} {
						for q := range table.KeyParts {
							ranks = append(ranks, Rank{Table: table, Choice: []int{q}, Feasible: feasible, Final: final,
								Excesses: excess, Joules: energy, KeepJoules: 1, Keep: keep})
						}
					}
				}
			}
		}
	}
	for _, a := range ranks {
		for _, b := range ranks {
			order := 0
			switch {
			case a.Feasible != b.Feasible:
				if a.Feasible {
					order = -1
				} else {
					order = 1
				}
			case !a.Feasible && !slices.Equal(a.Excesses, b.Excesses):
				order = slices.Compare(a.Excesses, b.Excesses)
			case a.Joules != b.Joules:
				order = cmp.Compare(a.Joules, b.Joules)
			case a.Keep != b.Keep:
				if a.Keep {
					order = -1
				} else {
					order = 1
				}
			default:
				order = cmp.Compare(table.KeyParts[a.Choice[0]], table.KeyParts[b.Choice[0]])
			}
			if got := a.Better(&b); got != (order < 0) {
				t.Fatalf("Better(%+v, %+v)=%v, order=%d", a, b, got, order)
			}
		}
	}
}

func TestRankPreservesOrderedWorkloadExcesses(t *testing.T) {
	table := &Table{KeyParts: []string{"p", "q"}}
	p := Rank{Table: table, Choice: []int{0}, Final: true, Excesses: []float64{0.6, 0.1}}
	q := Rank{Table: table, Choice: []int{1}, Excesses: []float64{0.4, 0.1}}
	if !q.Better(&p) {
		t.Fatal("worst-first excess order lost latency improvement")
	}
	// The adapter already applied explicit first-Workload priority, followed by
	// worst-first ordering within the unlisted Workload group.
	p.Excesses, q.Excesses = []float64{0.1, 0.6}, []float64{0.4, 0.1}
	if !p.Better(&q) {
		t.Fatal("Better reordered the adapter's explicit Workload priority")
	}
	p.Final, q.Final = false, true
	if !p.Better(&q) {
		t.Fatal("Final displaced the ordered latency comparison")
	}
	if allocations := testing.AllocsPerRun(100, func() { p.Better(&q) }); allocations != 0 {
		t.Fatalf("rank comparison allocated %g times", allocations)
	}
}
