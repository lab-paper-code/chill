package profilederivation

import (
	"fmt"
	"math"
	"slices"

	"gonum.org/v1/gonum/floats"
	"gonum.org/v1/gonum/mat"
)

// machineEpsilon scales the numerical rank cutoff.
const machineEpsilon = 0x1p-52

// The hold search: a geometric grid of holdGridPoints holds from
// holdGridFloorSeconds to the hold where every level's awake fraction is 1 in
// float64, rate*hold = saturatedExponent (exp(-50) is below half an ulp of 1),
// plus hold zero; the grid minimum is refined between its neighbours.
const (
	holdGridPoints       = 400
	holdGridFloorSeconds = 1e-4
	saturatedExponent    = 50
)

// AwakeFraction is the share of time a node is executing or within hold
// seconds after an execution, at input rate and utilisation (rate times the
// mean batch service per input, capped at 1): an instance grows batches only
// from a queue, so an idle period ends with the next input. The load power fit
// and the scheduler use this one formula.
func AwakeFraction(rate, utilisation, hold float64) float64 {
	return 1 - (1-min(utilisation, 1))*math.Exp(-rate*hold)
}

// LoadPower is a session's node power computed from its stored measurements:
// watts = idle + step*AwakeFraction(rate, utilisation, hold) + slope_b*rate.
// Step, hold and slopes are fit coefficients of that formula, not physical
// times or energies.
type LoadPower struct {
	IdleWatts      SessionParameter
	AwakeStepWatts SessionParameter
	HoldSeconds    SessionParameter
	// Batches are parallel to the result's batches.
	Batches []BatchLoadPower
}

type BatchLoadPower struct {
	// InputSlopeJoules is the batch's fitted slope.
	InputSlopeJoules SessionParameter
	// SaturationJoulesPerInput is the saturation level's watts above idle per
	// input per second, the energy per input the executor ranks batch sizes by.
	SaturationJoulesPerInput SessionParameter
}

// LoadPower computes the power model from the stored idle watts, each level's
// input rate and mean watts, and each batch's service records. Step and slopes
// are the non-negative least squares fit over every level with watts, the
// hold the one-dimensional minimum of its residual over the holds where the
// design has full rank. The fit is identified when there are more levels than
// step and slopes and some searched hold gives a full-rank design.
func (r SessionResult) LoadPower() LoadPower {
	power := LoadPower{IdleWatts: r.Power.IdleWatts, Batches: make([]BatchLoadPower, len(r.Batches))}
	idle, ok := r.Power.IdleWatts.Value()
	if !ok {
		const reason = "settled idle watts not identified"
		power.withoutFit(reason)
		for i := range power.Batches {
			power.Batches[i].SaturationJoulesPerInput = notIdentified(reason)
		}
		return power
	}
	for i, batch := range r.Batches {
		power.Batches[i].SaturationJoulesPerInput = saturationJoules(batch, idle)
	}
	fit, err := fitLoad(r.Batches, idle)
	if err != nil {
		power.withoutFit(err.Error())
		return power
	}
	power.AwakeStepWatts, power.HoldSeconds = valueParameter(fit.x[0]), valueParameter(fit.hold)
	for i, batch := range r.Batches {
		if column, fitted := fit.column[i]; fitted {
			power.Batches[i].InputSlopeJoules = valueParameter(fit.x[column])
		} else {
			power.Batches[i].InputSlopeJoules = notIdentified(fmt.Sprintf("no level of batch %d has measured watts", batch.Batch))
		}
	}
	return power
}

// withoutFit marks the step, the hold and every slope not identified.
func (p *LoadPower) withoutFit(reason string) {
	unknown := notIdentified(reason)
	p.AwakeStepWatts, p.HoldSeconds = unknown, unknown
	for i := range p.Batches {
		p.Batches[i].InputSlopeJoules = unknown
	}
}

func saturationJoules(batch SessionBatch, idle float64) SessionParameter {
	for _, level := range batch.Levels {
		if level.Kind == LevelSaturation && level.MeanWatts != nil {
			return valueParameter((*level.MeanWatts - idle) / level.InputRate)
		}
	}
	return notIdentified(fmt.Sprintf("batch %d has no saturation level with measured watts", batch.Batch))
}

// loadFit is the least squares problem watts - idle = step*awake(hold) +
// slope_b*rate over the levels with watts.
type loadFit struct {
	rate, utilisation []float64
	// row is each level's slope column, column each batch index's.
	row      []int
	column   map[int]int
	observed *mat.VecDense
	hold     float64
	x        []float64
}

// fitLoad lays out the levels with watts and solves for the hold, the step
// and the slopes.
func fitLoad(batches []SessionBatch, idle float64) (*loadFit, error) {
	fit := &loadFit{column: map[int]int{}}
	var observed []float64
	for index, batch := range batches {
		var service float64
		for _, nanos := range batch.ServiceNanos {
			service += float64(nanos) / float64(len(batch.ServiceNanos))
		}
		for _, level := range batch.Levels {
			if level.MeanWatts == nil {
				continue
			}
			if len(batch.ServiceNanos) == 0 {
				return nil, fmt.Errorf("batch %d has levels with watts but no service record for its utilisation", batch.Batch)
			}
			if _, ok := fit.column[index]; !ok {
				fit.column[index] = 1 + len(fit.column)
			}
			fit.rate = append(fit.rate, level.InputRate)
			fit.utilisation = append(fit.utilisation, level.InputRate*service/1e9/float64(batch.Batch))
			fit.row = append(fit.row, fit.column[index])
			observed = append(observed, *level.MeanWatts-idle)
		}
	}
	if unknowns := 1 + len(fit.column); len(observed) < unknowns+1 {
		return nil, fmt.Errorf("%d levels with watts do not determine the awake step, %d slopes and the hold", len(observed), len(fit.column))
	}
	fit.observed = mat.NewVecDense(len(observed), observed)
	return fit, fit.solve()
}

// design lays out the awake column followed by one rate column per batch.
func (f *loadFit) design(hold float64) *mat.Dense {
	design := mat.NewDense(len(f.rate), 1+len(f.column), nil)
	for i, rate := range f.rate {
		design.Set(i, 0, AwakeFraction(rate, f.utilisation[i], hold))
		design.Set(i, f.row[i], rate)
	}
	return design
}

// residual is the non-negative least squares residual at hold, +Inf where the
// design does not have full rank: at hold zero every awake fraction below 1
// is its utilisation, a multiple of its rate, so step and slopes trade.
func (f *loadFit) residual(hold float64) float64 {
	design := f.design(hold)
	rows, columns := design.Dims()
	var svd mat.SVD
	if !svd.Factorize(design, mat.SVDNone) || svd.Rank(rankCutoff(rows, columns)) < columns {
		return math.Inf(1)
	}
	_, residual := nonNegativeLeastSquares(design, f.observed)
	return residual
}

// solve picks the grid hold with the smallest residual, the first on ties, and
// refines it between its grid neighbours by golden-section search (gonum has
// no bounded scalar minimiser); then fits step and slopes at that hold.
func (f *loadFit) solve() error {
	top := saturatedExponent / slices.Min(f.rate)
	holds := append([]float64{0}, floats.LogSpan(make([]float64, holdGridPoints), min(holdGridFloorSeconds, top), top)...)
	holds[len(holds)-1] = top
	residuals := make([]float64, len(holds))
	for k, hold := range holds {
		residuals[k] = f.residual(hold)
	}
	k := floats.MinIdx(residuals)
	if math.IsInf(residuals[k], 1) {
		return fmt.Errorf("the power fit design does not have full rank (awake step and %d slopes) at any hold from 0 s to %.6g s", len(f.column), top)
	}
	f.hold = holds[k]
	if k > 0 && k < len(holds)-1 {
		if hold, residual := f.goldenSection(holds[k-1], holds[k+1]); residual <= residuals[k] {
			f.hold = hold
		}
	}
	f.x, _ = nonNegativeLeastSquares(f.design(f.hold), f.observed)
	return nil
}

// goldenSection narrows [lower, upper] until it no longer shrinks in float64.
func (f *loadFit) goldenSection(lower, upper float64) (float64, float64) {
	ratio := (math.Sqrt(5) - 1) / 2
	left, right := upper-ratio*(upper-lower), lower+ratio*(upper-lower)
	leftResidual, rightResidual := f.residual(left), f.residual(right)
	for left < right && lower < left && right < upper {
		if leftResidual <= rightResidual {
			upper, right, rightResidual = right, left, leftResidual
			left = upper - ratio*(upper-lower)
			leftResidual = f.residual(left)
		} else {
			lower, left, leftResidual = left, right, rightResidual
			right = lower + ratio*(upper-lower)
			rightResidual = f.residual(right)
		}
	}
	if leftResidual <= rightResidual {
		return left, leftResidual
	}
	return right, rightResidual
}

// rankCutoff is the relative singular value cutoff of LAPACK's xGELSS and
// numpy.linalg.lstsq: machine epsilon times the larger dimension.
func rankCutoff(rows, columns int) float64 {
	return machineEpsilon * float64(max(rows, columns))
}

// faceFit is the minimum-norm least squares solution on one set of free
// columns with the others held at zero.
type faceFit struct {
	x        []float64
	residual float64
	// usable is false when the decomposition failed.
	usable bool
}

func solveFace(a *mat.Dense, b *mat.VecDense, mask int) faceFit {
	rows, columns := a.Dims()
	fit := faceFit{x: make([]float64, columns), usable: true}
	var free []int
	for j := range columns {
		if mask&(1<<j) != 0 {
			free = append(free, j)
		}
	}
	if len(free) > 0 {
		sub := mat.NewDense(rows, len(free), nil)
		for k, j := range free {
			sub.SetCol(k, mat.Col(nil, j, a))
		}
		var svd mat.SVD
		if !svd.Factorize(sub, mat.SVDThin) {
			fit.usable = false
			return fit
		}
		if rank := svd.Rank(rankCutoff(rows, len(free))); rank > 0 {
			var z mat.VecDense
			svd.SolveVecTo(&z, b, rank)
			for k, j := range free {
				fit.x[j] = z.AtVec(k)
			}
		}
	}
	var residual mat.VecDense
	residual.MulVec(a, mat.NewVecDense(columns, fit.x))
	residual.SubVec(b, &residual)
	fit.residual = mat.Dot(&residual, &residual)
	return fit
}

// nonNegativeLeastSquares minimises |Ax - b| over x >= 0 by enumerating
// every set of free columns: the optimum is the least squares solution on the
// columns where it is positive (Lawson and Hanson, Solving Least Squares
// Problems, 1974, chapter 23), so the feasible face solution with the smallest
// residual is the optimum.
func nonNegativeLeastSquares(a *mat.Dense, b *mat.VecDense) ([]float64, float64) {
	_, columns := a.Dims()
	best := faceFit{residual: math.Inf(1)}
	for mask := range 1 << columns {
		fit := solveFace(a, b, mask)
		if fit.usable && fit.residual < best.residual && !slices.ContainsFunc(fit.x, func(x float64) bool { return x < 0 }) {
			best = fit
		}
	}
	return best.x, best.residual
}
