package protocol

import "slices"

// Rank borrows State's result buffers until its next Delta, Apply or Reset.
// Retain a candidate with CopyInto and caller-owned buffers, allocated once.
// Excesses contains heuristic e_h in the adapter's Workload priority order.
// Better preserves that order; exact outcomes are ranked by the native port.
type Rank struct {
	Feasible, Final, Keep, WeightsExceeded bool
	UnroutedPrepare, Searchable, Bounded   bool
	Joules, Route, KeepJoules, Period      float64
	Excesses, Demand, Supply               []float64
	Satisfied                              []bool
	Instances, CountExcess                 int
	Choice                                 []int // table indices, not v1 plan indices
	Table                                  *Table
}

func (a Rank) CopyInto(b *Rank) {
	excesses, choice := b.Excesses, b.Choice
	demand, supply, satisfied := b.Demand, b.Supply, b.Satisfied
	*b = a
	b.Excesses, b.Choice = excesses[:len(a.Excesses)], choice[:len(a.Choice)]
	copy(b.Excesses, a.Excesses)
	copy(b.Choice, a.Choice)
	// Existing evaluator callers may only retain the ordering projection.
	if len(demand) >= len(a.Demand) {
		b.Demand, b.Supply, b.Satisfied = demand[:len(a.Demand)], supply[:len(a.Supply)], satisfied[:len(a.Satisfied)]
		copy(b.Demand, a.Demand)
		copy(b.Supply, a.Supply)
		copy(b.Satisfied, a.Satisfied)
	} else {
		b.Demand, b.Supply, b.Satisfied = nil, nil, nil
	}
}

func (a Rank) Better(b *Rank) bool {
	if b == nil {
		return true
	}
	if a.Feasible != b.Feasible {
		return a.Feasible
	}
	if !a.Feasible {
		if order := slices.Compare(a.Excesses, b.Excesses); order != 0 {
			return order < 0
		}
	}
	if a.Joules-a.KeepJoules != b.Joules-b.KeepJoules {
		return a.Joules-a.KeepJoules < b.Joules-b.KeepJoules
	}
	if a.Keep != b.Keep {
		return a.Keep
	}
	return a.CompareKey(*b) < 0
}

// CompareKey compares the exact v1 joined string without constructing it. A
// fragment rank alone is insufficient when one key prefixes another (the '|'
// delimiter sorts after many key characters). Byte streams also handle keys
// containing delimiters, without imposing a new key-format restriction.
func (a Rank) CompareKey(b Rank) int {
	an, ai, bn, bi := 0, 0, 0, 0
	for {
		for an < len(a.Choice) && ai == len(a.Table.KeyParts[a.Choice[an]]) {
			an, ai = an+1, 0
		}
		for bn < len(b.Choice) && bi == len(b.Table.KeyParts[b.Choice[bn]]) {
			bn, bi = bn+1, 0
		}
		if an == len(a.Choice) || bn == len(b.Choice) {
			if an == len(a.Choice) && bn == len(b.Choice) {
				return 0
			}
			if an == len(a.Choice) {
				return -1
			}
			return 1
		}
		ac, bc := a.Table.KeyParts[a.Choice[an]][ai], b.Table.KeyParts[b.Choice[bn]][bi]
		if ac < bc {
			return -1
		}
		if ac > bc {
			return 1
		}
		ai++
		bi++
	}
}
