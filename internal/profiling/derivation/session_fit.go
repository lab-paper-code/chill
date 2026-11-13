package profilederivation

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"gonum.org/v1/gonum/mat"
)

// machineEpsilon scales the numerical rank and gradient tolerances.
const machineEpsilon = 0x1p-52

// fitPower solves level watts - idle = step*awake(hold) + joulesPerInput[b]*rate
// with step, joulesPerInput >= 0. A hold read from the decay is used as is;
// otherwise every observed gap is a hold candidate, because the awake
// fraction changes slope only at a gap, and the candidate with the smallest
// residual is chosen.
func fitPower(result *SessionResult, executions []NativeBatchRow, levels []fitLevel, idle float64, decay *float64, threshold float64) error {
	slopeColumn := make(map[int]int)
	for _, level := range levels {
		if _, ok := slopeColumn[level.at[0]]; !ok {
			slopeColumn[level.at[0]] = 1 + len(slopeColumn)
		}
	}
	for index, batch := range result.Batches {
		if _, ok := slopeColumn[index]; !ok {
			result.Batches[index].JoulesPerInput = notIdentified(fmt.Sprintf("no level of batch %d has a minimum power window", batch.Batch))
		}
	}
	if len(levels) == 0 {
		result.Power.AwakeStepWatts = notIdentified("no level has a minimum power window")
		result.Power.HoldSeconds = result.Power.AwakeStepWatts
		return nil
	}
	windows := make([]awakeWindow, len(levels))
	observed := mat.NewVecDense(len(levels), nil)
	var noise float64
	for i, level := range levels {
		windows[i] = newAwakeWindow(executions, level.start, level.end)
		observed.SetVec(i, level.watts-idle)
		noise += level.variance / float64(len(levels))
	}
	candidates := holdCandidates(windows, decay)
	type candidateFit struct {
		hold, residual float64
		solution       []float64
		design         *mat.Dense
	}
	fits := make([]candidateFit, len(candidates))
	best := 0
	for c, hold := range candidates {
		design := mat.NewDense(len(levels), 1+len(slopeColumn), nil)
		for i, level := range levels {
			design.Set(i, 0, windows[i].fraction(hold))
			design.Set(i, slopeColumn[level.at[0]], level.rate)
		}
		solution, residual, err := nonNegativeLeastSquares(design, observed)
		if err != nil {
			return fmt.Errorf("power fit at hold %gs: %w", hold, err)
		}
		fits[c] = candidateFit{hold: hold, residual: residual, solution: solution, design: design}
		if residual < fits[best].residual {
			best = c
		}
	}
	chosen := fits[best]
	for i, level := range levels {
		fraction := chosen.design.At(i, 0)
		result.Batches[level.at[0]].Levels[level.at[1]].AwakeFraction = &fraction
	}
	// A candidate is consistent when its residual exceeds the best by no more
	// than the chi-square threshold times the mean variance of a level mean.
	consistent := []candidateFit{}
	for _, fit := range fits {
		if fit.residual-chosen.residual <= threshold*noise {
			consistent = append(consistent, fit)
		}
	}
	span := func(value func(candidateFit) float64) []float64 {
		low, high := math.Inf(1), math.Inf(-1)
		for _, fit := range consistent {
			low, high = math.Min(low, value(fit)), math.Max(high, value(fit))
		}
		return []float64{low, high}
	}
	parameter := func(column int) SessionParameter {
		value := chosen.solution[column]
		p := SessionParameter{Status: ParameterIdentified, Value: &value}
		if decay == nil {
			p.Range = span(func(fit candidateFit) float64 { return fit.solution[column] })
		}
		if value == 0 {
			p.Status, p.Reason = ParameterAtBound, "the fit sits on the nonnegative bound"
		}
		return p
	}
	step := parameter(0)
	hold := SessionParameter{Status: ParameterIdentified, Value: &chosen.hold}
	if decay == nil {
		hold.Range = span(func(fit candidateFit) float64 { return fit.hold })
	}
	slopes := make(map[int]SessionParameter, len(slopeColumn))
	for index, column := range slopeColumn {
		slopes[index] = parameter(column)
	}
	rows, columns := chosen.design.Dims()
	var svd mat.SVD
	if !svd.Factorize(chosen.design, mat.SVDNone) {
		return fmt.Errorf("power fit design at hold %gs has no singular value decomposition", chosen.hold)
	}
	rank := svd.Rank(machineEpsilon * float64(max(rows, columns)))
	lowest, highest := consistent[0].hold, consistent[len(consistent)-1].hold
	switch {
	case rank < columns:
		reason := fmt.Sprintf("the power fit design has rank %d of %d", rank, columns)
		markNotIdentified(&step, &hold, slopes, reason, reason)
	case decay != nil:
	case step.Status == ParameterAtBound:
		hold = notIdentified("no awake step to time")
	case lowest == candidates[0] || highest == candidates[len(candidates)-1]:
		markNotIdentified(&step, &hold, slopes, fmt.Sprintf("the fit residual stays within the level noise for holds %gs to %gs, which reaches the end of the observed gaps %gs to %gs",
			lowest, highest, candidates[0], candidates[len(candidates)-1]), "the slope trades against the awake step, which is not identified")
	}
	result.Power.AwakeStepWatts, result.Power.HoldSeconds = step, hold
	for index, slope := range slopes {
		result.Batches[index].JoulesPerInput = slope
	}
	return nil
}

// holdCandidates is the decay hold alone, or zero, where the awake fraction is
// the busy fraction, and every distinct positive gap of the fitted levels.
func holdCandidates(windows []awakeWindow, decay *float64) []float64 {
	if decay != nil {
		return []float64{*decay}
	}
	candidates := []float64{0}
	for _, window := range windows {
		candidates = append(candidates, window.gaps...)
	}
	slices.Sort(candidates)
	candidates = slices.Compact(candidates)
	for candidates[0] < 0 {
		candidates = candidates[1:]
	}
	return candidates
}

func markNotIdentified(step, hold *SessionParameter, slopes map[int]SessionParameter, reason, slopeReason string) {
	step.Status, step.Reason, hold.Status, hold.Reason = ParameterNotIdentified, reason, ParameterNotIdentified, reason
	for index, slope := range slopes {
		slope.Status, slope.Reason = ParameterNotIdentified, slopeReason
		slopes[index] = slope
	}
}

// awakeWindow is a level window's executions, arranged so the fraction of the
// window that is inside an execution or within a hold after one costs a
// binary search.
type awakeWindow struct {
	seconds float64
	// busy is the execution time of the interior executions, those that start
	// in the window and are followed by another start in the window; gaps are
	// their following idle gaps in increasing order with prefix sums.
	busy   float64
	gaps   []float64
	prefix []float64
	edges  []awakeEdge
}

// awakeEdge is an execution crossing a window boundary, in seconds from the
// window start; next is the following start or +Inf.
type awakeEdge struct{ start, finish, next float64 }

func newAwakeWindow(executions []NativeBatchRow, start, end time.Time) awakeWindow {
	offset := func(at time.Time) float64 { return at.Sub(start).Seconds() }
	window := awakeWindow{seconds: end.Sub(start).Seconds()}
	first := sort.Search(len(executions), func(i int) bool { return !executions[i].StartedAt.Before(start) })
	last := sort.Search(len(executions), func(i int) bool { return !executions[i].StartedAt.Before(end) })
	for j := max(first-1, 0); j < last; j++ {
		edge := awakeEdge{start: offset(executions[j].StartedAt), finish: offset(executions[j].FinishedAt), next: math.Inf(1)}
		if j+1 < len(executions) {
			edge.next = offset(executions[j+1].StartedAt)
		}
		if edge.start >= 0 && edge.next <= window.seconds {
			window.busy += edge.finish - edge.start
			window.gaps = append(window.gaps, edge.next-edge.finish)
		} else {
			window.edges = append(window.edges, edge)
		}
	}
	slices.Sort(window.gaps)
	window.prefix = make([]float64, len(window.gaps)+1)
	for i, gap := range window.gaps {
		window.prefix[i+1] = window.prefix[i] + gap
	}
	return window
}

func (w awakeWindow) fraction(hold float64) float64 {
	below := sort.SearchFloat64s(w.gaps, hold)
	awake := w.busy + w.prefix[below] + hold*float64(len(w.gaps)-below)
	for _, edge := range w.edges {
		awake += math.Max(0, math.Min(math.Min(edge.finish+hold, edge.next), w.seconds)-math.Max(edge.start, 0))
	}
	return awake / w.seconds
}

// nonNegativeLeastSquares minimises |a x - b| subject to x >= 0 with the
// Lawson and Hanson active-set method (Solving Least Squares Problems, 1974,
// chapter 23); gonum has no bounded least squares. It returns x and the
// residual sum of squares.
func nonNegativeLeastSquares(a *mat.Dense, b *mat.VecDense) ([]float64, float64, error) {
	rows, columns := a.Dims()
	x := make([]float64, columns)
	passive := make([]bool, columns)
	tolerance := 10 * machineEpsilon * float64(max(rows, columns)) * mat.Norm(a, 1)
	residual := mat.NewVecDense(rows, nil)
	gradient := mat.NewVecDense(columns, nil)
	update := func() {
		residual.MulVec(a, mat.NewVecDense(columns, slices.Clone(x)))
		residual.SubVec(b, residual)
	}
	for iteration := 0; ; iteration++ {
		if iteration > 3*columns {
			return nil, 0, fmt.Errorf("non-negative least squares did not converge in %d iterations", 3*columns)
		}
		update()
		gradient.MulVec(a.T(), residual)
		next, largest := -1, tolerance
		for j := range x {
			if !passive[j] && gradient.AtVec(j) > largest {
				next, largest = j, gradient.AtVec(j)
			}
		}
		if next < 0 {
			break
		}
		passive[next] = true
		for {
			z, err := passiveLeastSquares(a, b, passive)
			if err != nil {
				return nil, 0, err
			}
			alpha, blocking := 1.0, -1
			for j := range x {
				if passive[j] && z[j] <= 0 {
					if step := x[j] / (x[j] - z[j]); blocking < 0 || step < alpha {
						alpha, blocking = step, j
					}
				}
			}
			if blocking < 0 {
				x = z
				break
			}
			for j := range x {
				x[j] += alpha * (z[j] - x[j])
			}
			x[blocking] = 0
			for j := range x {
				if passive[j] && x[j] <= 0 {
					passive[j], x[j] = false, 0
				}
			}
		}
	}
	update()
	return x, mat.Dot(residual, residual), nil
}

// passiveLeastSquares solves the unconstrained least squares problem on the
// passive columns and returns zero for the others.
func passiveLeastSquares(a *mat.Dense, b *mat.VecDense, passive []bool) ([]float64, error) {
	rows, _ := a.Dims()
	var indexes []int
	for j, in := range passive {
		if in {
			indexes = append(indexes, j)
		}
	}
	sub := mat.NewDense(rows, len(indexes), nil)
	for k, j := range indexes {
		sub.SetCol(k, mat.Col(nil, j, a))
	}
	var solution mat.VecDense
	if err := solution.SolveVec(sub, b); err != nil {
		return nil, fmt.Errorf("least squares on %d passive columns: %w", len(indexes), err)
	}
	z := make([]float64, len(passive))
	for k, j := range indexes {
		z[j] = solution.AtVec(k)
	}
	return z, nil
}
