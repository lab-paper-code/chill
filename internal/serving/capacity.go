package serving

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"
)

// Capacity row statuses and the management event that records a built row.
const (
	CapacityBuilt             = "Built"
	CapacityPending           = "CapacityPending"
	CapacityRejected          = "CapacityRejected"
	CapacityNonMonotone       = "NonMonotone"
	CapacityNoOccupancy       = "MissingDispatchOccupancy"
	CapacityRowBuiltEvent     = "capacity-row-built"
	CapacityServiceExceedsSLO = "BatchServiceExceedsSLO"
)

// CapacityInput names everything a row's kappa depends on; its hash is the row key.
type CapacityInput struct {
	WorkloadUID        string              `json:"workloadUID"`
	SLO                SLO                 `json:"slo"`
	Node               NodeReference       `json:"node"`
	PowerMode          string              `json:"powerMode"`
	ProfileID          string              `json:"profileID"`
	Allocation         SchedulerAllocation `json:"allocation"`
	MaxBatch           int                 `json:"maxBatch"`
	CompletionMarginMS float64             `json:"completionMarginMS"`
	Window             time.Duration       `json:"capacityWindowNanos"`
	Windows            int                 `json:"capacityWindows"`
	PassingWindows     int                 `json:"capacityPassingWindows"`
	MinimumRequests    int                 `json:"capacityMinimumRequests"`
	GridSteps          int                 `json:"capacityGridSteps"`
	BisectionWidth     float64             `json:"capacityBisectionWidth"`
	TailSamples        int                 `json:"capacityTailSamples"`
	// DispatchOccupancy is the measurement for the option's Node, power mode,
	// runtime image and model; without one the row has no kappa.
	DispatchOccupancy *DispatchOccupancy `json:"dispatchOccupancy,omitempty"`
}

// CapacityJudgment is one judged Poisson rate: the SLO-percentile latency of
// each judged window (ms, margin included; NaN-free, a failed window keeps -1).
type CapacityJudgment struct {
	Rate         float64   `json:"rate"`
	Requests     int       `json:"requests"`
	Passed       int       `json:"passed"`
	Met          bool      `json:"met"`
	PercentileMS []float64 `json:"percentileMS"`
}

// CapacityRow is the largest Poisson rate kappa (req/s) at which one instance
// of the option meets the Workload SLO in PassingWindows of Windows windows.
type CapacityRow struct {
	Key                 string             `json:"key"`
	Input               CapacityInput      `json:"input"`
	Status              string             `json:"status"`
	Reason              string             `json:"reason,omitempty"`
	Mu                  float64            `json:"mu"`
	Kappa               *float64           `json:"kappa,omitempty"`
	MinimumServiceNanos float64            `json:"minimumServiceNanos,omitempty"`
	Grid                []CapacityJudgment `json:"grid,omitempty"`
	Bisection           []CapacityJudgment `json:"bisection,omitempty"`
	BuiltAt             time.Time          `json:"builtAt"`
	Elapsed             time.Duration      `json:"elapsedNanos,omitempty"`
	// Previous marks a recorded row from an earlier build that stands in while
	// this build computes the row.
	Previous bool `json:"previous,omitempty"`
}

// CapacityVariants lists option o's measured max-batch variants (batch sizes up
// to the option's MaxBatch) in profile batch order.
func CapacityVariants(o SchedulerOption) []SchedulerOption {
	out := []SchedulerOption{}
	if o.Instance.Profile == nil {
		return out
	}
	for _, b := range o.Instance.Profile.Batches {
		if b.Batch <= o.Instance.MaxBatch {
			v := o
			v.Instance.MaxBatch = b.Batch
			out = append(out, v)
		}
	}
	return out
}

// NewCapacityInput keys option variant o for Workload w under settings and the
// measured dispatch occupancy.
func NewCapacityInput(settings SchedulerSettings, occupancy []DispatchOccupancy, o SchedulerOption, w *Definition) CapacityInput {
	i := o.Instance
	return CapacityInput{
		WorkloadUID: w.UID, SLO: w.SLO, Node: i.Node, PowerMode: i.Profile.Condition.PowerMode, ProfileID: i.Profile.ID,
		Allocation: i.Allocation, MaxBatch: i.MaxBatch, CompletionMarginMS: settings.CompletionMarginMS,
		Window: settings.CapacityWindow, Windows: settings.CapacityWindows, PassingWindows: settings.CapacityPassingWindows,
		MinimumRequests: settings.CapacityMinimumRequests, DispatchOccupancy: dispatchOccupancy(occupancy, o.Instance),
		GridSteps: settings.CapacityGridSteps, BisectionWidth: settings.CapacityBisectionWidth, TailSamples: settings.CapacityTailSamples,
	}
}

// Key is the stable row key of the input.
func (in CapacityInput) Key() string {
	data, _ := json.Marshal(in)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:12])
}

// capacityThroughput is the largest b/(mean service time + b × occupancy)
// over batches up to b (req/s).
func capacityThroughput(p *Profile, b int, occupancy time.Duration) float64 {
	mu := 0.0
	for _, batch := range p.Batches {
		if mean, ok := p.MeanNanos(batch.Batch); ok && batch.Batch <= b {
			mu = max(mu, float64(batch.Batch)/(mean+float64(batch.Batch)*float64(occupancy))*1e9)
		}
	}
	return mu
}

// capacityBuilder judges rates for one row with replay, which returns one
// window's SLO-percentile latency (ms) and whether the window met the SLO.
type capacityBuilder struct {
	settings   SchedulerSettings
	percentile float64
	replay     func(ctx context.Context, lambda float64, n, seed int) (float64, bool, error)
}

// BuildCapacityRow computes the row of option variant o for Workload w. node
// is the option's Node and baselines its idle evidence; at anchors the windows.
// An option whose Node and runtime image lack a dispatch occupancy measurement
// gets a rejected row without kappa.
func BuildCapacityRow(ctx context.Context, settings SchedulerSettings, occupancy []DispatchOccupancy, o SchedulerOption, w *Definition, node SchedulerNode, baselines []NodeBaseline, at time.Time) (CapacityRow, error) {
	if err := ctx.Err(); err != nil {
		return CapacityRow{}, err
	}
	began := time.Now()
	in := NewCapacityInput(settings, occupancy, o, w)
	row := CapacityRow{Key: in.Key(), Input: in}
	if in.DispatchOccupancy == nil {
		row.Status, row.Reason, row.BuiltAt = CapacityRejected, CapacityNoOccupancy, time.Now().UTC()
		return row, nil
	}
	row.Mu = capacityThroughput(o.Instance.Profile, o.Instance.MaxBatch, in.DispatchOccupancy.PerRequest)
	// A mean is not a lower bound: replay may choose any observed sample of
	// any batch up to MaxBatch. Only skip when even the fastest cannot fit.
	minimum := math.Inf(1)
	for _, batch := range o.Instance.Profile.Batches {
		if batch.Batch <= o.Instance.MaxBatch {
			for _, duration := range batch.DurationNanos {
				minimum = min(minimum, duration)
			}
		}
	}
	if !math.IsInf(minimum, 1) && minimum+settings.CompletionMarginMS*1e6 > float64(w.SLO.TargetLatency) {
		kappa := 0.0
		row.Status, row.Reason, row.Kappa = CapacityBuilt, CapacityServiceExceedsSLO, &kappa
		row.MinimumServiceNanos = minimum
		row.BuiltAt, row.Elapsed = time.Now().UTC(), time.Since(began)
		return row, nil
	}
	r := &capacityReplay{settings: settings, node: node, at: at, baselines: baselines, occupancy: *in.DispatchOccupancy, option: o, w: w, gaps: map[[2]int][]float64{}}
	b := capacityBuilder{settings: settings, percentile: w.SLO.Percentile, replay: r.window}
	if err := b.build(ctx, &row); err != nil {
		return row, err
	}
	row.BuiltAt, row.Elapsed = time.Now().UTC(), time.Since(began)
	return row, nil
}

// build judges every configured grid rate before bisecting the last passing
// interval to the selected fraction of mu. A pass after a miss rejects the row.
func (b *capacityBuilder) build(ctx context.Context, row *CapacityRow) error {
	met := 0
	for k := 1; k <= b.settings.CapacityGridSteps; k++ {
		j, err := b.judge(ctx, b.gridStep(k, row.Mu))
		if err != nil {
			return err
		}
		row.Grid = append(row.Grid, j)
		if j.Met && met == k-1 {
			met = k
		} else if j.Met {
			row.Status, row.Reason = CapacityRejected, CapacityNonMonotone
		}
	}
	if row.Status != "" {
		return nil
	}
	kappa := b.gridStep(met, row.Mu)
	if met > 0 && met < b.settings.CapacityGridSteps {
		hi := b.gridStep(met+1, row.Mu)
		for hi-kappa > b.settings.CapacityBisectionWidth*row.Mu {
			mid := (kappa + hi) / 2
			if mid == kappa || mid == hi {
				break // The selected width is below floating-point resolution.
			}
			j, err := b.judge(ctx, mid)
			if err != nil {
				return err
			}
			row.Bisection = append(row.Bisection, j)
			if j.Met {
				kappa = mid
			} else {
				hi = mid
			}
		}
	}
	row.Status, row.Kappa = CapacityBuilt, &kappa
	return nil
}

func (b *capacityBuilder) gridStep(k int, mu float64) float64 {
	return float64(k) / float64(b.settings.CapacityGridSteps) * mu
}

// requests is the window population: MinimumRequests, CapacityTailSamples
// above the SLO percentile, and at least CapacityWindow of arrivals.
func (b *capacityBuilder) requests(lambda float64) int {
	n := max(b.settings.CapacityMinimumRequests, int(math.Ceil(lambda*b.settings.CapacityWindow.Seconds())))
	if b.percentile < 100 {
		n = max(n, int(math.Ceil(float64(b.settings.CapacityTailSamples)/(1-b.percentile/100))))
	}
	return n
}

// judge replays windows seeded 1..Windows at rate lambda and stops once the
// outcome is decided; lambda is met when PassingWindows windows meet the SLO.
func (b *capacityBuilder) judge(ctx context.Context, lambda float64) (CapacityJudgment, error) {
	s := b.settings
	j := CapacityJudgment{Rate: lambda, Requests: b.requests(lambda)}
	for seed := 1; seed <= s.CapacityWindows && j.Passed < s.CapacityPassingWindows && j.Passed+s.CapacityWindows-seed+1 >= s.CapacityPassingWindows; seed++ {
		latency, ok, err := b.replay(ctx, lambda, j.Requests, seed)
		if err != nil {
			return j, err
		}
		j.PercentileMS = append(j.PercentileMS, latency)
		if ok {
			j.Passed++
		}
	}
	j.Met = j.Passed >= s.CapacityPassingWindows
	return j, nil
}

// capacityReplay replays windows of one option instance; gaps are the
// unit-rate exponential gaps per (seed, request count), shared across rates.
type capacityReplay struct {
	settings  SchedulerSettings
	node      SchedulerNode
	at        time.Time
	baselines []NodeBaseline
	occupancy DispatchOccupancy
	option    SchedulerOption
	w         *Definition
	gaps      map[[2]int][]float64
}

// capacityWindowLimit can only disprove a window; a passing window still
// replays every arrival. The fixed population matches stat.Empirical's rank.
type capacityWindowLimit struct {
	requests int
	slo      SLO
	missed   int
	failed   bool
}

func (s *schedulerReplayState) capacityWindowFailed() bool {
	limit := s.capacity
	if limit == nil {
		return false
	}
	for _, r := range s.live {
		if r.expired || r.dropped {
			limit.failed = true
			return true
		}
		if r.capacityMiss {
			continue
		}
		end := s.now
		if r.completed {
			end = r.completion
		}
		if float64(r.source.Elapsed)+end-r.arrival > float64(limit.slo.TargetLatency) {
			r.capacityMiss = true
			limit.missed++
		}
	}
	limit.failed = limit.missed > limit.requests-int(math.Ceil(float64(limit.requests)*limit.slo.Percentile/100))
	return limit.failed
}

// window replays one window of n Poisson arrivals at lambda that starts empty
// and returns the SLO-percentile latency (ms, margin included; -1 when none)
// and whether the SLO holds with no timeout or expiry.
func (b *capacityReplay) window(ctx context.Context, lambda float64, n, seed int) (float64, bool, error) {
	gaps := b.gaps[[2]int{seed, n}]
	if gaps == nil {
		rng := rand.New(rand.NewPCG(uint64(seed), 1))
		gaps = make([]float64, n)
		for j := range gaps {
			gaps[j] = rng.ExpFloat64()
		}
		b.gaps[[2]int{seed, n}] = gaps
	}
	at, w, o := b.at, b.w, b.option
	offsets := make([]time.Duration, n)
	t := 0.0
	for j, g := range gaps {
		t += g / lambda
		offsets[j] = time.Duration(t * 1e9)
	}
	span := offsets[n-1] + time.Millisecond
	start := at.Add(-span)
	arrivals := make([]SchedulerArrival, n)
	for j, offset := range offsets {
		arrivals[j] = SchedulerArrival{ID: fmt.Sprintf("a%07d", j), At: start.Add(offset), Definition: w, Accepted: true, Compute: true, WorkloadUID: w.UID}
	}
	node := b.node
	node.PowerMode, node.Ready, node.Measurement, node.Reasons, node.ObservedAt = o.Instance.Profile.Condition.PowerMode, true, false, nil, at
	instance := o.Instance
	instance.ID, instance.TargetID, instance.OwnerWorkloadUID = "capacity", "", ""
	instance.WorkloadUID, instance.Ready, instance.Stopped = w.UID, true, false
	instance.Weight, instance.ObservedAt = 1, at
	snapshot := SchedulerSnapshot{
		At: at, ObservationEnd: at, Workloads: []*Definition{w}, Nodes: []SchedulerNode{node},
		Instances: []SchedulerInstance{instance}, Routing: map[string]map[string]int32{w.UID: {instance.ID: 1}},
		Arrivals: arrivals, Options: []SchedulerOption{o}, Baselines: b.baselines, DispatchOccupancy: []DispatchOccupancy{b.occupancy},
	}
	settings := b.settings
	settings.AutonomousWorkloads = nil
	settings.ObservationWindow, settings.ComparisonPeriod, settings.MaxObservationAge = span, span, time.Hour
	keep := SchedulerCandidate{ID: "keep", Keep: true, Nodes: snapshot.Nodes, Instances: snapshot.Instances, Routing: snapshot.Routing}
	// An empty compared set counts no energy, so no Node baseline is needed.
	limit := capacityWindowLimit{requests: n, slo: w.SLO}
	estimate, err := schedulerReplayEstimate(ctx, settings, snapshot, keep, uint64(seed), map[NodeReference]bool{}, &limit)
	if err != nil {
		return 0, false, err
	}
	if limit.failed {
		return -1, false, nil // A failed window has no complete percentile sample.
	}
	if len(estimate.Reasons) != 0 {
		return 0, false, fmt.Errorf("capacity replay: %s", strings.Join(estimate.Reasons, ", "))
	}
	for _, e := range estimate.SLO {
		if e.UID != w.UID || e.ExpectedNanos == nil {
			continue
		}
		for kind, count := range e.PredictedOutcomes {
			if count > 0 && (kind == "timeout" || strings.HasPrefix(kind, "expired-")) {
				return *e.ExpectedNanos / 1e6, false, nil
			}
		}
		return *e.ExpectedNanos / 1e6, *e.ExpectedNanos <= float64(w.SLO.TargetLatency), nil
	}
	return -1, false, nil
}

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
	node     SchedulerNode
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
			if !slices.Contains(option.CompatibleUIDs, w.UID) {
				continue
			}
			for _, o := range CapacityVariants(option) {
				in := NewCapacityInput(settings, snapshot.DispatchOccupancy, o, w)
				wanted = append(wanted, in)
				key := in.Key()
				keys[key] = true
				if _, built := t.rows[key]; built || t.running[key] != nil {
					continue
				}
				node := SchedulerNode{Node: o.Instance.Node, CPUs: o.Instance.Allocation.CPUs, MemoryBytes: o.Instance.Allocation.MemoryBytes}
				if gpu := o.Instance.Allocation.GPUUUID; gpu != "" {
					node.GPUUUIDs = []string{gpu}
				}
				for _, n := range snapshot.Nodes {
					if n.Node.UID == o.Instance.Node.UID {
						node = n
					}
				}
				taskCtx, cancel := context.WithCancel(ctx)
				task := &capacityTask{o, w, node, key, taskCtx, cancel}
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
					go t.build(settings, snapshot.DispatchOccupancy, task, snapshot.Baselines, snapshot.At)
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

func (t *CapacityTable) build(settings SchedulerSettings, occupancy []DispatchOccupancy, task *capacityTask, baselines []NodeBaseline, at time.Time) {
	defer t.wg.Done()
	defer task.cancel()
	defer func() { <-t.parallel }()
	row, err := BuildCapacityRow(task.ctx, settings, occupancy, task.option, task.workload, task.node, baselines, at)
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
		out = append(out, row)
	}
	return out
}
