package serving

import (
	"maps"
	"testing"
)

func TestSchedulerRebaseRoutingKeepsNewerCommittedCapacity(t *testing.T) {
	ready := map[string]bool{"a": true, "b": true, "pending": false}
	b := SchedulerRebaseRouting(map[string]int32{"a": 1000}, map[string]int32{"b": 300, "pending": 700}, ready, nil)
	if b["a"] != 700 || b["b"] != 300 || b["pending"] != 0 {
		t.Fatalf("new owner lost optimizer's 300-unit share or routed pending capacity: %v", b)
	}
	late := SchedulerRebaseRouting(b, map[string]int32{"a": 1000}, ready, map[string]bool{"b": true})
	if !maps.Equal(late, b) {
		t.Fatalf("older final route overwrote newer committed capacity: before=%v after=%v", b, late)
	}
	withdrawn := SchedulerRebaseRouting(late, map[string]int32{"a": 0}, ready, nil)
	if withdrawn["a"] != 0 || withdrawn["b"] != 1000 {
		t.Fatalf("explicit withdrawal was not retained: %v", withdrawn)
	}
	if partial := SchedulerRebaseRouting(nil, map[string]int32{"b": 300, "pending": 700}, ready, nil); partial["b"] != 1000 || partial["pending"] != 0 {
		t.Fatalf("partial-Ready route was not normalized: %v", partial)
	}
	if empty := SchedulerRebaseRouting(nil, map[string]int32{"pending": 1000}, ready, nil); empty != nil {
		t.Fatalf("no Ready destination fabricated a route: %v", empty)
	}
}

func TestSchedulerRebaseRoutingRoundsDeterministically(t *testing.T) {
	ready := map[string]bool{"a": true, "b": true, "c": true, "new": true}
	first := SchedulerRebaseRouting(map[string]int32{"c": 1, "a": 1, "b": 1}, map[string]int32{"new": 300}, ready, nil)
	second := SchedulerRebaseRouting(map[string]int32{"b": 1, "c": 1, "a": 1}, map[string]int32{"new": 300}, ready, nil)
	if !maps.Equal(first, second) || first["a"] != 233 || first["b"] != 233 || first["c"] != 234 || first["new"] != 300 {
		t.Fatalf("rounding depends on map order or lost optimizer share: %v / %v", first, second)
	}
}
