package v2

import (
	"cmp"
	"slices"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

// Group builds option/mode cap groups once. Keep always has a separate entry.
func Group(t *protocol.Table) {
	if t.Entries != nil {
		return
	}
	t.Entries = []protocol.Entry{}
	for n, plans := range t.ByPlan {
		type identity struct{ option, mode string }
		entries := map[identity]int{}
		for j, q := range plans {
			if j != 0 && !t.Searchable[q] {
				continue
			}
			key := identity{t.Options[q], t.Modes[q]}
			e, found := entries[key]
			if !found || j == 0 {
				e = len(t.Entries)
				w := -1
				for k, count := range t.Count[q] {
					if count > 0 {
						w = k
					}
				}
				if j == 0 {
					w = -1
				} else {
					entries[key] = e
				}
				t.Entries = append(t.Entries, protocol.Entry{Node: n, Option: t.Options[q], Mode: t.Modes[q], Workload: w})
			}
			t.Entries[e].Variants = append(t.Entries[e].Variants, q)
		}
	}
	for e := range t.Entries {
		entry := &t.Entries[e]
		slices.SortFunc(entry.Variants, func(a, b int) int {
			ka, kb := 0.0, 0.0
			if entry.Workload >= 0 {
				ka, kb = t.Kappa[a][entry.Workload], t.Kappa[b][entry.Workload]
			}
			return cmp.Or(cmp.Compare(ka, kb), cmp.Compare(t.PlanKeys[a], t.PlanKeys[b]))
		})
	}
	t.NodeOrder, t.EntryOrder, t.EntryOf = make([]int, len(t.ByPlan)), make([][]int, len(t.ByPlan)), make([]int, len(t.Index))
	for n := range t.NodeOrder {
		t.NodeOrder[n] = n
	}
	slices.SortFunc(t.NodeOrder, func(a, b int) int { return cmp.Compare(t.NodeUID[a], t.NodeUID[b]) })
	for e, entry := range t.Entries {
		t.EntryOrder[entry.Node] = append(t.EntryOrder[entry.Node], e)
		for _, q := range entry.Variants {
			t.EntryOf[q] = e
		}
	}
	for n := range t.EntryOrder {
		slices.SortFunc(t.EntryOrder[n], func(a, b int) int {
			ea, eb := t.Entries[a], t.Entries[b]
			qa, qb := ea.Variants[0], eb.Variants[0]
			return cmp.Or(cmp.Compare(ea.Option, eb.Option), cmp.Compare(ea.Mode, eb.Mode), cmp.Compare(t.PlanKeys[qa], t.PlanKeys[qb]), cmp.Compare(t.Index[qa], t.Index[qb]))
		})
	}
}
