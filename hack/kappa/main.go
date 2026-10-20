// Command kappa prototypes the per-option capacity table: kappa is the largest
// Poisson request rate one instance sustains while the existing scheduler replay
// (serving.EvaluateScheduler on a one-instance keep snapshot) meets the Workload
// SLO percentile in at least 95% of independent windows. It also replays
// recorded decisions with a node-wise planner that sums kappa and compares its
// choice with the recorded replay target, and with the replay planner on one
// recorded snapshot at swept Poisson loads.
//
//	go run ./hack/kappa -mode table    -scheduler scheduler-pre.json [-margin 139] [-window 60]
//	go run ./hack/kappa -mode validate -scheduler scheduler-pre.json -options mnv2-rasp5-cpu-q1-ed628c8:1 [-scale 2.82]
//	go run ./hack/kappa -mode compare  -scheduler scheduler-pre.json -profiles profiles-pre.json -records records.jsonl [-kappa-cache k.json] [-sweep 5,40,80]
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"gonum.org/v1/gonum/stat"
)

func main() {
	mode := flag.String("mode", "table", "table, validate or compare")
	schedulerFile := flag.String("scheduler", "", "ChillServer /api/v1/scheduler dump; options carry profile bodies")
	profilesFile := flag.String("profiles", "", "ChillServer /api/v1/scheduler/profiles dump (compare)")
	recordsFile := flag.String("records", "", "scheduler record JSONL with decision rows (compare)")
	margin := flag.Float64("margin", -1, "completion margin ms; negative uses the settings of the input")
	options := flag.String("options", "", "validate: comma-separated <profile condition name>:<max batch>")
	scale := flag.Float64("scale", 1, "validate: factor applied to the selected profiles' service samples")
	seeds := flag.Int("seeds", 20, "independent arrival windows judged at each bisection rate")
	attainment := flag.Float64("attainment", 0.95, "fraction of those windows that must meet the SLO")
	window := flag.Float64("window", 0, "seconds of arrivals a window covers at least")
	cache := flag.String("kappa-cache", "", "compare: kappa table JSON, read when present, written after a build")
	sweep := flag.String("sweep", "", "compare: comma-separated loads (req/s) for the replay planner comparison")
	flag.Parse()
	base, settings, err := loadScheduler(*schedulerFile)
	if err == nil {
		if *margin >= 0 {
			settings.CompletionMarginMS = *margin
		}
		e := &engine{base: base, settings: settings, seeds: *seeds, need: int(math.Ceil(*attainment * float64(*seeds))), window: *window, gaps: map[uint64][]float64{}}
		switch *mode {
		case "table":
			err = e.table()
		case "validate":
			err = e.validate(*options, *scale)
		case "compare":
			err = compare(e, *profilesFile, *recordsFile, *margin, *cache, *sweep)
		default:
			err = fmt.Errorf("unknown mode %q", *mode)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadScheduler(name string) (serving.SchedulerSnapshot, serving.SchedulerSettings, error) {
	var dump struct {
		Settings serving.SchedulerSettings `json:"settings"`
		State    struct {
			Snapshot serving.SchedulerSnapshot `json:"snapshot"`
		} `json:"state"`
	}
	data, err := os.ReadFile(name)
	if err == nil {
		err = json.Unmarshal(data, &dump)
	}
	return dump.State.Snapshot, dump.Settings, err
}

// engine evaluates one-instance snapshots with the production replay.
type engine struct {
	base     serving.SchedulerSnapshot
	settings serving.SchedulerSettings
	seeds    int                  // independent windows judged per rate
	need     int                  // windows that must meet the SLO
	window   float64              // seconds a window covers at least; 0 keeps the request count alone
	gaps     map[uint64][]float64 // unit-rate exponential gaps per arrival seed
}

// requests gives the replayed population: 100 samples above the percentile,
// and at least the configured window of arrivals at rate lambda.
func (e *engine) requests(w *serving.Definition, lambda float64) int {
	return max(1000, int(math.Ceil(100/(1-w.SLO.Percentile/100))), int(math.Ceil(lambda*e.window)))
}

// throughput is the largest service rate over the batch sizes up to b (req/s).
func throughput(p *serving.Profile, b int) float64 {
	mu := 0.0
	for _, batch := range p.Batches {
		if batch.Batch <= b {
			mu = max(mu, float64(batch.Batch)/stat.Mean(batch.DurationNanos, nil)*1e9)
		}
	}
	return mu
}

// attain replays option o with max batch b for Workload w at Poisson rate
// lambda and returns the replayed SLO-percentile latency (ms, margin included)
// and whether the SLO holds (percentile within target, no timeout or expiry).
func (e *engine) attain(o serving.SchedulerOption, b int, w *serving.Definition, lambda float64, n int, seed uint64) (float64, bool, error) {
	gaps := e.gaps[seed<<32|uint64(n)]
	if gaps == nil {
		rng := rand.New(rand.NewPCG(seed, 1))
		gaps = make([]float64, n)
		for j := range gaps {
			gaps[j] = rng.ExpFloat64()
		}
		e.gaps[seed<<32|uint64(n)] = gaps
	}
	at := e.base.At
	offsets := make([]time.Duration, n)
	t := 0.0
	for j, g := range gaps {
		t += g / lambda
		offsets[j] = time.Duration(t * 1e9)
	}
	window := offsets[n-1] + time.Millisecond
	start := at.Add(-window)
	arrivals := make([]serving.SchedulerArrival, n)
	for j, offset := range offsets {
		arrivals[j] = serving.SchedulerArrival{ID: fmt.Sprintf("a%07d", j), At: start.Add(offset), Definition: w, Accepted: true, Compute: true, WorkloadUID: w.UID}
	}
	node := serving.SchedulerNode{Node: o.Instance.Node, CPUs: o.Instance.Allocation.CPUs, MemoryBytes: o.Instance.Allocation.MemoryBytes}
	if gpu := o.Instance.Allocation.GPUUUID; gpu != "" {
		node.GPUUUIDs = []string{gpu}
	}
	for _, n := range e.base.Nodes {
		if n.Node.UID == o.Instance.Node.UID {
			node = n
		}
	}
	node.PowerMode, node.Ready, node.Measurement, node.Reasons, node.ObservedAt = o.Instance.Profile.Condition.PowerMode, true, false, nil, at
	instance := o.Instance
	instance.ID, instance.TargetID, instance.OwnerWorkloadUID = "kappa", "", ""
	instance.WorkloadUIDs, instance.Ready, instance.Stopped, instance.MaxBatch = []string{w.UID}, true, false, b
	instance.Weight, instance.ObservedAt, instance.ArrivalAges = 1, at, nil
	snapshot := serving.SchedulerSnapshot{
		At: at, ObservationEnd: at, Workloads: []*serving.Definition{w}, Nodes: []serving.SchedulerNode{node},
		Instances: []serving.SchedulerInstance{instance}, Routing: map[string]map[string]int32{w.UID: {"kappa": 1}},
		Arrivals: arrivals, Options: []serving.SchedulerOption{o}, Baselines: e.base.Baselines,
	}
	settings := e.settings
	settings.Algorithm, settings.AutonomousWorkloads = serving.SchedulerAlgorithmFixedHorizon, nil
	settings.ObservationWindow, settings.Horizon, settings.MaxObservationAge = window, window, time.Hour
	settings.Replays, settings.Seed, settings.MaxReplayRequests = 1, int64(seed), n+1
	decision, err := serving.EvaluateScheduler(context.Background(), settings, snapshot)
	if err != nil {
		return 0, false, err
	}
	if len(decision.Keep.Reasons) != 0 {
		return 0, false, fmt.Errorf("%s b%d: %v", o.Instance.Profile.Condition.Name, b, decision.Keep.Reasons)
	}
	for _, estimate := range decision.Keep.SLO {
		if estimate.UID != w.UID || estimate.ExpectedNanos == nil {
			continue
		}
		failed := 0
		for kind, count := range estimate.PredictedOutcomes {
			if kind == "timeout" || strings.HasPrefix(kind, "expired-") {
				failed += count
			}
		}
		return *estimate.ExpectedNanos / 1e6, failed == 0 && *estimate.ExpectedNanos <= float64(w.SLO.TargetLatency), nil
	}
	return 0, false, fmt.Errorf("%s b%d: no SLO estimate", o.Instance.Profile.Condition.Name, b)
}

// meets reports whether at least e.need of the arrival seeds 1..e.seeds meet
// the SLO, each an independent window starting empty.
func (e *engine) meets(o serving.SchedulerOption, b int, w *serving.Definition, lambda float64) (bool, error) {
	passed, n := 0, e.requests(w, lambda)
	for seed := 1; seed <= e.seeds && passed < e.need && passed+e.seeds-seed+1 >= e.need; seed++ {
		_, ok, err := e.attain(o, b, w, lambda, n, uint64(seed))
		if err != nil {
			return false, err
		}
		if ok {
			passed++
		}
	}
	return passed >= e.need, nil
}

// kappa bisects the Poisson rate on [0.01 mu, mu] to 0.5% of mu with common
// random numbers. It returns 0 when 0.01 mu already misses the SLO.
func (e *engine) kappa(o serving.SchedulerOption, b int, w *serving.Definition) (float64, error) {
	mu := throughput(o.Instance.Profile, b)
	lo, hi := 0.01*mu, mu
	if ok, err := e.meets(o, b, w, lo); err != nil || !ok {
		return 0, err
	}
	if ok, err := e.meets(o, b, w, hi); err != nil || ok {
		return hi, err
	}
	for hi-lo > 0.005*mu {
		mid := (lo + hi) / 2
		ok, err := e.meets(o, b, w, mid)
		if err != nil {
			return 0, err
		}
		if ok {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo, nil
}

// variants lists the measured max-batch variants of the snapshot options, as
// the enumerator does with Controls.Batching.
func variants(options []serving.SchedulerOption) []serving.SchedulerOption {
	out := []serving.SchedulerOption{}
	for _, o := range options {
		if o.Instance.Profile == nil {
			continue
		}
		for _, b := range o.Instance.Profile.Batches {
			if b.Batch <= o.Instance.MaxBatch {
				v := o
				v.Instance.MaxBatch = b.Batch
				out = append(out, v)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Instance, out[j].Instance
		if a.Profile.Condition.Name != b.Profile.Condition.Name {
			return a.Profile.Condition.Name < b.Profile.Condition.Name
		}
		return a.MaxBatch < b.MaxBatch
	})
	return out
}

func (e *engine) table() error {
	fmt.Printf("completion margin %g ms\n\n| option | b | mu (req/s) |", e.settings.CompletionMarginMS)
	for _, w := range e.base.Workloads {
		fmt.Printf(" kappa %s p%g %v |", w.Name, w.SLO.Percentile, w.SLO.TargetLatency)
	}
	fmt.Printf(" CPU s |\n|---|---|---|%s---|\n", strings.Repeat("---|", len(e.base.Workloads)))
	began := time.Now()
	for _, o := range variants(e.base.Options) {
		rowBegan := time.Now()
		fmt.Printf("| %s | %d | %.1f |", o.Instance.Profile.Condition.Name, o.Instance.MaxBatch, throughput(o.Instance.Profile, o.Instance.MaxBatch))
		for _, w := range e.base.Workloads {
			k, err := e.kappa(o, o.Instance.MaxBatch, w)
			if err != nil {
				return err
			}
			fmt.Printf(" %.1f |", k)
		}
		fmt.Printf(" %.1f |\n", time.Since(rowBegan).Seconds())
	}
	fmt.Printf("\ntotal %.1f s\n", time.Since(began).Seconds())
	return nil
}

// validate replays each selected option at 0.8, 0.9, 1.0 and 1.2 kappa in 20
// fresh windows (seeds after the bisection seeds), each the bisection population.
func (e *engine) validate(selection string, scale float64) error {
	byName := map[string]serving.SchedulerOption{}
	for _, o := range e.base.Options {
		if o.Instance.Profile != nil {
			byName[o.Instance.Profile.Condition.Name] = o
		}
	}
	fmt.Printf("completion margin %g ms, service scale %g, kappa from %d of %d windows\n\n| option | b | Workload | kappa | x | p-latency ms median / max (target) | windows meeting SLO |\n|---|---|---|---|---|---|---|\n", e.settings.CompletionMarginMS, scale, e.need, e.seeds)
	for _, item := range strings.Split(selection, ",") {
		name, batch, _ := strings.Cut(item, ":")
		b, err := strconv.Atoi(batch)
		o, ok := byName[name]
		if err != nil || !ok {
			return fmt.Errorf("option %q: %v", item, err)
		}
		if scale != 1 {
			p := *o.Instance.Profile
			p.Batches = slices.Clone(p.Batches)
			for j := range p.Batches {
				p.Batches[j].DurationNanos = slices.Clone(p.Batches[j].DurationNanos)
				for k := range p.Batches[j].DurationNanos {
					p.Batches[j].DurationNanos[k] *= scale
				}
			}
			o.Instance.Profile = &p
		}
		for _, w := range e.base.Workloads {
			k, err := e.kappa(o, b, w)
			if err != nil {
				return err
			}
			if k == 0 {
				fmt.Printf("| %s | %d | %s | 0 | - | - | - |\n", name, b, w.Name)
				continue
			}
			for _, x := range []float64{0.8, 0.9, 1, 1.2} {
				latencies, met := []float64{}, 0
				for seed := e.seeds + 1; seed <= e.seeds+20; seed++ {
					q, ok, err := e.attain(o, b, w, x*k, e.requests(w, x*k), uint64(seed))
					if err != nil {
						return err
					}
					latencies = append(latencies, q)
					if ok {
						met++
					}
				}
				sort.Float64s(latencies)
				fmt.Printf("| %s | %d | %s | %.1f | %.1f | %.0f / %.0f (%v) | %d/20 |\n", name, b, w.Name, k, x, stat.Quantile(0.5, stat.Empirical, latencies, nil), latencies[len(latencies)-1], w.SLO.TargetLatency, met)
			}
		}
	}
	return nil
}

// --- node-wise planner over kappa ---

type record struct {
	At       time.Time                  `json:"at"`
	Decision *serving.SchedulerDecision `json:"decision"`
	Snapshot *serving.SchedulerSnapshot `json:"snapshot"`
}

type op struct{ mean, max, joules float64 } // seconds, seconds, wall J

// plan is one node's final controlled instances and its transition from now.
type plan struct {
	instances []serving.SchedulerInstance
	kappa     float64
	fill      []float64 // per-instance e(b) sorted ascending, with its kappa
	fillK     []float64
	keep      bool
	pre, post op // chain before the route (power, freeing removes, prepares) and removes after it
	before    float64
	mid       float64 // watts from the end of the pre-route chain to the route
	after     float64
}

// energy is the node's joules over [0,T] when the route lands at r.
func (p *plan) energy(r, t float64) float64 {
	if p.keep {
		return p.before * t
	}
	return p.pre.joules + p.mid*(r-p.pre.mean) + p.post.joules + p.after*(t-r-p.post.mean)
}

// serve is the request energy of load y routed to the cheapest-e instances first.
func (p *plan) serve(y, t float64) float64 {
	j := 0.0
	for i, e := range p.fill {
		take := min(y, p.fillK[i])
		j += take * e * t
		y -= take
	}
	return j
}

type planner struct {
	snap       *serving.SchedulerSnapshot
	settings   serving.SchedulerSettings
	workload   *serving.Definition
	kappa      map[string]float64 // profileID/b
	energyPerB map[string]float64
}

func optionKey(i serving.SchedulerInstance) string {
	return fmt.Sprintf("%s/%d", profileID(i), i.MaxBatch)
}

func profileID(i serving.SchedulerInstance) string {
	if i.Profile != nil {
		return i.Profile.ID
	}
	return i.ProfileID
}

func instanceKey(i serving.SchedulerInstance) string {
	return fmt.Sprintf("%s|%s|%v|%s|b%d", i.Node.Name, profileID(i), i.Allocation.CPUs, i.Allocation.GPUUUID, i.MaxBatch)
}

func configKey(instances []serving.SchedulerInstance) string {
	keys := []string{}
	for _, i := range instances {
		keys = append(keys, instanceKey(i))
	}
	sort.Strings(keys)
	return strings.Join(keys, " + ")
}

// power mirrors the replay baseline: the exact loaded baseline when measured,
// else the unloaded baseline plus each profile's loaded idle minus unloaded.
// ponytail: hack-side mirror of the unexported schedulerBaseline; the planner calls it directly.
func (pl *planner) power(node serving.SchedulerNode, mode string, instances []serving.SchedulerInstance) (float64, bool) {
	state := []string{}
	for _, i := range instances {
		state = append(state, serving.SchedulerInstanceState(i))
	}
	sort.Strings(state)
	var matched, unloaded *serving.NodeBaseline
	for j := range pl.snap.Baselines {
		b := &pl.snap.Baselines[j]
		if b.Node != node.Node || b.PowerMode != mode || b.Scope != "node-exclusive-wall" || b.Meter == "" || b.ID == "" {
			continue
		}
		bs := slices.Clone(b.State)
		sort.Strings(bs)
		if slices.Equal(bs, state) && (matched == nil || b.MeasuredAt.After(matched.MeasuredAt)) {
			matched = b
		}
		if len(bs) == 0 && (unloaded == nil || b.MeasuredAt.After(unloaded.MeasuredAt)) {
			unloaded = b
		}
	}
	if matched != nil {
		return matched.Watts, true
	}
	if unloaded == nil {
		return 0, false
	}
	watts := unloaded.Watts
	for _, i := range instances {
		if i.Profile == nil || i.Profile.Condition.PowerMode != mode {
			return 0, false
		}
		watts += i.Profile.IdleBaselineWatts - unloaded.Watts
	}
	return watts, true
}

// cost picks the first matching operation cost, like selectCosts, and returns
// its mean and max trial duration and mean wall joules.
func (pl *planner) cost(node serving.NodeReference, kind, from, to string, match func(serving.OperationCost) bool) (op, bool) {
	for _, c := range pl.snap.Costs {
		if c.Node != node || c.Kind != kind || c.From != from || c.To != to || !match(c) || c.Meter == "" || c.Scope != "node-exclusive-wall" || len(c.Trials) == 0 {
			continue
		}
		o := op{}
		for _, t := range c.Trials {
			d := t.Duration.Seconds()
			j := t.Joules
			if c.EnergyScope == "target-baseline-delta" && c.SubtractedWatts != nil {
				j += *c.SubtractedWatts * d
			}
			o.mean += d / float64(len(c.Trials))
			o.joules += j / float64(len(c.Trials))
			o.max = max(o.max, d)
		}
		return o, true
	}
	return op{}, false
}

func (o op) then(next op) op {
	return op{o.mean + next.mean, o.max + next.max, o.joules + next.joules}
}

func states(instances []serving.SchedulerInstance) []string {
	out := []string{}
	for _, i := range instances {
		out = append(out, serving.SchedulerInstanceState(i))
	}
	sort.Strings(out)
	return out
}

// chain builds the node transition from (m0, current) to the plan instances,
// following the schedulerPlan order. It reports false when a cost is missing.
func (pl *planner) chain(node serving.SchedulerNode, m0 string, current []serving.SchedulerInstance, p *plan) bool {
	m1 := m0
	if len(p.instances) > 0 {
		m1 = p.instances[0].Profile.Condition.PowerMode
	}
	var ok bool
	if p.before, ok = pl.power(node, m0, current); !ok {
		return false
	}
	if p.after, ok = pl.power(node, m1, p.instances); !ok {
		return false
	}
	remove := func(i serving.SchedulerInstance, mode string) (op, bool) {
		return pl.cost(node.Node, "remove", serving.SchedulerInstanceState(i), "unloaded", func(c serving.OperationCost) bool { return c.PowerMode == mode })
	}
	prepare := func(i serving.SchedulerInstance, mode string) (op, bool) {
		return pl.cost(node.Node, "prepare", "unloaded", serving.SchedulerInstanceState(i), func(c serving.OperationCost) bool { return c.PowerMode == mode })
	}
	if m1 != m0 {
		before := states(current)
		power, ok := pl.cost(node.Node, "power", m0, m1, func(c serving.OperationCost) bool {
			witness := slices.Clone(c.BeforeState)
			sort.Strings(witness)
			return slices.Equal(witness, before)
		})
		if !ok {
			return false
		}
		p.pre = power
		for _, i := range current {
			o, ok := remove(i, m1)
			if !ok {
				return false
			}
			p.pre = p.pre.then(o)
		}
		for _, i := range p.instances {
			o, ok := prepare(i, m1)
			if !ok {
				return false
			}
			p.pre = p.pre.then(o)
		}
		p.mid = p.after
		return true
	}
	kept := map[string]bool{}
	for _, i := range p.instances {
		kept[instanceKey(i)] = true
	}
	added, removed, union := []serving.SchedulerInstance{}, []serving.SchedulerInstance{}, slices.Clone(current)
	have := map[string]bool{}
	for _, i := range current {
		have[instanceKey(i)] = true
		if !kept[instanceKey(i)] {
			removed = append(removed, i)
		}
	}
	for _, i := range p.instances {
		if !have[instanceKey(i)] {
			added = append(added, i)
			union = append(union, i)
		}
	}
	// Removes free resources before the prepares only when both cannot coexist.
	early := len(serving.ValidateNodeBudget(node, allocations(node, union), nil)) != 0
	for _, i := range removed {
		o, ok := remove(i, m0)
		if !ok {
			return false
		}
		if early {
			p.pre = p.pre.then(o)
		} else {
			p.post = p.post.then(o)
		}
	}
	for _, i := range added {
		o, ok := prepare(i, m0)
		if !ok {
			return false
		}
		p.pre = p.pre.then(o)
	}
	p.mid = p.before
	if len(added) > 0 {
		if early {
			p.mid = p.after
		} else if p.mid, ok = pl.power(node, m0, union); !ok {
			return false
		}
	}
	return true
}

func allocations(node serving.SchedulerNode, instances []serving.SchedulerInstance) []serving.SchedulerAllocation {
	out := append([]serving.SchedulerAllocation{node.Reserved}, node.NonServing...)
	for _, i := range instances {
		out = append(out, i.Allocation)
	}
	return out
}

func (pl *planner) annotate(p *plan) bool {
	type item struct{ e, k float64 }
	items := []item{}
	for _, i := range p.instances {
		k, ok := pl.kappa[optionKey(i)]
		if !ok || k <= 0 {
			return false
		}
		items = append(items, item{pl.energyPerB[optionKey(i)], k})
		p.kappa += k
	}
	sort.Slice(items, func(a, b int) bool { return items[a].e < items[b].e })
	for _, it := range items {
		p.fill, p.fillK = append(p.fill, it.e), append(p.fillK, it.k)
	}
	return true
}

// plans lists node plans: keep, and on a placeable Node every set of measured
// option variants (one variant per option) that fits the Node budget at one mode.
//
//nolint:gocyclo // Plan enumeration checks mode, budget and costs in one ordered pass.
func (pl *planner) plans(node serving.SchedulerNode, options []serving.SchedulerOption) []*plan {
	current, fixed := []serving.SchedulerInstance{}, false
	for _, i := range pl.snap.Instances {
		if i.Node != node.Node {
			continue
		}
		current = append(current, i)
		if !slices.Contains(i.WorkloadUIDs, pl.workload.UID) {
			fixed = true
		}
	}
	m0 := node.PowerMode
	for _, a := range pl.snap.Actions {
		if a.Fixed && a.Node == node.Node && (a.Status == "planned" || a.Status == "running") {
			m0 = a.To
		}
	}
	available := node.Ready && !node.Measurement && !slices.Contains(node.Reasons, serving.NodePowerModeUnavailable) && len(current) == 0
	if lowest := serving.SchedulerLowestMode(pl.snap.Baselines, node.Node); available && lowest != "" {
		m0 = lowest // the common lowering every candidate shares
	}
	keep := &plan{instances: current, keep: true}
	if ok := pl.annotate(keep); !ok && len(current) > 0 {
		keep.kappa, keep.fill, keep.fillK = 0, nil, nil
	}
	keep.before, _ = pl.power(node, m0, current)
	out := []*plan{keep}
	placeable := node.Ready && !node.Measurement && !fixed && !slices.Contains(node.Reasons, serving.NodePlacementInventoryMissing) && !slices.Contains(node.Reasons, serving.NodePlacementBudgetUnavailable)
	if !placeable {
		return out
	}
	byOption := map[string][]serving.SchedulerOption{}
	names := []string{}
	for _, o := range options {
		if o.Instance.Node != node.Node || !slices.Contains(o.CompatibleUIDs, pl.workload.UID) {
			continue
		}
		name := o.Instance.Profile.ID
		if byOption[name] == nil {
			names = append(names, name)
		}
		byOption[name] = append(byOption[name], o)
	}
	keepKey := configKey(current)
	var walk func(j int, chosen []serving.SchedulerInstance)
	walk = func(j int, chosen []serving.SchedulerInstance) {
		if j == len(names) {
			if configKey(chosen) == keepKey || (len(chosen) == 0 && len(current) == 0) || len(chosen) > pl.settings.MaxReplicas {
				return
			}
			for _, i := range chosen {
				if i.Profile.Condition.PowerMode != chosen[0].Profile.Condition.PowerMode {
					return
				}
			}
			if len(serving.ValidateNodeBudget(node, allocations(node, chosen), nil)) != 0 {
				return
			}
			p := &plan{instances: slices.Clone(chosen)}
			if pl.annotate(p) && pl.chain(node, m0, current, p) {
				out = append(out, p)
			}
			return
		}
		walk(j+1, chosen)
		for _, o := range byOption[names[j]] {
			i := o.Instance
			i.ID = "plan-" + instanceKey(i)
			i.WorkloadUIDs = []string{pl.workload.UID}
			for _, c := range current {
				if instanceKey(c) == instanceKey(i) {
					i = c
				}
			}
			walk(j+1, append(chosen, i))
		}
	}
	walk(0, nil)
	return out
}

type choice struct {
	instances []serving.SchedulerInstance
	joules    float64
	route     float64
	period    float64
	plans     int
}

// period is the comparison period T = max(H, longest chain in the plan space):
// the longest pre-route chain plus the longest post-route removal, max trials.
func (pl *planner) period(nodes [][]*plan) float64 {
	pre, post := 0.0, 0.0
	for _, ps := range nodes {
		for _, p := range ps {
			pre, post = max(pre, p.pre.max), max(post, p.post.max)
		}
	}
	return max(pl.settings.Horizon.Seconds(), pre+post)
}

// choose builds every node's plans and picks one per node with solve.
func (pl *planner) choose(load float64) choice {
	options := variants(pl.snap.Options)
	nodes, count := [][]*plan{}, 0
	for _, n := range pl.snap.Nodes {
		ps := pl.plans(n, options)
		nodes = append(nodes, ps)
		count += len(ps)
	}
	period := pl.period(nodes)
	picks, joules, route := solve(nodes, load, period)
	c := choice{joules: joules, route: route, period: period, plans: count}
	for n, j := range picks {
		c.instances = append(c.instances, nodes[n][j].instances...)
	}
	return c
}

// solve picks one plan per node with sum kappa >= load (at least one instance
// at zero load), ordered by the transition backlog max(0, load - keep kappa) * r
// (requests queued until the route at r), then by sum_n E_n(T) + request energy.
// Plan 0 of every node is keep, which wins ties. A DP over 50 load units runs
// once per distinct route time r; nil picks means infeasible.
// ponytail: backlog assumes make-before-break keeps all keep capacity until the
// route; a retained-capacity DP dimension covers early removals (mode changes).
func solve(nodes [][]*plan, load, period float64) ([]int, float64, float64) {
	units, unit := 50, load/50
	if load == 0 {
		units, unit = 1, 0
	}
	capacity := func(p *plan) int {
		if len(p.instances) == 0 || p.kappa <= 0 {
			return 0
		}
		if unit == 0 {
			return 1
		}
		return min(units, int(p.kappa/unit))
	}
	var picks []int
	best, bestRoute, bestBacklog := math.Inf(1), 0.0, math.Inf(1)
	keepJ, keepCap, keepKappa, rest := 0.0, 0, 0.0, load
	for _, ps := range nodes {
		take := min(rest, ps[0].kappa)
		keepJ += ps[0].energy(0, period) + ps[0].serve(take, period)
		keepCap += capacity(ps[0])
		keepKappa += ps[0].kappa
		rest -= take
	}
	if keepCap >= units {
		picks, best, bestBacklog = make([]int, len(nodes)), keepJ, 0
	}
	routes := []float64{0}
	for _, ps := range nodes {
		for _, p := range ps {
			routes = append(routes, p.pre.mean)
		}
	}
	sort.Float64s(routes)
	// State: load units assigned plus whether some chosen chain ends exactly at
	// r, so r is the actual route time (energy need not grow with r).
	type step struct{ plan, from int }
	width := units + 1
	for _, r := range slices.Compact(routes) {
		dp := make([]float64, 2*width)
		for s := range dp {
			dp[s] = math.Inf(1)
		}
		if r == 0 {
			dp[width] = 0
		} else {
			dp[0] = 0
		}
		steps := make([][]step, len(nodes))
		for n, ps := range nodes {
			next := make([]float64, 2*width)
			for s := range next {
				next[s] = math.Inf(1)
			}
			steps[n] = make([]step, 2*width)
			for s, v := range dp {
				if math.IsInf(v, 1) {
					continue
				}
				for j, p := range ps {
					if p.pre.mean > r {
						continue
					}
					hit, used := s/width, s%width
					if p.pre.mean == r {
						hit = 1
					}
					e := v + p.energy(r, period)
					for y := 0; y <= min(capacity(p), units-used); y++ {
						if c := e + p.serve(float64(y)*unit, period); c < next[hit*width+used+y] {
							next[hit*width+used+y] = c
							steps[n][hit*width+used+y] = step{j, s}
						}
					}
				}
			}
			dp = next
		}
		backlog := max(0, load-keepKappa) * r
		if end := 2*width - 1; !math.IsInf(dp[end], 1) && (backlog < bestBacklog-1e-9 || backlog < bestBacklog+1e-9 && dp[end] < best-1e-6) {
			best, bestRoute, bestBacklog, picks = dp[end], r, backlog, make([]int, len(nodes))
			s := end
			for n := len(nodes) - 1; n >= 0; n-- {
				picks[n], s = steps[n][s].plan, steps[n][s].from
			}
		}
	}
	return picks, best, bestRoute
}

// configJoules evaluates a given configuration under the same model, or NaN
// when one of its node plans is outside the plan space.
func (pl *planner) configJoules(instances []serving.SchedulerInstance, load float64) float64 {
	want := map[string][]serving.SchedulerInstance{}
	for _, i := range instances {
		want[i.Node.UID] = append(want[i.Node.UID], i)
	}
	options := variants(pl.snap.Options)
	nodes, chosen, route := [][]*plan{}, []*plan{}, 0.0
	for _, n := range pl.snap.Nodes {
		ps := pl.plans(n, options)
		nodes = append(nodes, ps)
		var match *plan
		for _, p := range ps {
			if configKey(p.instances) == configKey(want[n.Node.UID]) {
				match = p
			}
		}
		if match == nil {
			return math.NaN()
		}
		chosen = append(chosen, match)
		route = max(route, match.pre.mean)
	}
	period := pl.period(nodes)
	j := 0.0
	for _, p := range chosen {
		take := min(load, p.kappa)
		j += p.energy(route, period) + p.serve(take, period)
		load -= take
	}
	return j
}

//nolint:gocyclo // One offline comparison pass over recorded decisions, kept in record order.
func compare(e *engine, profilesFile, recordsFile string, margin float64, cache string, sweep string) error {
	var catalog struct {
		Profiles []serving.Profile `json:"profiles"`
	}
	data, err := os.ReadFile(profilesFile)
	if err == nil {
		err = json.Unmarshal(data, &catalog)
	}
	file, openErr := os.Open(recordsFile)
	if err = errors.Join(err, openErr); err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	profiles := map[string]*serving.Profile{}
	for j := range catalog.Profiles {
		profiles[catalog.Profiles[j].ID] = &catalog.Profiles[j]
	}
	hydrate := func(instances []serving.SchedulerInstance) {
		for j := range instances {
			if instances[j].Profile == nil {
				instances[j].Profile = profiles[instances[j].ProfileID]
			}
		}
	}
	kappa, energyPerB := map[string]float64{}, map[string]float64{}
	if data, err := os.ReadFile(cache); err == nil {
		if err := json.Unmarshal(data, &kappa); err != nil {
			return err
		}
	}
	var tableTime time.Duration
	var last struct {
		snap     *serving.SchedulerSnapshot
		settings serving.SchedulerSettings
		workload *serving.Definition
	}
	type row struct {
		at                 time.Time
		status, load       string
		recorded, chosen   string
		agree              bool
		modelDelta, replay float64
		period, route      float64
		plans              int
		tick               time.Duration
	}
	rows, gated := []row{}, 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<30)
	for scanner.Scan() {
		var r record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			return err
		}
		if r.Decision == nil || r.Snapshot == nil {
			continue
		}
		d, s := r.Decision, r.Snapshot
		if len(s.Reasons) != 0 {
			gated++ // snapshot reasons block every candidate in either planner
			continue
		}
		settings := d.Settings
		if margin >= 0 {
			settings.CompletionMarginMS = margin
		}
		if len(settings.AutonomousWorkloads) != 1 {
			continue // ponytail: one controlled Workload, as in E1; a load-vector DP covers several
		}
		var workload *serving.Definition
		for _, w := range s.Workloads {
			if w.UID == settings.AutonomousWorkloads[0] {
				workload = w
			}
		}
		for j := range s.Options {
			s.Options[j].Instance.Profile = profiles[s.Options[j].Instance.ProfileID]
		}
		hydrate(s.Instances)
		for _, o := range variants(s.Options) {
			if b := batchProfile(o.Instance.Profile, 1); b != nil {
				energyPerB[optionKey(o.Instance)] = b.EnergyPerInputJoules
			}
		}
		// The capacity table depends only on profiles and SLO settings; build it once.
		if len(kappa) == 0 {
			began := time.Now()
			e.settings = settings
			for _, o := range variants(s.Options) {
				if !slices.Contains(o.CompatibleUIDs, workload.UID) {
					continue
				}
				k, err := e.kappa(o, o.Instance.MaxBatch, workload)
				if err != nil {
					return err
				}
				kappa[optionKey(o.Instance)] = k
			}
			tableTime = time.Since(began)
			if cache != "" {
				data, _ := json.Marshal(kappa)
				if err := os.WriteFile(cache, data, 0o644); err != nil {
					return err
				}
			}
		}
		if d.Status == "Keep" {
			last.snap, last.settings, last.workload = s, settings, workload
		}
		load := 0.0
		for _, l := range d.Search.Loads {
			if l.UID == workload.UID {
				load = float64(l.ComputeInputs)
			}
		}
		load = (load + float64(len(s.Outstanding))) / settings.ObservationWindow.Seconds()
		pl := &planner{snap: s, settings: settings, workload: workload, kappa: kappa, energyPerB: energyPerB}
		began := time.Now()
		c := pl.choose(load)
		tick := time.Since(began)
		x := row{at: d.At, status: d.Status, load: fmt.Sprintf("%.2f", load), chosen: configKey(c.instances), period: c.period, route: c.route, plans: c.plans, tick: tick, modelDelta: math.NaN(), replay: math.NaN()}
		if d.Target != nil {
			target := d.Target.Instances
			x.recorded = configKey(target)
			x.agree = x.recorded == x.chosen
			if !x.agree {
				hydrate(target)
				x.modelDelta = c.joules - pl.configJoules(target, load)
				for _, candidate := range d.Candidates {
					if configKey(candidate.Instances) == x.chosen && candidate.ExpectedJoules != nil && d.Target.ExpectedJoules != nil {
						x.replay = *candidate.ExpectedJoules - *d.Target.ExpectedJoules
					}
				}
			}
		}
		rows = append(rows, x)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	fmt.Printf("capacity table: %d rows in %.1f s\n\n", len(kappa), tableTime.Seconds())
	keys := []string{}
	for k := range kappa {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  kappa %s = %.1f\n", k, kappa[k])
	}
	targets, agree := 0, 0
	ticks := []float64{}
	periods := map[float64]int{}
	for _, x := range rows {
		ticks = append(ticks, x.tick.Seconds()*1e3)
		periods[math.Round(x.period*10)/10]++
		if x.recorded == "" {
			continue
		}
		targets++
		if x.agree {
			agree++
		}
	}
	sort.Float64s(ticks)
	fmt.Printf("\ndecisions %d (+%d gated by snapshot reasons), with target %d, agree %d\n", len(rows), gated, targets, agree)
	if len(ticks) > 0 {
		fmt.Printf("tick ms p50 %.2f p99 %.2f max %.2f\n", stat.Quantile(0.5, stat.Empirical, ticks, nil), stat.Quantile(0.99, stat.Empirical, ticks, nil), ticks[len(ticks)-1])
	}
	fmt.Printf("comparison period T (s): %v\n\n| at | status | load | recorded | kappa choice | model dJ | replay dJ | T s | route s | plans |\n|---|---|---|---|---|---|---|---|---|---|\n", periods)
	choices := map[string]int{}
	for _, x := range rows {
		if x.recorded == "" {
			choices[x.chosen]++
		}
		if x.recorded == "" || x.agree && x.at != rows[0].at {
			continue
		}
		fmt.Printf("| %s | %s | %s | %s | %s | %.1f | %.1f | %.1f | %.1f | %d |\n", x.at.Format("15:04:05"), x.status, x.load, x.recorded, x.chosen, x.modelDelta, x.replay, x.period, x.route, x.plans)
	}
	fmt.Println("\nno-target decisions, kappa choice:")
	for k, v := range choices {
		fmt.Printf("  %d x %s\n", v, k)
	}
	if sweep != "" && last.snap != nil {
		return sweepLoads(last.snap, last.settings, last.workload, kappa, energyPerB, sweep)
	}
	return nil
}

// sweepLoads replaces the arrivals of one recorded keep snapshot with Poisson
// arrivals at each load and compares the kappa planner with the replay planner
// (serving.EvaluateScheduler, recorded settings) on the same snapshot.
func sweepLoads(s *serving.SchedulerSnapshot, settings serving.SchedulerSettings, w *serving.Definition, kappa, energyPerB map[string]float64, loads string) error {
	fmt.Printf("\nload sweep on the %s snapshot (%s)\n\n| load | kappa choice | kappa ms | replay target | replay status | replay s | replay dJ (kappa - target) | examined |\n|---|---|---|---|---|---|---|---|\n", s.At.Format("15:04:05"), configKey(s.Instances))
	for _, field := range strings.Split(loads, ",") {
		load, err := strconv.ParseFloat(field, 64)
		if err != nil {
			return err
		}
		pl := &planner{snap: s, settings: settings, workload: w, kappa: kappa, energyPerB: energyPerB}
		began := time.Now()
		chosen := configKey(pl.choose(load).instances)
		tick := time.Since(began)
		snapshot := *s
		snapshot.Arrivals = nil
		rng := rand.New(rand.NewPCG(1, 1))
		window := settings.ObservationWindow.Seconds()
		start := s.ObservationEnd.Add(-settings.ObservationWindow)
		for t := rng.ExpFloat64() / load; t < window; t += rng.ExpFloat64() / load {
			snapshot.Arrivals = append(snapshot.Arrivals, serving.SchedulerArrival{ID: fmt.Sprintf("a%07d", len(snapshot.Arrivals)), At: start.Add(time.Duration(t * 1e9)), Definition: w, Accepted: true, Compute: true, WorkloadUID: w.UID})
		}
		began = time.Now()
		d, err := serving.EvaluateScheduler(context.Background(), settings, snapshot)
		if err != nil {
			return err
		}
		elapsed := time.Since(began)
		target, delta := "-", math.NaN()
		if d.Target != nil {
			target = configKey(d.Target.Instances)
			for _, c := range d.Candidates {
				if configKey(c.Instances) == chosen && c.ExpectedJoules != nil && d.Target.ExpectedJoules != nil {
					delta = *c.ExpectedJoules - *d.Target.ExpectedJoules
				}
			}
		}
		fmt.Printf("| %g | %s | %.2f | %s | %s | %.1f | %.1f | %d |\n", load, chosen, tick.Seconds()*1e3, target, d.Status, elapsed.Seconds(), delta, d.Search.Examined)
	}
	return nil
}

func batchProfile(p *serving.Profile, size int) *serving.BatchProfile {
	for j := range p.Batches {
		if p.Batches[j].Batch == size {
			return &p.Batches[j]
		}
	}
	return nil
}
