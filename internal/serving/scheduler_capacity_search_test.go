package serving

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	v2 "github.com/lab-paper-code/chill/internal/scheduler/v2"
)

// evaluationOption uses the recorded energy/timing values with an explicit
// synthetic routing rate. These fixtures are not measured SLO evidence.
type evaluationOption struct {
	name                 string
	mu, e, idle          float64
	prepareS, prepareJ   float64
	plc, vlmCPU, vlmCUDA bool // derives the synthetic PLC / SmolVLM-like options
	unloaded, removeS    float64
	removeJ              float64
	node                 string
}

// evaluationNodes are the five nodes m4 r7 planned on (Jetsons excluded), with the
// unloaded node baseline and the measured remove chain of each node.
func evaluationNodes() [][]evaluationOption {
	node := func(name string, unloaded, removeS, removeJ float64, options ...evaluationOption) []evaluationOption {
		for k := range options {
			options[k].node, options[k].unloaded, options[k].removeS, options[k].removeJ = name, unloaded, removeS, removeJ
		}
		return options
	}
	return [][]evaluationOption{
		node("rasp5", 4.8866, 3.603, 18.092,
			evaluationOption{name: "q1", mu: 25.288, e: 0.07068, idle: 4.8985, prepareS: 19.256, prepareJ: 113.906, plc: true, vlmCPU: true},
			evaluationOption{name: "q1-lut", mu: 25.688, e: 0.07453, idle: 4.9041, prepareS: 20.190, prepareJ: 120.089},
			evaluationOption{name: "q2", mu: 29.300, e: 0.09886, idle: 4.8768, prepareS: 15.926, prepareJ: 101.666}),
		node("orangepi", 2.7264, 3.979, 11.378,
			evaluationOption{name: "q1", mu: 7.533, e: 0.09943, idle: 2.7128, prepareS: 36.859, prepareJ: 126.737, plc: true}),
		node("lattepanda", 9.3047, 3.695, 34.689,
			evaluationOption{name: "q1", mu: 30.067, e: 0.12079, idle: 9.4955, prepareS: 18.278, prepareJ: 202.782, plc: true, vlmCPU: true},
			evaluationOption{name: "q2", mu: 39.079, e: 0.13780, idle: 9.4869, prepareS: 15.764, prepareJ: 177.609}),
		node("genesis2", 54.0631, 4.361, 232.881,
			evaluationOption{name: "cpu-q1", mu: 82.901, e: 0.32292, idle: 54.8725, prepareS: 12.357, prepareJ: 756.903, plc: true, vlmCPU: true},
			evaluationOption{name: "cuda-q1", mu: 104.338, e: 0.26299, idle: 109.6394, prepareS: 13.159, prepareJ: 1164.312, vlmCUDA: true}),
		node("genesis3", 49.9728, 4.236, 211.444,
			evaluationOption{name: "cpu-q1", mu: 63.596, e: 0.27643, idle: 51.6730, prepareS: 13.110, prepareJ: 712.615, plc: true, vlmCPU: true},
			evaluationOption{name: "cpu-q2", mu: 69.151, e: 0.32284, idle: 49.7983, prepareS: 10.681, prepareJ: 635.085}),
	}
}

// evaluationScaled adds synthetic faster-service variants and copied nodes.
// The historical "-b2" suffix names a profile variant, not an actual batch-2
// chooser experiment. Modes scale μ and loaded watts by (1 − 0.15m), and
// preparation time by (1 + 0.1m).
func evaluationScaled(variants, modes int) [][]evaluationOption {
	nodes := evaluationNodes()
	if modes > 0 {
		for i, name := range []string{"jetsono", "jetsonx", "jetson-nano"} {
			k := []int{4, 0, 2}[i]
			copied := []evaluationOption{}
			for m := range modes {
				for _, o := range nodes[k] {
					f := 1 - 0.15*float64(m)
					o.node, o.name = name, fmt.Sprint(o.name, "@", m)
					o.mu, o.idle, o.prepareS = f*o.mu, o.unloaded+f*(o.idle-o.unloaded), (1+0.1*float64(m))*o.prepareS
					copied = append(copied, o)
				}
			}
			nodes = append(nodes, copied)
		}
	}
	for n := range nodes {
		for _, o := range slices.Clone(nodes[n]) {
			if variants > 1 {
				o.name, o.mu = o.name+"-b2", 1.3*o.mu
				nodes[n] = append(nodes[n], o)
			}
		}
	}
	return nodes
}

// All three workloads use synthetic deterministic raw service laws. Recorded
// node energy and transition timings remain unchanged.
var e5Workloads = []struct {
	name   string
	target time.Duration
}{{"mnv2", time.Second}, {"plc", time.Second}, {"vlm", 10 * time.Second}}

// e5Pair is one instance a node plan can hold: Workload w on an option.
type e5Pair struct {
	w   int
	id  string
	opt evaluationOption
}

func e5Pairs(options []evaluationOption) []e5Pair {
	pairs := []e5Pair{}
	add := func(w int, o evaluationOption, suffix string) {
		pairs = append(pairs, e5Pair{w: w, id: o.node + "/" + e5Workloads[w].name + "/" + o.name + suffix, opt: o})
	}
	for _, o := range options {
		add(0, o, "")
		if o.plc {
			p := o
			p.mu, p.e = 1.5*o.mu, 0.4*o.e
			add(1, p, "")
		}
		if o.vlmCPU || o.vlmCUDA {
			v := o
			v.mu, v.e, v.prepareS, v.prepareJ = o.mu/120, 240*o.e, 2*o.prepareS, 2*o.prepareJ
			if o.vlmCUDA {
				v.mu, v.e = o.mu/50, 150*o.e
			}
			add(2, v, "-vlm")
		}
	}
	return pairs
}

// e5Start retains the original five-node placement.
var e5Start = map[string]bool{
	"rasp5/mnv2/q1": true, "orangepi/mnv2/q1": true, "genesis3/mnv2/cpu-q2": true,
	"lattepanda/plc/q1": true, "genesis2/vlm/cuda-q1-vlm": true,
}

// e5Planner builds the node plans the planner would hold for the pairs from
// the current instances: keep, each pair alone (one model and one instance per
// Workload on a Node) and removing every instance. plans[n][0] keeps the
// current instances at μ-proportional weights.
// Removals run after the route.
func e5Planner(rates map[int]float64, nodes [][]evaluationOption, current map[string]bool) *capacityPlanner {
	defaults, base := schedulerFixture()
	s := capacityTestSettings
	s.DefaultOperationCost, s.DefaultIdlePower = defaults.DefaultOperationCost, defaults.DefaultIdlePower
	s.ObservationWindow = 10 * time.Second
	s.ControlInterval = time.Second
	s.ComparisonPeriod, s.MaxReplicas, s.MaxInstances, s.PlanSearchWorkers = 10*time.Minute, 8, 16, 3
	p := &capacityPlanner{ctx: context.Background(), settings: s, index: map[string]int{}}
	p.snapshot.At, p.snapshot.ObservationEnd = base.At, base.ObservationEnd
	p.keep.Routing = map[string]map[string]int32{}
	index := map[int]int{}
	for w := range e5Workloads {
		if rate, ok := rates[w]; ok {
			index[w] = len(p.ws)
			uid := e5Workloads[w].name
			p.index[uid] = len(p.ws)
			d := *base.Workloads[0]
			d.UID, d.Name = uid, uid
			d.SLO = SLO{Percentile: 100, TargetLatency: e5Workloads[w].target}
			p.ws = append(p.ws, capacityWorkload{d: &d, rate: rate, minRate: rate, totalNanos: 29e6})
			p.snapshot.Workloads = append(p.snapshot.Workloads, &d)
			p.snapshot.PathFeedback = append(p.snapshot.PathFeedback, PathFeedback{WorkloadUID: uid, TotalNanos: 29e6})
			p.settings.AutonomousWorkloads = append(p.settings.AutonomousWorkloads, uid)
			capacityLoad(p.settings, &p.snapshot, &d, rate)
			p.keep.Routing[uid] = map[string]int32{}
		}
	}
	weights := map[string]int32{}
	for uid := range p.index {
		currents := []e5Pair{}
		total := 0.0
		for _, options := range nodes {
			for _, c := range e5Pairs(options) {
				if current[c.id] && e5Workloads[c.w].name == uid {
					currents, total = append(currents, c), total+c.opt.mu
				}
			}
		}
		left := int32(schedulerRouteUnits)
		for k, c := range currents {
			weights[c.id] = int32(math.Floor(c.opt.mu / total * schedulerRouteUnits))
			if k == len(currents)-1 {
				weights[c.id] = left
			}
			left -= weights[c.id]
			if weights[c.id] > 0 {
				p.keep.Routing[uid][c.id] = weights[c.id]
			}
		}
	}
	for _, options := range nodes {
		pairs := slices.DeleteFunc(e5Pairs(options), func(c e5Pair) bool { _, ok := index[c.w]; return !ok })
		now := slices.DeleteFunc(slices.Clone(pairs), func(c e5Pair) bool { return !current[c.id] })
		node := base.Nodes[0]
		node.Node = NodeReference{Name: options[0].node, UID: options[0].node}
		p.snapshot.Nodes = append(p.snapshot.Nodes, node)
		instances := map[string]SchedulerInstance{}
		for _, c := range pairs {
			profile := *base.Instances[0].Profile
			profile.ID, profile.NodeName = c.id, node.Node.Name
			profile.Condition.NodeUID = node.Node.UID
			profile.IdleBaselineWatts = c.opt.idle
			service := 1e9 / c.opt.mu
			profile.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1, EnergyPerInputJoules: c.opt.e, MemoryBytes: 100}, []float64{service})}
			instance := base.Instances[0]
			instance.ID, instance.Node, instance.Profile = c.id, node.Node, &profile
			instance.WorkloadUID, instance.Weight = e5Workloads[c.w].name, weights[c.id]
			instance.Routable = map[string]bool{instance.WorkloadUID: true}
			instances[c.id] = instance
			p.snapshot.Options = append(p.snapshot.Options, poolsOption(instance))
			if current[c.id] {
				p.snapshot.Instances = append(p.snapshot.Instances, instance)
			}
		}
		itemFor := func(c e5Pair) capacityItem {
			instance := instances[c.id]
			service := float64(instance.Profile.Batches[0].ServiceSamples[0].ServiceDurationNanos)
			mu := capacityThroughput(instance.Profile, instance.MaxBatch)
			return capacityItem{id: c.id, routingWeight: profileRoutingWeight(mu), throughput: mu, serviceNanos: service, e: c.opt.e, weight: weights[c.id], prepare: !current[c.id]}
		}
		watts := func(set []e5Pair) float64 {
			x := options[0].unloaded
			for _, c := range set {
				x += c.opt.idle - c.opt.unloaded
			}
			return x
		}
		plan := func(set []e5Pair, keep bool) *capacityPlan {
			k := len(p.ws)
			plan := &capacityPlan{keep: keep, items: make([][]capacityItem, k), retained: make([][]capacityItem, k), before: watts(now)}
			ids := []string{}
			mid := slices.Clone(now)
			for _, c := range set {
				w := index[c.w]
				plan.option = strings.Replace(c.id, "-b2", "", 1)
				item := itemFor(c)
				if !current[c.id] {
					plan.preMean, plan.preMax, plan.preJ = plan.preMean+c.opt.prepareS, plan.preMax+c.opt.prepareS, plan.preJ+c.opt.prepareJ
					mid = append(mid, c)
				}
				plan.items[w] = append(plan.items[w], item)
				plan.instances = append(plan.instances, instances[c.id])
				ids = append(ids, c.id)
			}
			for _, c := range now {
				item := itemFor(c)
				item.prepare = false
				plan.retained[index[c.w]] = append(plan.retained[index[c.w]], item)
				if !slices.ContainsFunc(set, func(x e5Pair) bool { return x.id == c.id }) {
					plan.postMean, plan.postMax, plan.postJ = plan.postMean+c.opt.removeS, plan.postMax+c.opt.removeS, plan.postJ+c.opt.removeJ
				}
			}
			for w := range plan.items {
				slices.SortFunc(plan.items[w], func(a, b capacityItem) int { return cmp.Or(cmp.Compare(a.e, b.e), cmp.Compare(a.id, b.id)) })
			}
			plan.mid, plan.after = watts(mid), watts(set)
			plan.replaces = len(mid) > len(now) && plan.postMean > 0
			plan.record.Eligible, plan.record.Key = true, strings.Join(ids, "+")
			p.sum(plan)
			return plan
		}
		plans := []*capacityPlan{plan(now, true)}
		for _, c := range pairs {
			if len(now) != 1 || now[0].id != c.id {
				plans = append(plans, plan([]e5Pair{c}, false))
			}
		}
		if len(now) > 0 {
			plans = append(plans, plan(nil, false)) // remove every instance
		}
		p.plans = append(p.plans, plans)
		p.compared = append(p.compared, true)
	}
	p.periodSeconds = s.ComparisonPeriod.Seconds()
	p.snapshot.Routing = p.keep.Routing
	p.keep.Nodes, p.keep.Instances = p.snapshot.Nodes, p.snapshot.Instances
	// The baseline uses only the unchanged energy equations, without replay.
	p.keepWatts = p.evaluateEnergyAtWindow(make([]int, len(p.plans)), 0, nil, p.periodSeconds).joules / p.periodSeconds
	return p
}

var e5Mixes = func() []map[int]float64 {
	mixes := []map[int]float64{}
	for _, mnv2 := range []float64{20, 60, 110} {
		mixes = append(mixes, map[int]float64{0: mnv2, 1: 15}, map[int]float64{0: mnv2, 1: 40}, map[int]float64{0: mnv2, 2: 0.3}, map[int]float64{0: mnv2, 2: 1.2},
			map[int]float64{0: mnv2, 1: 15, 2: 0.3}, map[int]float64{0: mnv2, 1: 15, 2: 1.2}, map[int]float64{0: mnv2, 1: 40, 2: 0.3}, map[int]float64{0: mnv2, 1: 40, 2: 1.2})
	}
	return mixes
}()

// The optional sweep reports heuristic cost only. It makes no exact quality,
// capacity, latency, or global-optimality claim for the synthetic node space.
func TestCapacityPlanSearchEvaluation(t *testing.T) {
	if os.Getenv("CHILL_PLAN_SEARCH") == "" {
		t.Skip("heuristic sweep; set CHILL_PLAN_SEARCH=1")
	}
	for _, space := range []struct{ variants, modes int }{{1, 0}, {2, 0}, {1, 2}, {2, 4}} {
		for mix, rates := range e5Mixes {
			p := e5Planner(rates, evaluationScaled(space.variants, space.modes), e5Start)
			problem := p.scalarProblem(p.planTable(), time.Time{})
			candidates, stats := v2.Candidates(problem, problem.Table, 150000, 16, 8, time.Time{})
			if len(candidates) == 0 {
				t.Fatalf("space=%+v mix=%d: no heuristic finalists", space, mix)
			}
			t.Logf("HEURISTIC variants=%d modes=%d mix=%d J=%g searchFeasible=%t evaluations=%d exhausted=%t",
				space.variants, space.modes, mix, candidates[0].Joules, candidates[0].Feasible, stats.SurrogateEvaluated, stats.Exhausted)
		}
	}
}
