package serving

import (
	"context"
	"slices"
	"time"
)

type capacityTableWorkload struct {
	active, building, latest *capacityGroup
}

type capacityGroup struct {
	ctx       context.Context
	settings  SchedulerSettings
	occupancy []DispatchOccupancy
	workload  Definition
	budget    PathBudget
	at        time.Time
	items     []capacityTableItem
	structure map[string]bool
	started   bool
}

type capacityTableItem struct {
	option SchedulerOption
	input  CapacityInput
	key    string
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
			g.items = append(g.items, capacityTableItem{option: o, input: in, key: in.Key()})
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
	return g.hasBudget(other.budget)
}

func (g *capacityGroup) hasBudget(budget PathBudget) bool {
	return g.budget.TotalMS == budget.TotalMS && g.budget.PostMS == budget.PostMS &&
		g.budget.JudgmentPercentile == budget.JudgmentPercentile
}

func (t *CapacityTable) retainBudgetLocked(state *capacityTableWorkload, budget PathBudget) {
	if state.active != nil && !state.active.hasBudget(budget) {
		state.active = nil
	}
	if state.building != nil && !state.building.hasBudget(budget) {
		state.building = nil
	}
	if state.latest != nil && !state.latest.hasBudget(budget) {
		state.latest = nil
	}
	for key, task := range t.running {
		if task.workload.UID == budget.WorkloadUID && (task.budget.TotalMS != budget.TotalMS ||
			task.budget.PostMS != budget.PostMS || task.budget.JudgmentPercentile != budget.JudgmentPercentile) {
			task.cancel()
			delete(t.running, key)
		}
	}
}

func (t *CapacityTable) mergeGroupLocked(state *capacityTableWorkload, g *capacityGroup) {
	if !g.settings.UsesObservedPathBudgets() {
		t.retainBudgetLocked(state, g.budget)
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

func (t *CapacityTable) dropLocked(uid string, state *capacityTableWorkload) {
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
