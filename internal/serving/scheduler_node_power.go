package serving

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
)

// schedulerLoadModel is one instance's measured power terms under its batch
// limit, resolved from its profile once per Tick.
type schedulerLoadModel struct {
	// identity names the profile and batch limit in missing-energy cells.
	identity, profile string
	batches           []schedulerLoadBatch
	step, hold        float64
	stepOK, holdOK    bool
}

type schedulerLoadBatch struct {
	saturation   float64 // inputs per second at the saturation level
	saturationOK bool
	slope        float64 // joules per input
	slopeOK      bool
	busy         float64 // mean batch service seconds per input
	busyOK       bool
}

func newSchedulerLoadModel(p *Profile, maxBatch int) *schedulerLoadModel {
	if p == nil {
		return nil
	}
	m := &schedulerLoadModel{identity: fmt.Sprintf("%s/b%d", p.ID, maxBatch), profile: p.ID}
	if p.Power != nil {
		m.step, m.stepOK = p.Power.AwakeStepWatts.Value()
		m.hold, m.holdOK = p.Power.HoldSeconds.Value()
	}
	batches := slices.Clone(p.Batches)
	slices.SortFunc(batches, func(a, b BatchProfile) int { return cmp.Compare(a.Batch, b.Batch) })
	for _, b := range batches {
		if b.Batch < 1 || maxBatch > 0 && b.Batch > maxBatch {
			continue
		}
		batch := schedulerLoadBatch{}
		if b.SaturationInputRate != nil {
			batch.saturation, batch.saturationOK = *b.SaturationInputRate, true
		}
		if b.InputSlopeJoules != nil {
			batch.slope, batch.slopeOK = b.InputSlopeJoules.Value()
		}
		if mean, ok := p.MeanNanos(b.Batch); ok {
			batch.busy, batch.busyOK = mean/1e9/float64(b.Batch), true
		}
		m.batches = append(m.batches, batch)
	}
	return m
}

// batchAt is the smallest measured batch whose saturation input rate covers
// rate, or the largest measured batch when none does.
func (m *schedulerLoadModel) batchAt(rate float64) *schedulerLoadBatch {
	if len(m.batches) == 0 {
		return nil
	}
	for k := range m.batches {
		if m.batches[k].saturationOK && m.batches[k].saturation >= rate {
			return &m.batches[k]
		}
	}
	return &m.batches[len(m.batches)-1]
}

// schedulerLoadWatts is the scheduler's node power model; every energy
// consumer (exact evaluation, the search's per-move evaluation, the bridge,
// replay, the decision presentation) calls it. A node's watts are
//
//	settled idle + S*a + sum_i e_i*lambda_i
//
// The settled idle is the node's loaded-idle watts (schedulerBaseline); this
// function returns the rest, over the instances i on the node with input rate
// lambda_i > 0:
//   - lambda = sum_i lambda_i, and rho = min(1, sum_i lambda_i*s_i/b_i), the
//     node's utilisation, where s_i is the mean service time of batch b_i;
//   - a = 1 - (1-rho)*exp(-lambda*tau) is the awake fraction, the share of
//     time the node is executing or within the hold tau after an execution:
//     an instance grows batches only from a queue, so an idle period ends
//     with the next input;
//   - S and tau are taken together from the loaded configuration with the
//     largest S, since the fit trades one against the other;
//   - b_i is the smallest measured batch size, within the instance's batch
//     limit, whose saturation input rate covers lambda_i (the largest
//     measured one when none does), and e_i is that batch's input slope.
//
// ok is false when a value the sum needs is not identified: S of a loaded
// configuration, tau when the chosen S is above zero, e_i or s_i. Callers then
// treat the node's energy as not comparable, never as a number. Without load
// the term is zero and needs no value.
func schedulerLoadWatts(models []*schedulerLoadModel, rates []float64) (float64, bool) {
	lambda, rho, watts := 0.0, 0.0, 0.0
	var awake *schedulerLoadModel
	for k, m := range models {
		rate := rates[k]
		if rate <= 0 {
			continue
		}
		if m == nil || !m.stepOK {
			return 0, false
		}
		b := m.batchAt(rate)
		if b == nil || !b.slopeOK || !b.busyOK {
			return 0, false
		}
		lambda, rho, watts = lambda+rate, rho+rate*b.busy, watts+b.slope*rate
		if awake == nil || m.step > awake.step {
			awake = m
		}
	}
	if awake == nil || awake.step == 0 {
		return watts, true
	}
	if !awake.holdOK {
		return 0, false
	}
	return watts + awake.step*(1-(1-min(rho, 1))*math.Exp(-lambda*awake.hold)), true
}

// schedulerRate is one instance's input rate in one routing state.
type schedulerRate struct {
	id    string
	node  NodeReference
	model *schedulerLoadModel
	rate  float64
}

// schedulerNodeLoad is one node's load watts in one routing state. key names
// the load (configurations and rates) a not-identified value is unknown at.
type schedulerNodeLoad struct {
	node       NodeReference
	watts      float64
	known      bool
	key        string
	identities []string
	references []string
}

// nodeLoadWatts merges rates per instance and applies schedulerLoadWatts to
// each loaded node, in node UID order.
func nodeLoadWatts(rates []schedulerRate) []schedulerNodeLoad {
	rates = slices.Clone(rates)
	slices.SortFunc(rates, func(a, b schedulerRate) int {
		return cmp.Or(cmp.Compare(a.node.UID, b.node.UID), cmp.Compare(a.id, b.id))
	})
	merged := rates[:0]
	for _, r := range rates {
		if n := len(merged); n > 0 && merged[n-1].id == r.id && merged[n-1].node == r.node {
			merged[n-1].rate += r.rate
			continue
		}
		merged = append(merged, r)
	}
	var out []schedulerNodeLoad
	for start := 0; start < len(merged); {
		end := start
		for end < len(merged) && merged[end].node == merged[start].node {
			end++
		}
		models, values := make([]*schedulerLoadModel, 0, end-start), make([]float64, 0, end-start)
		load := schedulerNodeLoad{node: merged[start].node}
		for _, r := range merged[start:end] {
			if r.rate <= 0 {
				continue
			}
			models, values = append(models, r.model), append(values, r.rate)
			identity := r.id
			if r.model != nil {
				identity = r.model.identity
				load.references = appendUnique(load.references, r.model.profile)
			}
			load.identities = append(load.identities, fmt.Sprintf("%s@%g", identity, r.rate))
		}
		load.watts, load.known = schedulerLoadWatts(models, values)
		slices.Sort(load.identities)
		load.key = merged[start].node.UID + ":" + strings.Join(load.identities, ",")
		if len(models) > 0 {
			out = append(out, load)
		}
		start = end
	}
	return out
}

// schedulerLoadUnknown is a node's load watts the exact evaluation could not
// compute, held for its seconds as a missing-energy cell.
type schedulerLoadUnknown struct {
	node       NodeReference
	phase      string
	key        string
	identities []string
	seconds    float64
}

// schedulerLoadModels resolves one model per profile and batch limit; it is
// safe for the concurrent exact evaluations of one Tick.
type schedulerLoadModels struct{ cache sync.Map }

type schedulerLoadModelKey struct {
	profile  *Profile
	maxBatch int
}

func (c *schedulerLoadModels) of(i SchedulerInstance) *schedulerLoadModel {
	if i.Profile == nil {
		return nil
	}
	key := schedulerLoadModelKey{i.Profile, i.MaxBatch}
	if model, ok := c.cache.Load(key); ok {
		return model.(*schedulerLoadModel)
	}
	model, _ := c.cache.LoadOrStore(key, newSchedulerLoadModel(i.Profile, i.MaxBatch))
	return model.(*schedulerLoadModel)
}

// capacityEnergyOrder orders instances for routing ties by their batch-1 input
// slope; an instance without an identified slope orders last. It is an order,
// not an energy.
func capacityEnergyOrder(i SchedulerInstance) float64 {
	if b := schedulerBatch(i.Profile, 1); b != nil && b.InputSlopeJoules != nil {
		if slope, ok := b.InputSlopeJoules.Value(); ok {
			return slope
		}
	}
	return math.Inf(1)
}
