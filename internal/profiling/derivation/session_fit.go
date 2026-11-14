package profilederivation

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"gonum.org/v1/gonum/floats"
	"gonum.org/v1/gonum/mat"
	"gonum.org/v1/gonum/stat/distuv"
)

// machineEpsilon scales the numerical rank cutoff.
const machineEpsilon = 0x1p-52

// powerFit is the least squares problem of the power model: level watts less
// idle equal step*awake(hold) + joulesPerInput[b]*rate.
type powerFit struct {
	levels   []fitLevel
	observed *mat.VecDense
	// slopeColumn is the slope position of a batch index.
	slopeColumn map[int]int
	// limit is the largest residual sum of squares a fit may have and still
	// explain the levels: the best fit's plus the chi-square quantile with as
	// many degrees as fitted parameters times the mean variance of a level
	// mean, so the fits within it form a joint confidence region and each
	// parameter's range is that region's projection.
	limit float64
}

// fitPower fills the step, hold and slopes of result. A hold read from the
// decay is used as is. Otherwise the hold is searched over segments between
// breakpoints of the awake fractions (observed gaps and window edges). Within
// a segment every awake fraction is linear in the hold, so with p, q >= 0 the
// segment's model p*awake(lower) + q*awake(upper) is step*awake(hold) with
// step = p+q and hold = (lower*p + upper*q)/step, and each segment is a
// non-negative least squares problem.
//
// The resolution of the search: consecutive breakpoints are merged into one
// segment while the largest change of any level's awake fraction across the
// merged span, times the largest level watts above idle, stays at or below
// resolution, the smallest standard error of a level's mean watts. A segment
// is split only where the hold could move a level's predicted power by more
// than the measurement resolves; inside a merged segment the linear model
// departs from the exact awake fractions by at most resolution watts. A zero
// resolution searches every breakpoint.
//
// Every parameter carries its value from the best joint fit and its range over
// the fits within the limit. A parameter is NotIdentified only when the design
// does not determine it or, for the hold, when its range reaches the top of
// the searched holds.
func fitPower(result *SessionResult, levels []fitLevel, idle float64, decay *SessionParameter, tests sessionTests, resolution float64) {
	fit := powerFit{levels: levels, observed: mat.NewVecDense(max(len(levels), 1), nil), slopeColumn: map[int]int{}}
	var noise float64
	for i, level := range levels {
		if _, ok := fit.slopeColumn[level.at[0]]; !ok {
			fit.slopeColumn[level.at[0]] = len(fit.slopeColumn)
		}
		fit.observed.SetVec(i, level.watts-idle)
		noise += level.variance / float64(len(levels))
	}
	for index, batch := range result.Batches {
		if _, ok := fit.slopeColumn[index]; !ok {
			result.Batches[index].JoulesPerInput = notIdentified(fmt.Sprintf("no level of batch %d has a power window", batch.Batch))
		}
	}
	if len(levels) == 0 {
		result.Power.AwakeStepWatts = notIdentified("no level has a power window")
		result.Power.HoldSeconds = result.Power.AwakeStepWatts
		return
	}
	slopes := len(fit.slopeColumn)
	var step, hold SessionParameter
	var slope []SessionParameter
	var best acceptedFit
	awakeColumns := 1
	if decay != nil {
		value, _ := decay.Value()
		design := fit.design(fit.fractions(value))
		x, residual := nonNegativeLeastSquares(design, fit.observed)
		result.Power.FitResidualSquareWatts = residual
		fit.limit = residual + jointQuantile(tests.confidence, 1+slopes)*noise
		best = acceptedFit{design, x}
		parameters := fit.linearParameters(slices.Clone(x), 1, []acceptedFit{best})
		step, slope = parameters[0], parameters[1:]
		hold = *decay
	} else {
		awakeColumns = 2
		search := fit.searchHold(tests.confidence, noise, resolution)
		result.Power.FitResidualSquareWatts = search.best.residual
		result.Power.HoldSegmentsSearched, result.Power.HoldResolutionWatts = search.segments, resolution
		best = acceptedFit{search.best.design, search.best.x}
		fits := make([]acceptedFit, len(search.acceptable))
		for i, segment := range search.acceptable {
			fits[i] = acceptedFit{segment.design, segment.x}
		}
		values := slices.Concat([]float64{best.x[0] + best.x[1]}, best.x[2:])
		parameters := fit.linearParameters(values, 2, fits)
		step, slope = parameters[0], parameters[1:]
		hold = fit.holdParameter(search, step)
	}
	result.Power.FitLimitSquareWatts = fit.limit
	result.Power.AwakeStepWatts, result.Power.HoldSeconds = step, hold
	for index, column := range fit.slopeColumn {
		result.Batches[index].JoulesPerInput = slope[column]
	}
	// The awake fraction each level entered the best fit with: the awake
	// columns weighted by their share of the step.
	var stepSum float64
	for j := range awakeColumns {
		stepSum += best.x[j]
	}
	for i, level := range levels {
		fraction := best.design.At(i, 0)
		if stepSum > 0 {
			fraction = 0
			for j := range awakeColumns {
				fraction += best.x[j] * best.design.At(i, j) / stepSum
			}
		}
		result.Batches[level.at[0]].Levels[level.at[1]].AwakeFraction = &fraction
	}
}

// holdResolution is the smallest standard error of a fitted level's mean
// watts.
func holdResolution(levels []fitLevel) float64 {
	resolution := math.Inf(1)
	for _, level := range levels {
		resolution = math.Min(resolution, math.Sqrt(level.variance))
	}
	return resolution
}

func jointQuantile(confidence float64, parameters int) float64 {
	return distuv.ChiSquared{K: float64(parameters)}.Quantile(confidence)
}

// acceptedFit is a design and a non-negative solution within the limit.
type acceptedFit struct {
	design *mat.Dense
	x      []float64
}

// fractions returns each level's awake fraction at the hold.
func (f powerFit) fractions(hold float64) []float64 {
	awake := make([]float64, len(f.levels))
	for i, level := range f.levels {
		awake[i] = level.awake.fraction(hold)
	}
	return awake
}

// design lays out the awake columns followed by one rate column per batch.
func (f powerFit) design(awake ...[]float64) *mat.Dense {
	design := mat.NewDense(len(f.levels), len(awake)+len(f.slopeColumn), nil)
	for i, level := range f.levels {
		for k, column := range awake {
			design.Set(i, k, column[i])
		}
		design.Set(i, len(awake)+f.slopeColumn[level.at[0]], level.rate)
	}
	return design
}

// holdSegment is the hold interval between two searched breakpoints.
type holdSegment struct {
	lower, upper float64
	design       *mat.Dense
	// bound is the unconstrained least squares residual, a lower bound of the
	// segment's non-negative one.
	bound    float64
	solved   bool
	x        []float64
	residual float64
}

func (s *holdSegment) solve(observed *mat.VecDense) {
	if !s.solved {
		s.x, s.residual = nonNegativeLeastSquares(s.design, observed)
		s.solved = true
	}
}

type holdSearch struct {
	best *holdSegment
	// acceptable are the segments holding a fit within the limit.
	acceptable []*holdSegment
	// domain is the largest breakpoint; above it no awake fraction changes.
	domain   float64
	segments int
}

func (f *powerFit) searchHold(confidence, variance, resolution float64) holdSearch {
	breakpoints := []float64{0}
	for _, level := range f.levels {
		for _, point := range level.awake.breakpoints() {
			if point > 0 {
				breakpoints = append(breakpoints, point)
			}
		}
	}
	slices.Sort(breakpoints)
	breakpoints = slices.Compact(breakpoints)
	if len(breakpoints) == 1 {
		// No gap: the domain is the single hold zero.
		breakpoints = append(breakpoints, 0)
	}
	var scale float64
	for i := range f.levels {
		scale = math.Max(scale, f.observed.AtVec(i))
	}
	ends, fractions := mergeBreakpoints(breakpoints, f.fractions, scale, resolution)
	segments := make([]*holdSegment, len(ends)-1)
	all := 1<<(2+len(f.slopeColumn)) - 1
	for k := range segments {
		segment := &holdSegment{lower: breakpoints[ends[k]], upper: breakpoints[ends[k+1]], design: f.design(fractions[k], fractions[k+1])}
		segment.bound = solveFace(segment.design, f.observed, all).residual
		segments[k] = segment
	}
	ordered := slices.Clone(segments)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].bound < ordered[j].bound })
	search := holdSearch{domain: breakpoints[len(breakpoints)-1], segments: len(segments)}
	for _, segment := range ordered {
		if search.best != nil && segment.bound > search.best.residual {
			break
		}
		segment.solve(f.observed)
		if search.best == nil || segment.residual < search.best.residual {
			search.best = segment
		}
	}
	f.limit = search.best.residual + jointQuantile(confidence, 2+len(f.slopeColumn))*variance
	for _, segment := range segments {
		if segment != search.best && segment.bound > f.limit {
			continue
		}
		segment.solve(f.observed)
		if segment.residual <= f.limit {
			search.acceptable = append(search.acceptable, segment)
		}
	}
	return search
}

// mergeBreakpoints returns the indexes of the breakpoints that end searched
// segments, starting with zero, and the awake fractions there. A segment
// grows by one breakpoint while the largest change of a level's fraction from
// its start times scale stays at or below resolution; a single step is a
// segment whatever its change. Fractions grow with the hold, so the change
// over a span is the change between its ends.
func mergeBreakpoints(breakpoints []float64, fractions func(float64) []float64, scale, resolution float64) ([]int, [][]float64) {
	ends, at := []int{0}, [][]float64{fractions(breakpoints[0])}
	start, previous := at[0], at[0]
	for k := 1; k < len(breakpoints); k++ {
		current := fractions(breakpoints[k])
		if k-1 > ends[len(ends)-1] {
			var change float64
			for i := range current {
				change = math.Max(change, current[i]-start[i])
			}
			if change*scale > resolution {
				ends, at, start = append(ends, k-1), append(at, previous), previous
			}
		}
		previous = current
	}
	return append(ends, len(breakpoints)-1), append(at, previous)
}

// linearParameters gives the step and every slope their value at the best fit
// and their range over every non-negative x within the limit of each accepted
// fit's design, with the given number of awake columns; a parameter is
// NotIdentified when a design does not determine it.
func (f powerFit) linearParameters(values []float64, awake int, fits []acceptedFit) []SessionParameter {
	// The step sums the awake columns, a slope is its rate column.
	functionals := make([][]float64, len(values))
	for k := range functionals {
		functionals[k] = make([]float64, awake+len(values)-1)
		if k == 0 {
			for j := range awake {
				functionals[k][j] = 1
			}
		} else {
			functionals[k][awake+k-1] = 1
		}
	}
	bounds := make([][2]float64, len(values))
	determined := make([]bool, len(values))
	for k := range bounds {
		bounds[k], determined[k] = [2]float64{math.Inf(1), math.Inf(-1)}, true
	}
	for _, fit := range fits {
		for k, c := range functionals {
			determined[k] = determined[k] && estimable(fit.design, c)
		}
		for k, extent := range extents(fit.design, f.observed, functionals, f.limit, fit.x) {
			bounds[k] = [2]float64{math.Min(bounds[k][0], extent[0]), math.Max(bounds[k][1], extent[1])}
		}
	}
	parameters := make([]SessionParameter, len(values))
	for k := range parameters {
		name := "the awake step"
		if k > 0 {
			name = "the slope"
		}
		parameters[k] = identifiedParameter(values[k], bounds[k])
		if !determined[k] {
			parameters[k] = notIdentified(fmt.Sprintf("the power fit design does not determine %s", name))
		}
	}
	return parameters
}

// holdParameter ranges the hold over the acceptable segments. Within one
// segment the holds that fit form an interval (the projection of a convex set
// through q/(p+q)); its ends are the segment ends that fit or the holds where
// the residual of a feasible set of free columns reaches the limit. A step
// that may be zero lets every hold fit, so the range reaches the top.
func (f powerFit) holdParameter(search holdSearch, step SessionParameter) SessionParameter {
	stepRange, _ := step.Range()
	switch {
	case step.Status() == ParameterNotIdentified:
		return notIdentified("the awake step is not determined, so neither is the hold")
	case stepRange[0] <= 0:
		return notIdentifiedWithin(fmt.Sprintf("the awake step may be zero within the noise limit, so every hold from 0 s to the top of the searched holds, %.6g s, fits", search.domain), [2]float64{0, search.domain})
	}
	best := search.best
	value := (best.lower*best.x[0] + best.upper*best.x[1]) / (best.x[0] + best.x[1])
	switch {
	case best.x[0] == 0:
		value = best.upper
	case best.x[1] == 0:
		value = best.lower
	}
	bounds := [2]float64{value, value}
	include := func(hold float64) {
		bounds = [2]float64{math.Min(bounds[0], hold), math.Max(bounds[1], hold)}
	}
	for _, segment := range search.acceptable {
		for _, end := range []float64{segment.lower, segment.upper} {
			if _, residual := nonNegativeLeastSquares(f.design(f.fractions(end)), f.observed); residual <= f.limit {
				include(end)
			}
		}
		for _, hold := range f.limitHolds(segment) {
			include(hold)
		}
	}
	if bounds[1] >= search.domain {
		return notIdentifiedWithin(fmt.Sprintf("the holds within the noise limit, %.6g s to %.6g s, reach the top of the searched holds, %.6g s", bounds[0], bounds[1], search.domain), bounds)
	}
	return identifiedParameter(value, bounds)
}

// limitHolds returns the holds of a segment where the residual of a feasible
// full-rank set of free columns, the awake column among them, equals the
// limit. With the free slope columns E projected out by P, the residual at
// w in [0, 1] is |Pb|^2 - (Pa(w)·Pb)^2/|Pa(w)|^2 with a(w) linear in w, so
// the condition is a quadratic in w.
func (f powerFit) limitHolds(segment *holdSegment) []float64 {
	rows, columns := segment.design.Dims()
	lower, upper := mat.Col(nil, 0, segment.design), mat.Col(nil, 1, segment.design)
	difference := slices.Clone(upper)
	floats.Sub(difference, lower)
	var holds []float64
	for mask := range 1 << (columns - 2) {
		var rates []int
		for j := range columns - 2 {
			if mask&(1<<j) != 0 {
				rates = append(rates, 2+j)
			}
		}
		project := func(v []float64) []float64 { return v }
		if len(rates) > 0 {
			sub := mat.NewDense(rows, len(rates), nil)
			for k, j := range rates {
				sub.SetCol(k, mat.Col(nil, j, segment.design))
			}
			var svd mat.SVD
			if !svd.Factorize(sub, mat.SVDThin) || svd.Rank(rankCutoff(rows, len(rates))) < len(rates) {
				continue
			}
			project = func(v []float64) []float64 {
				var solution, fitted mat.VecDense
				svd.SolveVecTo(&solution, mat.NewVecDense(rows, v), len(rates))
				fitted.MulVec(sub, &solution)
				out := slices.Clone(v)
				floats.Sub(out, fitted.RawVector().Data)
				return out
			}
		}
		u, v, b := project(lower), project(difference), project(f.observed.RawVector().Data)
		alpha, beta := floats.Dot(u, b), floats.Dot(v, b)
		gamma, delta, epsilon := floats.Dot(u, u), floats.Dot(u, v), floats.Dot(v, v)
		k := floats.Dot(b, b) - f.limit
		a2, b1, c0 := beta*beta-k*epsilon, alpha*beta-k*delta, alpha*alpha-k*gamma
		var roots []float64
		switch discriminant := b1*b1 - a2*c0; {
		case a2 == 0 && b1 != 0:
			roots = []float64{-c0 / (2 * b1)}
		case a2 != 0 && discriminant >= 0:
			roots = []float64{(-b1 - math.Sqrt(discriminant)) / a2, (-b1 + math.Sqrt(discriminant)) / a2}
		}
		for _, w := range roots {
			if w < 0 || w > 1 {
				continue
			}
			hold := segment.lower + w*(segment.upper-segment.lower)
			face := solveFace(f.design(f.fractions(hold)), f.observed, 1|mask<<1)
			if face.fullRank && !slices.ContainsFunc(face.x, func(x float64) bool { return x < 0 }) {
				holds = append(holds, hold)
			}
		}
	}
	return holds
}

// rankCutoff is the relative singular value cutoff of LAPACK's xGELSS and
// numpy.linalg.lstsq: machine epsilon times the larger dimension.
func rankCutoff(rows, columns int) float64 {
	return machineEpsilon * float64(max(rows, columns))
}

// faceFit is the minimum-norm least squares solution on one set of free
// columns with the others held at zero.
type faceFit struct {
	free     []int
	x        []float64
	residual float64
	// usable is false when the decomposition failed.
	usable, fullRank bool
	svd              mat.SVD
}

func solveFace(a *mat.Dense, b *mat.VecDense, mask int) faceFit {
	rows, columns := a.Dims()
	fit := faceFit{x: make([]float64, columns), usable: true, fullRank: true}
	for j := range columns {
		if mask&(1<<j) != 0 {
			fit.free = append(fit.free, j)
		}
	}
	if len(fit.free) > 0 {
		sub := mat.NewDense(rows, len(fit.free), nil)
		for k, j := range fit.free {
			sub.SetCol(k, mat.Col(nil, j, a))
		}
		if !fit.svd.Factorize(sub, mat.SVDThin) {
			fit.usable, fit.fullRank = false, false
			return fit
		}
		rank := fit.svd.Rank(rankCutoff(rows, len(fit.free)))
		fit.fullRank = rank == len(fit.free)
		if rank > 0 {
			var z mat.VecDense
			fit.svd.SolveVecTo(&z, b, rank)
			for k, j := range fit.free {
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

// extents returns the smallest and largest c·x over x >= 0 with
// |Ax - b|^2 <= limit for each c >= 0 that is estimable. By the KKT
// conditions an extreme lies on a face with independent free columns F,
// either where c vanishes on F or where the ellipsoid bounds it:
// x = x_F ± r*G⁻¹c_F with G = A_Fᵀ A_F and r = sqrt((limit -
// residual_F)/(c_Fᵀ G⁻¹ c_F)). Every face is factorized once and tried for
// every c, and the feasible candidates kept, along with the known solution
// within the limit, so rounding cannot leave a range empty.
func extents(a *mat.Dense, b *mat.VecDense, cs [][]float64, limit float64, known []float64) [][2]float64 {
	bounds := make([][2]float64, len(cs))
	for k, c := range cs {
		value := floats.Dot(c, known)
		bounds[k] = [2]float64{value, value}
	}
	include := func(k int, x []float64) {
		if !slices.ContainsFunc(x, func(v float64) bool { return v < 0 }) {
			value := floats.Dot(cs[k], x)
			bounds[k] = [2]float64{math.Min(bounds[k][0], value), math.Max(bounds[k][1], value)}
		}
	}
	_, columns := a.Dims()
	for mask := range 1 << columns {
		fit := solveFace(a, b, mask)
		if !fit.fullRank || fit.residual > limit {
			continue
		}
		var v mat.Dense
		var sigmas []float64
		if len(fit.free) > 0 {
			fit.svd.VTo(&v)
			sigmas = fit.svd.Values(nil)
		}
		for k, c := range cs {
			g := make([]float64, columns)
			for i, sigma := range sigmas {
				var projection float64
				for row, j := range fit.free {
					projection += v.At(row, i) * c[j]
				}
				for row, j := range fit.free {
					g[j] += v.At(row, i) * projection / (sigma * sigma)
				}
			}
			curvature := floats.Dot(c, g)
			if curvature == 0 {
				include(k, fit.x)
				continue
			}
			radius := math.Sqrt((limit - fit.residual) / curvature)
			for _, sign := range []float64{-1, 1} {
				x := slices.Clone(fit.x)
				floats.AddScaled(x, sign*radius, g)
				include(k, x)
			}
		}
	}
	return bounds
}

// estimable reports whether c·x is the same for every x with the same Ax,
// that is, c lies in the row space of A.
func estimable(a *mat.Dense, c []float64) bool {
	rows, columns := a.Dims()
	stacked := mat.NewDense(rows+1, columns, nil)
	stacked.Slice(0, rows, 0, columns).(*mat.Dense).Copy(a)
	stacked.SetRow(rows, c)
	var plain, extended mat.SVD
	if !plain.Factorize(a, mat.SVDNone) || !extended.Factorize(stacked, mat.SVDNone) {
		return false
	}
	return plain.Rank(rankCutoff(rows, columns)) == extended.Rank(rankCutoff(rows+1, columns))
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

// awakeEdge is an execution crossing a window boundary. Its awake time inside
// the window at hold h is max(0, min(h, until) - from): from is the hold at
// which its finish plus the hold enters the window, until the hold at which
// it reaches the following start or the window end. Both are native time
// differences, so one instant gives one breakpoint whichever row it came from.
type awakeEdge struct{ from, until float64 }

// levelAwakeWindow is the awake window of one level on the native clock
// alone: from the first native start plus the meter lag, which drops the
// executions the meter has not yet seen, to the last native finish. It
// returns the window bounds and whether the window is positive.
func levelAwakeWindow(rows []NativeBatchRow, lag time.Duration) (awakeWindow, [2]time.Time, bool) {
	executions := slices.Clone(rows)
	sort.Slice(executions, func(i, j int) bool { return executions[i].StartedAt.Before(executions[j].StartedAt) })
	bounds := [2]time.Time{executions[0].StartedAt.Add(lag), executions[0].FinishedAt}
	for _, row := range executions {
		if row.FinishedAt.After(bounds[1]) {
			bounds[1] = row.FinishedAt
		}
	}
	if !bounds[1].After(bounds[0]) {
		return awakeWindow{}, bounds, false
	}
	return newAwakeWindow(executions, bounds[0], bounds[1]), bounds, true
}

func newAwakeWindow(executions []NativeBatchRow, start, end time.Time) awakeWindow {
	window := awakeWindow{seconds: end.Sub(start).Seconds()}
	first := sort.Search(len(executions), func(i int) bool { return !executions[i].StartedAt.Before(start) })
	last := sort.Search(len(executions), func(i int) bool { return !executions[i].StartedAt.Before(end) })
	for j := max(first-1, 0); j < last; j++ {
		execution := executions[j]
		limit := end
		if j+1 < len(executions) && executions[j+1].StartedAt.Before(end) {
			limit = executions[j+1].StartedAt
		}
		if !execution.StartedAt.Before(start) && j+1 < len(executions) && !executions[j+1].StartedAt.After(end) {
			window.busy += execution.FinishedAt.Sub(execution.StartedAt).Seconds()
			window.gaps = append(window.gaps, executions[j+1].StartedAt.Sub(execution.FinishedAt).Seconds())
			continue
		}
		entered := execution.StartedAt
		if entered.Before(start) {
			entered = start
		}
		window.edges = append(window.edges, awakeEdge{from: entered.Sub(execution.FinishedAt).Seconds(), until: limit.Sub(execution.FinishedAt).Seconds()})
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
		awake += math.Max(0, math.Min(hold, edge.until)-edge.from)
	}
	return awake / w.seconds
}

// breakpoints are the holds where the fraction changes slope.
func (w awakeWindow) breakpoints() []float64 {
	points := slices.Clone(w.gaps)
	for _, edge := range w.edges {
		points = append(points, edge.from, edge.until)
	}
	return points
}
