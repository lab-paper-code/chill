package serving

import (
	"context"
	"time"
)

type capacityTask struct {
	option   SchedulerOption
	workload *Definition
	input    CapacityInput
	budget   PathBudget
	key      string
	ctx      context.Context
	cancel   context.CancelFunc
}

// pickLocked admits directly from the current groups, never from a semaphore
// backlog. Within each priority class, rows advance round-robin by Workload.
func (t *CapacityTable) pickLocked() (*capacityGroup, *capacityTableItem) {
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
		delete(t.asked, task.key)
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
