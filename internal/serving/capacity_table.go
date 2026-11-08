package serving

import (
	"context"
	"slices"
	"sync"
	"time"
)
// CapacityTable keeps the rows of autonomous Workloads in memory. Ensure
// starts the rows its snapshot needs and lacks; each row is one task and up to
// parallel rows run at once. Rows exist only in memory, so a start recomputes;
// until a row is built its latest recorded row (load) stands in for it.
type CapacityTable struct {
	mu       sync.Mutex
	rows     map[string]CapacityRow
	running  map[string]*capacityTask
	previous map[string]CapacityRow
	asked    map[string]bool // keys already passed to load
	wanted   []CapacityInput
	parallel chan struct{}
	record   func(CapacityRow) error
	load     func(context.Context, []string) (map[string]CapacityRow, error)
	wg       sync.WaitGroup
}

// NewCapacityTable runs up to parallel rows at once; record receives each
// built row (the capacity-row-built event) and its error is not retried. load
// returns the latest recorded rows of keys; record and load may be nil.
func NewCapacityTable(parallel int, record func(CapacityRow) error, load func(context.Context, []string) (map[string]CapacityRow, error)) *CapacityTable {
	return &CapacityTable{rows: map[string]CapacityRow{}, running: map[string]*capacityTask{}, previous: map[string]CapacityRow{}, asked: map[string]bool{},
		parallel: make(chan struct{}, max(1, parallel)), record: record, load: load}
}

type capacityTask struct {
	option   SchedulerOption
	workload *Definition
	key      string
	ctx      context.Context
	cancel   context.CancelFunc
}

// Ensure starts every missing row for the autonomous Workloads of snapshot and
// returns at once. Missing rows enter the semaphore round-robin by Workload;
// ctx bounds the queued and started rows; keys no longer wanted are canceled.
func (t *CapacityTable) Ensure(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot) {
	wanted := []CapacityInput{}
	pending := make([][]*capacityTask, len(snapshot.Workloads))
	keys := map[string]bool{}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, w := range snapshot.Workloads {
		if !slices.Contains(settings.AutonomousWorkloads, w.UID) {
			continue
		}
		for _, option := range snapshot.Options {
			for _, o := range CapacityVariants(option) {
				if !slices.Contains(o.CompatibleUIDs, w.UID) {
					continue
				}
				in := NewCapacityInput(settings, snapshot.DispatchOccupancy, o, w)
				wanted = append(wanted, in)
				key := in.Key()
				keys[key] = true
				if _, built := t.rows[key]; built || t.running[key] != nil {
					continue
				}
				taskCtx, cancel := context.WithCancel(ctx)
				task := &capacityTask{o, w, key, taskCtx, cancel}
				t.running[key] = task
				t.wg.Add(1)
				pending[i] = append(pending[i], task)
			}
		}
	}
	t.wanted = wanted
	for key, task := range t.running {
		if !keys[key] {
			task.cancel()
			delete(t.running, key)
		}
	}
	go func() {
		for row := 0; ; row++ {
			more := false
			for _, tasks := range pending {
				if row >= len(tasks) {
					continue
				}
				more = true
				task := tasks[row]
				select {
				case t.parallel <- struct{}{}:
					go t.build(settings, snapshot.DispatchOccupancy, task, snapshot.At)
				case <-task.ctx.Done():
					t.mu.Lock()
					if t.running[task.key] == task {
						delete(t.running, task.key)
					}
					t.mu.Unlock()
					task.cancel()
					t.wg.Done()
				}
			}
			if !more {
				return
			}
		}
	}()
	missing := []string{}
	for _, in := range wanted {
		if key := in.Key(); t.running[key] != nil && !t.asked[key] {
			t.asked[key] = true
			missing = append(missing, key)
		}
	}
	if t.load != nil && len(missing) > 0 {
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			rows, err := t.load(ctx, missing)
			if err != nil {
				return // the rows stay CapacityPending until built
			}
			t.mu.Lock()
			defer t.mu.Unlock()
			for key, row := range rows {
				row.Previous = true
				t.previous[key] = row
			}
		}()
	}
}

func (t *CapacityTable) build(settings SchedulerSettings, occupancy []DispatchOccupancy, task *capacityTask, at time.Time) {
	defer t.wg.Done()
	defer task.cancel()
	defer func() { <-t.parallel }()
	row, err := BuildCapacityRow(task.ctx, settings, occupancy, task.option, task.workload, at)
	t.mu.Lock()
	if t.running[task.key] != task {
		t.mu.Unlock()
		return
	}
	delete(t.running, task.key)
	if task.ctx.Err() != nil {
		t.mu.Unlock()
		return // Discard partial results, even if the build returned successfully.
	}
	if err != nil {
		row.Status, row.Reason, row.Kappa, row.BuiltAt = CapacityRejected, err.Error(), nil, time.Now().UTC()
	}
	t.rows[task.key] = row
	t.mu.Unlock()
	if t.record != nil {
		_ = t.record(row)
	}
}

// Wait blocks until every started row ends.
func (t *CapacityTable) Wait() { t.wg.Wait() }

// Rows returns the rows the last Ensure wanted, in that order. A row still
// being computed is its latest recorded row (Previous) or CapacityPending.
func (t *CapacityTable) Rows() []CapacityRow {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]CapacityRow, 0, len(t.wanted))
	for _, in := range t.wanted {
		key := in.Key()
		row, built := t.rows[key]
		if !built {
			row, built = t.previous[key]
		}
		if !built {
			row = CapacityRow{Key: key, Input: in, Status: CapacityPending}
		}
		row.Input = in // Keep current provenance even when numerical κ is reused.
		out = append(out, row)
	}
	return out
}
