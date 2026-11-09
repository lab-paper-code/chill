package serving

import (
	"context"
	"runtime"
	"slices"
	"sync"
	"time"
)

// CapacityTable publishes one coherent budget and complete row group per
// autonomous Workload. A budget refresh keeps running work and coalesces queued
// work; a structural change withdraws the old group. Matching completed numerical
// rows are reused from memory or the current calculation-contract archive.
type CapacityTable struct {
	mu       sync.Mutex
	idle     *sync.Cond
	rows     map[string]CapacityRow
	running  map[string]*capacityTask
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

// NewCapacityTable runs up to parallel rows at once; zero selects half of
// GOMAXPROCS, with at least one builder. record receives each built row (the
// capacity-row-built event) and its error is not retried. load returns the latest
// recorded rows of keys before builder admission; record and load may be nil.
func NewCapacityTable(parallel int, record func(CapacityRow) error, load func(context.Context, []string) (map[string]CapacityRow, error)) *CapacityTable {
	t := &CapacityTable{rows: map[string]CapacityRow{}, running: map[string]*capacityTask{}, asked: map[string]*capacityArchiveLoad{},
		groups: map[string]*capacityTableWorkload{}, parallel: capacityWorkerLimit(parallel), record: record, load: load, buildRow: BuildCapacityRow}
	t.idle = sync.NewCond(&t.mu)
	return t
}

func capacityWorkerLimit(workers int) int {
	if workers == 0 {
		workers = runtime.GOMAXPROCS(0) / 2
	}
	return max(1, workers)
}

// Configure changes worker admission and removes inputs incompatible with accepted
// configured path budgets. Missing groups wait for the next real Ensure capture.
func (t *CapacityTable) Configure(settings SchedulerSettings) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.parallel = capacityWorkerLimit(settings.CapacityWorkers)
	if !settings.UsesObservedPathBudgets() {
		for uid, state := range t.groups {
			budget := ResolvePathBudget(settings, nil, &state.latest.workload)
			t.retainBudgetLocked(state, budget)
			if state.latest == nil {
				state.latest = state.building
				if state.latest == nil {
					state.latest = state.active
				}
			}
			if state.latest == nil {
				delete(t.groups, uid)
				continue
			}
			for _, g := range []*capacityGroup{state.active, state.building, state.latest} {
				if g != nil {
					g.settings.CompletionMarginMS = settings.CompletionMarginMS
					g.settings.ObservedPathBudgets = settings.ObservedPathBudgets
					g.budget = ResolvePathBudget(settings, []PathBudget{g.budget}, &g.workload)
				}
			}
		}
		t.order = slices.DeleteFunc(t.order, func(uid string) bool { return t.groups[uid] == nil })
	}
	t.startLocked()
}

// CapacityWorkerStatus reports row-builder admission and drain state.
type CapacityWorkerStatus struct {
	Limit  int `json:"limit"`
	Active int `json:"active"`
}

// WorkerStatus includes builders still recording completed rows. Active may
// exceed Limit after a reduction until admitted builders finish.
func (t *CapacityTable) WorkerStatus() CapacityWorkerStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return CapacityWorkerStatus{Limit: t.parallel, Active: t.workers}
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
			t.dropLocked(w.UID, g)
			state = nil
		}
		if state == nil {
			state = &capacityTableWorkload{}
			t.groups[w.UID] = state
		}
		t.mergeGroupLocked(state, g)
	}
	for uid := range t.groups {
		if !wanted[uid] {
			t.dropLocked(uid, nil)
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

// Inputs atomically returns only complete active row groups and their matching
// budgets. A Workload without an active group contributes neither, so consumers
// retain configured defaults and per-Workload missing-row handling.
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
			if withBudgets {
				continue
			}
			g = state.latest
		}
		if withBudgets {
			budgets = append(budgets, g.budget)
		}
		for _, item := range g.items {
			row, found := t.rows[item.key]
			if !found {
				row = CapacityRow{Key: item.key, Status: CapacityPending}
			}
			row.Input = item.input // Numerical reuse keeps current provenance.
			rows = append(rows, row)
		}
	}
	return rows, budgets
}

// Rows returns active groups, or desired diagnostic rows before activation.
func (t *CapacityTable) Rows() []CapacityRow {
	t.mu.Lock()
	defer t.mu.Unlock()
	rows, _ := t.inputsLocked(false)
	return rows
}
