package serving

import (
	"context"
	"slices"
	"sync"
	"time"
)

// CapacityTable publishes one coherent budget and complete row group per
// autonomous Workload. A budget refresh keeps running work and coalesces queued
// work; a structural change discards the old group. Recorded rows can stand in
// for matching rows while this process rebuilds them.
type CapacityTable struct {
	mu       sync.Mutex
	idle     *sync.Cond
	rows     map[string]CapacityRow
	running  map[string]*capacityTask
	previous map[string]CapacityRow
	asked    map[string]*capacityArchiveLoad
	groups   map[string]*capacityTableWorkload
	order    []string
	next     int
	parallel int
	workers  int
	loading  *capacityArchiveLoad
	record   func(CapacityRow) error
	load     func(context.Context, []string) (map[string]CapacityRow, error)
	buildRow func(context.Context, SchedulerSettings, []DispatchOccupancy, SchedulerOption, *Definition, time.Time, PathBudget) (CapacityRow, error)
}

// NewCapacityTable runs up to parallel rows at once; record receives each
// built row (the capacity-row-built event) and its error is not retried. load
// returns the latest recorded rows of keys; record and load may be nil.
func NewCapacityTable(parallel int, record func(CapacityRow) error, load func(context.Context, []string) (map[string]CapacityRow, error)) *CapacityTable {
	t := &CapacityTable{rows: map[string]CapacityRow{}, running: map[string]*capacityTask{}, previous: map[string]CapacityRow{}, asked: map[string]*capacityArchiveLoad{},
		groups: map[string]*capacityTableWorkload{}, parallel: max(1, parallel), record: record, load: load, buildRow: BuildCapacityRow}
	t.idle = sync.NewCond(&t.mu)
	return t
}

// Ensure merges the desired inputs and returns at once. Only admitted rows get
// goroutines, so queued upward budget changes can pass queued downward changes.
// Running groups finish even when a newer budget arrives; obsolete structural
// inputs are canceled. ctx bounds the work admitted from this snapshot.
func (t *CapacityTable) Ensure(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	order := make([]string, 0, len(snapshot.Workloads))
	wanted := make(map[string]bool, len(snapshot.Workloads))
	for _, w := range snapshot.Workloads {
		if !slices.Contains(settings.AutonomousWorkloads, w.UID) || wanted[w.UID] {
			continue
		}
		wanted[w.UID] = true
		order = append(order, w.UID)
		g := newCapacityGroup(ctx, settings, snapshot, w)
		state := t.groups[w.UID]
		if state != nil && !state.latest.sameStructure(g) {
			t.dropLocked(w.UID, state)
			state = nil
		}
		if state == nil {
			state = &capacityTableWorkload{}
			t.groups[w.UID] = state
		}
		// Refresh provenance without changing a task's frozen inputs or budget.
		for _, old := range []*capacityGroup{state.active, state.building, state.latest} {
			if old != nil && old.sameBudget(g) {
				old.ctx, old.settings, old.occupancy = g.ctx, g.settings, g.occupancy
				old.workload, old.budget, old.at, old.items = g.workload, g.budget, g.at, g.items
				g = old
			}
		}
		state.latest = g
		if state.building == nil || !state.building.started {
			state.building = g
		}
	}
	for uid, state := range t.groups {
		if !wanted[uid] {
			t.dropLocked(uid, state)
			delete(t.groups, uid)
		}
	}
	t.order = order
	if len(order) > 0 {
		t.next %= len(order)
	}
	t.startLocked()
}

// Wait blocks until admitted rows, their queued successors, and archive loads
// finish. A successor starts under the same lock before a completion wakes Wait.
func (t *CapacityTable) Wait() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for t.workers > 0 || t.loading != nil {
		t.idle.Wait()
	}
}

// Inputs atomically returns the active rows and their matching budgets. Before
// the first complete group exists, it returns the latest desired budget and
// matching recorded or CapacityPending rows. No partially replaced group leaks.
func (t *CapacityTable) Inputs() ([]CapacityRow, []PathBudget) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inputsLocked(true)
}

func (t *CapacityTable) inputsLocked(withBudgets bool) ([]CapacityRow, []PathBudget) {
	rows := []CapacityRow{}
	var budgets []PathBudget
	if withBudgets {
		budgets = make([]PathBudget, 0, len(t.order))
	}
	for _, uid := range t.order {
		state := t.groups[uid]
		g := state.active
		if g == nil {
			g = state.latest
		}
		if withBudgets {
			budgets = append(budgets, g.budget)
		}
		for _, item := range g.items {
			row, found := t.rows[item.key]
			if !found {
				row, found = t.previous[item.key]
			}
			if !found {
				row = CapacityRow{Key: item.key, Status: CapacityPending}
			}
			row.Input = item.input // Numerical reuse keeps current provenance.
			rows = append(rows, row)
		}
	}
	return rows, budgets
}

// Rows returns the management view of the same active groups as Inputs.
func (t *CapacityTable) Rows() []CapacityRow {
	t.mu.Lock()
	defer t.mu.Unlock()
	rows, _ := t.inputsLocked(false)
	return rows
}
