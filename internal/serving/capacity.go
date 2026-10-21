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
	CapacityBuilt         = "Built"
	CapacityPending       = "CapacityPending"
	CapacityRejected      = "CapacityRejected"
	CapacityNonMonotone   = "NonMonotone"
	CapacityNoOccupancy   = "MissingDispatchOccupancy"
	CapacityRowBuiltEvent = "capacity-row-built"

	capacityGridSteps      = 20    // grid rates 0.05k mu, k = 1..20
	capacityBisectionWidth = 0.005 // of mu
	capacityTailSamples    = 100   // samples above the SLO percentile in each window
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
	Key       string             `json:"key"`
	Input     CapacityInput      `json:"input"`
	Status    string             `json:"status"`
	Reason    string             `json:"reason,omitempty"`
	Mu        float64            `json:"mu"`
	Kappa     *float64           `json:"kappa,omitempty"`
	Grid      []CapacityJudgment `json:"grid,omitempty"`
	Bisection []CapacityJudgment `json:"bisection,omitempty"`
	BuiltAt   time.Time          `json:"builtAt"`
	Elapsed   time.Duration      `json:"elapsedNanos,omitempty"`
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
	began := time.Now()
	in := NewCapacityInput(settings, occupancy, o, w)
	row := CapacityRow{Key: in.Key(), Input: in}
	if in.DispatchOccupancy == nil {
		row.Status, row.Reason, row.BuiltAt = CapacityRejected, CapacityNoOccupancy, time.Now().UTC()
		return row, nil
	}
	row.Mu = capacityThroughput(o.Instance.Profile, o.Instance.MaxBatch, in.DispatchOccupancy.PerRequest)
	r := &capacityReplay{settings: settings, node: node, at: at, baselines: baselines, occupancy: *in.DispatchOccupancy, option: o, w: w, gaps: map[[2]int][]float64{}}
	b := capacityBuilder{settings: settings, percentile: w.SLO.Percentile, replay: r.window}
	if err := b.build(ctx, &row); err != nil {
		return row, err
	}
	row.BuiltAt, row.Elapsed = time.Now().UTC(), time.Since(began)
	return row, nil
}

// build judges every grid rate 0.05k mu. Met rates followed by missed rates
// give kappa: the last met grid rate bisected toward the next one to 0.5% of
// mu (0 when 0.05 mu misses, mu when every rate is met). A met rate after a
// missed one rejects the row as NonMonotone without kappa.
func (b *capacityBuilder) build(ctx context.Context, row *CapacityRow) error {
	met := 0
	for k := 1; k <= capacityGridSteps; k++ {
		j, err := b.judge(ctx, capacityGridStep(k, row.Mu))
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
	kappa := capacityGridStep(met, row.Mu)
	if met > 0 && met < capacityGridSteps {
		hi := capacityGridStep(met+1, row.Mu)
		for hi-kappa > capacityBisectionWidth*row.Mu {
			mid := (kappa + hi) / 2
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

func capacityGridStep(k int, mu float64) float64 { return float64(k) / capacityGridSteps * mu }

// requests is the window population: MinimumRequests, capacityTailSamples
// above the SLO percentile, and at least CapacityWindow of arrivals.
func (b *capacityBuilder) requests(lambda float64) int {
	n := max(b.settings.CapacityMinimumRequests, int(math.Ceil(lambda*b.settings.CapacityWindow.Seconds())))
	if b.percentile < 100 {
		n = max(n, int(math.Ceil(capacityTailSamples/(1-b.percentile/100))))
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
	estimate, err := schedulerReplayEstimate(ctx, settings, snapshot, keep, uint64(seed), map[NodeReference]bool{})
	if err != nil {
		return 0, false, err
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
	running  map[string]bool
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
	return &CapacityTable{rows: map[string]CapacityRow{}, running: map[string]bool{}, previous: map[string]CapacityRow{}, asked: map[string]bool{},
		parallel: make(chan struct{}, max(1, parallel)), record: record, load: load}
}

// Ensure starts every missing row for the autonomous Workloads of snapshot and
// returns at once. ctx bounds the started rows.
func (t *CapacityTable) Ensure(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot) {
	wanted := []CapacityInput{}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, w := range snapshot.Workloads {
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
				if _, built := t.rows[key]; built || t.running[key] {
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
				t.running[key] = true
				t.wg.Add(1)
				go t.build(ctx, settings, snapshot.DispatchOccupancy, o, w, node, snapshot.Baselines, snapshot.At, key)
			}
		}
	}
	t.wanted = wanted
	missing := []string{}
	for _, in := range wanted {
		if key := in.Key(); t.running[key] && !t.asked[key] {
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

func (t *CapacityTable) build(ctx context.Context, settings SchedulerSettings, occupancy []DispatchOccupancy, o SchedulerOption, w *Definition, node SchedulerNode, baselines []NodeBaseline, at time.Time, key string) {
	defer t.wg.Done()
	select {
	case t.parallel <- struct{}{}:
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.running, key)
		t.mu.Unlock()
		return
	}
	row, err := BuildCapacityRow(ctx, settings, occupancy, o, w, node, baselines, at)
	<-t.parallel
	if err != nil {
		if ctx.Err() != nil {
			t.mu.Lock()
			delete(t.running, key)
			t.mu.Unlock()
			return
		}
		row.Status, row.Reason, row.Kappa, row.BuiltAt = CapacityRejected, err.Error(), nil, time.Now().UTC()
	}
	t.mu.Lock()
	t.rows[key] = row
	delete(t.running, key)
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
