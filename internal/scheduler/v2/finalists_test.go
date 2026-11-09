package v2

import (
	"testing"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

func TestCapacityV2FinalistsDistinctAndOrdered(t *testing.T) {
	tab := &protocol.Table{ByPlan: [][]int{{0, 1, 2}}, KeyParts: []string{"n=a", "n=b", "n=c"}}
	f := newV2Finalists(tab, 0, 2)
	key := newV2Key(tab, 0)
	for _, q := range []int{2, 1, 1, 0, 2} {
		key.Choice[0], key.Joules = q, float64(q)
		f.add(key, "keep")
	}
	if f.used != 2 || f.ordered[0].key.Choice[0] != 0 || f.ordered[1].key.Choice[0] != 1 {
		t.Fatalf("bad finalist set: %+v", f)
	}
}

func TestMovesRetainEverySearchableRoutingVariant(t *testing.T) {
	tab := &protocol.Table{
		ByPlan: [][]int{{0, 1, 2, 3, 4}}, Index: []int{0, 1, 2, 3, 4},
		NodeUID: []string{"n"}, Options: []string{"keep", "option", "option", "option", "option"},
		Modes: []string{"m", "m", "m", "m", "m"}, PlanKeys: []string{"keep", "a", "b", "c", "d"},
		KeyParts: []string{"n=keep", "n=a", "n=b", "n=c", "n=d"},
		Searchable: []bool{true, true, true, true, false},
		Count: [][]int{{1}, {1}, {1}, {1}, {1}},
		RoutingRate: [][]float64{{5}, {10}, {50}, {25}, {100}},
	}
	Group(tab)
	// Starting inside an option group must not hide its other cap variants.
	c := &v2Search{s: &state{Table: tab, Choice: []int{1}}}
	moves := c.movePlans(0, nil)
	for _, q := range []int{0, 2, 3} {
		found := false
		for _, move := range moves {
			found = found || move == q
		}
		if !found {
			t.Fatalf("searchable routing variant %d disappeared: %v", q, moves)
		}
	}
	if len(moves) != 3 {
		t.Fatalf("unexpected current or physically ineligible move: %v", moves)
	}
}
