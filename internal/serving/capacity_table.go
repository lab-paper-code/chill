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
	asked    map[string]*capacityLoad
	groups   map[string]*capacityWorkload
	order    []string
	next     int
	parallel int
	workers  int
	loading  *capacityLoad
	record   func(CapacityRow) error
	load     func(context.Context, []string) (map[string]CapacityRow, error)
	buildRow func(context.Context, SchedulerSettings, []DispatchOccupancy, SchedulerOption, *Definition, time.Time, PathBudget) (CapacityRow, error)
}

// NewCapacityTable runs up to parallel rows at once; record receives each
// built row (the capacity-row-built event) and its error is not retried. load
// returns the latest recorded rows of keys; record and load may be nil.
func NewCapacityTable(parallel int, record func(CapacityRow) error, load func(context.Context, []string) (map[string]CapacityRow, error)) *CapacityTable {
	t := &CapacityTable{rows: map[string]CapacityRow{}, running: map[string]*capacityTask{}, previous: map[string]CapacityRow{}, asked: map[string]*capacityLoad{},
		groups: map[string]*capacityWorkload{}, parallel: max(1, parallel), record: record, load: load, buildRow: BuildCapacityRow}
	t.idle = sync.NewCond(&t.mu)
	return t
}

type capacityWorkload struct {
	active, building, latest *capacityGroup
}

type capacityGroup struct {
	ctx       context.Context
	settings  SchedulerSettings
	occupancy []DispatchOccupancy
	workload  Definition
	budget    PathBudget
	at        time.Time
	items     []capacityItem
	structure map[string]bool
	started   bool
}

type capacityItem struct {
	option SchedulerOption
	input  CapacityInput
	key    string
}

type capacityTask struct {
	option   SchedulerOption
	workload *Definition
	input    CapacityInput
	budget   PathBudget
	key      string
	ctx      context.Context
	cancel   context.CancelFunc
}

type capacityLoad struct {
	ctx  context.Context
	keys []string
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
			state = &capacityWorkload{}
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

func newCapacityGroup(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, w *Definition) *capacityGroup {
	g := &capacityGroup{ctx: ctx, settings: settings, occupancy: snapshot.DispatchOccupancy, workload: *w,
		budget: ResolvePathBudget(settings, snapshot.PathBudgets, w), at: snapshot.At, structure: map[string]bool{}}
	for _, option := range snapshot.Options {
		for _, o := range CapacityVariants(option) {
			if !slices.Contains(o.CompatibleUIDs, w.UID) {
				continue
			}
			in := NewCapacityInput(settings, snapshot.DispatchOccupancy, o, w, g.budget)
			g.items = append(g.items, capacityItem{option: o, input: in, key: in.Key()})
			in.CompletionMarginMS, in.PostMarginMS, in.JudgmentPercentile = 0, 0, 0
			g.structure[in.Key()] = true
		}
	}
	return g
}

func (g *capacityGroup) sameStructure(other *capacityGroup) bool {
	if g.workload.Identity() != other.workload.Identity() || len(g.structure) != len(other.structure) {
		return false
	}
	for key := range g.structure {
		if !other.structure[key] {
			return false
		}
	}
	return true
}

func (g *capacityGroup) sameBudget(other *capacityGroup) bool {
	return g.budget.TotalMS == other.budget.TotalMS && g.budget.PostMS == other.budget.PostMS &&
		g.budget.JudgmentPercentile == other.budget.JudgmentPercentile
}

func (t *CapacityTable) dropLocked(uid string, state *capacityWorkload) {
	for key, task := range t.running {
		if task.workload.UID == uid {
			task.cancel()
			delete(t.running, key)
		}
	}
	for _, g := range []*capacityGroup{state.active, state.building, state.latest} {
		if g != nil {
			for _, item := range g.items {
				delete(t.rows, item.key)
				delete(t.previous, item.key)
				delete(t.asked, item.key)
			}
		}
	}
}

func (t *CapacityTable) completeLocked(g *capacityGroup, recorded bool) bool {
	for _, item := range g.items {
		row, found := t.rows[item.key]
		if !found && recorded {
			row, found = t.previous[item.key]
		}
		if !found || row.Status != CapacityBuilt && row.Status != CapacityRejected {
			return false
		}
	}
	return true
}

func (t *CapacityTable) advanceLocked() {
	for _, state := range t.groups {
		if g := state.building; g != nil {
			select {
			case <-g.ctx.Done():
				g.started, state.building = false, nil
			default:
				if t.completeLocked(g, true) {
					state.active = g
				}
				if t.completeLocked(g, false) {
					state.active, state.building = g, nil
				}
			}
		}
		if state.building == nil {
			if t.completeLocked(state.latest, false) {
				state.active = state.latest
			} else {
				select {
				case <-state.latest.ctx.Done():
				default:
					state.building = state.latest
				}
			}
		}
	}
}

// pickLocked admits directly from the current groups, never from a semaphore
// backlog. Within each priority class, rows advance round-robin by Workload.
func (t *CapacityTable) pickLocked() (*capacityGroup, *capacityItem) {
	for _, upward := range []bool{true, false} {
		for offset := range t.order {
			index := (t.next + offset) % len(t.order)
			state := t.groups[t.order[index]]
			g := state.building
			if g == nil {
				continue
			}
			select {
			case <-g.ctx.Done():
				continue
			default:
			}
			up := state.active != nil && (g.budget.TotalMS > state.active.budget.TotalMS ||
				g.budget.PostMS > state.active.budget.PostMS || g.budget.JudgmentPercentile > state.active.budget.JudgmentPercentile)
			if up != upward {
				continue
			}
			for i := range g.items {
				item := &g.items[i]
				if _, built := t.rows[item.key]; !built && t.running[item.key] == nil {
					t.next = (index + 1) % len(t.order)
					return g, item
				}
			}
		}
	}
	return nil, nil
}

func (t *CapacityTable) startLocked() {
	t.advanceLocked()
	t.pruneLocked()
	for t.workers < t.parallel {
		g, item := t.pickLocked()
		if g == nil {
			break
		}
		ctx, cancel := context.WithCancel(g.ctx)
		w := g.workload
		task := &capacityTask{option: item.option, workload: &w, input: item.input, budget: g.budget, key: item.key, ctx: ctx, cancel: cancel}
		g.started = true
		t.running[item.key] = task
		t.workers++
		go t.build(g.settings, g.occupancy, task, g.at)
	}
	if t.load != nil && t.loading == nil {
		t.startLoadLocked()
	}
}

func (t *CapacityTable) build(settings SchedulerSettings, occupancy []DispatchOccupancy, task *capacityTask, at time.Time) {
	row, err := t.buildRow(task.ctx, settings, occupancy, task.option, task.workload, at, task.budget)
	t.mu.Lock()
	publish := t.running[task.key] == task && task.ctx.Err() == nil
	if t.running[task.key] == task {
		delete(t.running, task.key)
	}
	if publish {
		row.Key, row.Input = task.key, task.input
		if err != nil {
			row.Status, row.Reason, row.Kappa, row.BuiltAt = CapacityRejected, err.Error(), nil, time.Now().UTC()
		}
		t.rows[task.key] = row
		delete(t.previous, task.key)
		t.advanceLocked()
	}
	t.mu.Unlock()
	task.cancel()
	if publish && t.record != nil {
		_ = t.record(row)
	}
	t.mu.Lock()
	t.workers--
	t.startLocked() // Account for successor work before waking Wait.
	t.idle.Broadcast()
	t.mu.Unlock()
}

func (t *CapacityTable) startLoadLocked() {
	request := &capacityLoad{}
	for _, uid := range t.order {
		state := t.groups[uid]
		for _, g := range []*capacityGroup{state.active, state.building, state.latest} {
			if g == nil {
				continue
			}
			select {
			case <-g.ctx.Done():
				continue
			default:
			}
			for _, item := range g.items {
				if _, built := t.rows[item.key]; built || t.asked[item.key] != nil {
					continue
				}
				request.ctx = g.ctx
				request.keys = append(request.keys, item.key)
				t.asked[item.key] = request
			}
		}
	}
	if len(request.keys) == 0 {
		return
	}
	t.loading = request
	go func() {
		rows, err := t.load(request.ctx, request.keys)
		t.mu.Lock()
		if err == nil {
			for key, row := range rows {
				if _, built := t.rows[key]; !built && t.asked[key] == request {
					row.Previous = true
					t.previous[key] = row
				}
			}
		}
		t.loading = nil
		t.startLocked()
		t.idle.Broadcast()
		t.mu.Unlock()
	}()
}

func (t *CapacityTable) pruneLocked() {
	keys := map[string]bool{}
	for _, state := range t.groups {
		for _, g := range []*capacityGroup{state.active, state.building, state.latest} {
			if g != nil {
				for _, item := range g.items {
					keys[item.key] = true
				}
			}
		}
	}
	for key := range t.rows {
		if !keys[key] {
			delete(t.rows, key)
		}
	}
	for key := range t.previous {
		if !keys[key] {
			delete(t.previous, key)
		}
	}
	for key := range t.asked {
		if !keys[key] {
			delete(t.asked, key)
		}
	}
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
