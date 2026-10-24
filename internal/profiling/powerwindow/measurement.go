package powerwindow

import (
	"errors"
	"math"
	"sort"
	"time"
)

// Gap is a full original interval between successful reads known to contain a
// failed/missed read or to exceed the configured normal receipt-gap allowance.
// It is not expanded again for each phase.
type Gap struct {
	Start time.Time
	End   time.Time
}

// Measurement keeps nominal wall energy separate from deterministic sensitivity.
// MissingGapJoules assumes missing power lies between zero and the largest
// actual power reading in this record; it is not a certified device bound.
type Measurement struct {
	Joules            float64
	SensitivityJoules float64
	MissingGapJoules  float64
	UncertaintyJoules float64
}

// Measure integrates the actual piecewise-linear curve and finds every extremum
// of its shifted-window integral over [-shift, shift]. Unlike legacy Integrate,
// it requires real samples bracketing the entire expanded support and never
// fabricates endpoints or rejects a valid record for imprecision.
func Measure(window Window, shift time.Duration, missing []Gap) (Measurement, error) {
	if !window.End.After(window.Start) || shift < 0 || len(window.Samples) < 2 {
		return Measurement{}, errors.New("invalid actual power window or timing allowance")
	}
	start, end := window.Start.Add(-shift), window.End.Add(shift)
	if window.Samples[0].ObservedAt.After(start) || window.Samples[len(window.Samples)-1].ObservedAt.Before(end) {
		return Measurement{}, errors.New("actual power samples do not bracket expanded support")
	}
	maximumWatts, err := validatedMaximumWatts(window.Samples)
	if err != nil {
		return Measurement{}, err
	}
	first := sort.Search(len(window.Samples), func(i int) bool { return !window.Samples[i].ObservedAt.Before(start) })
	if window.Samples[first].ObservedAt.After(start) {
		first--
	}
	last := sort.Search(len(window.Samples), func(i int) bool { return !window.Samples[i].ObservedAt.Before(end) })
	x := make([]float64, last-first+1)
	y := make([]float64, len(x))
	prefix := make([]float64, len(x))
	interpolate := func(left, right Sample, at time.Time) float64 {
		fraction := at.Sub(left.ObservedAt).Seconds() / right.ObservedAt.Sub(left.ObservedAt).Seconds()
		return left.Watts*(1-fraction) + right.Watts*fraction
	}
	// Only actual expanded support contributes to S. Boundary interpolation
	// evaluates the retained curve; it neither alters raw samples nor holds an
	// unobserved endpoint. Unrelated outer power cannot erase small variations.
	var baseline float64
	for i, sample := range window.Samples[first : last+1] {
		at, watts := sample.ObservedAt, sample.Watts
		if at.Before(start) {
			at, watts = start, interpolate(sample, window.Samples[first+1], start)
		}
		if at.After(end) {
			at, watts = end, interpolate(window.Samples[last-1], sample, end)
		}
		x[i], y[i] = at.Sub(window.Start).Seconds(), watts
		if i == 0 {
			baseline = watts
		} else {
			// Centering removes any constant offset from S. Prefix trapezoids
			// reuse the nominal integrator's segment primitive.
			prefix[i] = prefix[i-1] + trapezoidJoules(y[i-1]-baseline, watts-baseline, x[i]-x[i-1])
			if !finite(prefix[i]) {
				return Measurement{}, errors.New("power integral is non-finite")
			}
		}
	}
	value := func(t float64) float64 {
		i := min(sort.Search(len(x)-1, func(i int) bool { return x[i+1] >= t }), len(x)-2)
		return (y[i] - baseline) + (y[i+1]-y[i])*(t-x[i])/(x[i+1]-x[i])
	}
	primitive := func(t float64) float64 {
		i := min(sort.Search(len(x)-1, func(i int) bool { return x[i+1] >= t }), len(x)-2)
		watts := (y[i] - baseline) + (y[i+1]-y[i])*(t-x[i])/(x[i+1]-x[i])
		return prefix[i] + trapezoidJoules(y[i]-baseline, watts, t-x[i])
	}
	duration, allowance := window.End.Sub(window.Start).Seconds(), shift.Seconds()
	integral := func(delta float64) float64 { return primitive(duration+delta) - primitive(delta) }
	nominal := integral(0)
	// Sum nominal positive-power segments directly: subtracting centered
	// prefixes and then restoring a large baseline can erase a true zero.
	result := Measurement{Joules: nominalCurveJoules(x, y, duration)}
	candidates := make([]float64, 0, 2*len(x)+2)
	candidates = append(candidates, -allowance, allowance)
	for _, knot := range x {
		for _, delta := range []float64{knot, knot - duration} {
			if delta > -allowance && delta < allowance {
				candidates = append(candidates, delta)
			}
		}
	}
	sort.Float64s(candidates)
	consider := func(delta float64) {
		result.SensitivityJoules = math.Max(result.SensitivityJoules, math.Abs(integral(delta)-nominal))
	}
	for i, delta := range candidates {
		consider(delta)
		if i == 0 || delta == candidates[i-1] {
			continue
		}
		left := candidates[i-1]
		// Between knots dE/d(delta)=q(b+delta)-q(a+delta) is linear.
		dl, dr := value(duration+left)-value(left), value(duration+delta)-value(delta)
		if (dl < 0 && dr > 0) || (dl > 0 && dr < 0) {
			scale := math.Max(math.Abs(dl), math.Abs(dr))
			leftDerivative, rightDerivative := dl/scale, dr/scale
			fraction := leftDerivative / (leftDerivative - rightDerivative)
			consider(left + (delta-left)*fraction)
		}
	}
	missingSeconds, err := missingGapSeconds(missing, start, end)
	if err != nil {
		return Measurement{}, err
	}
	result.MissingGapJoules = maximumWatts * math.Min(duration, missingSeconds)
	result.UncertaintyJoules = result.SensitivityJoules + result.MissingGapJoules
	if !finite(result.Joules) || result.Joules < 0 || !finite(result.UncertaintyJoules) {
		return Measurement{}, errors.New("measured energy or sensitivity is non-finite")
	}
	return result, nil
}

// Validate every original sample, including those outside expanded support.
// G uses the maximum of this entire actual record.
func validatedMaximumWatts(samples []Sample) (float64, error) {
	var maximumWatts float64
	for i, sample := range samples {
		if sample.ObservedAt.IsZero() || !finite(sample.Watts) || sample.Watts < 0 || (i > 0 && !sample.ObservedAt.After(samples[i-1].ObservedAt)) {
			return 0, errors.New("invalid power sample or original timestamp order")
		}
		maximumWatts = math.Max(maximumWatts, sample.Watts)
	}
	return maximumWatts, nil
}

func nominalCurveJoules(x, y []float64, duration float64) float64 {
	var joules float64
	for i := 1; i < len(x); i++ {
		left, right := math.Max(0, x[i-1]), math.Min(duration, x[i])
		if right <= left {
			continue
		}
		first, last := y[i-1], y[i]
		leftWatts, rightWatts := first, last
		if left > x[i-1] {
			leftWatts = first + (last-first)*(left-x[i-1])/(x[i]-x[i-1])
		}
		if right < x[i] {
			rightWatts = first + (last-first)*(right-x[i-1])/(x[i]-x[i-1])
		}
		joules += trapezoidJoules(leftWatts, rightWatts, right-left)
	}
	return joules
}

func missingGapSeconds(missing []Gap, start, end time.Time) (float64, error) {
	gaps := make([]Gap, 0, len(missing))
	for _, gap := range missing {
		if !gap.End.After(gap.Start) {
			return 0, errors.New("invalid original missing interval")
		}
		if gap.End.After(start) && gap.Start.Before(end) {
			gaps = append(gaps, gap)
		}
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].Start.Before(gaps[j].Start) })
	var missingSeconds float64
	if len(gaps) > 0 {
		first, last := gaps[0].Start, gaps[0].End
		for _, gap := range gaps[1:] {
			if gap.Start.After(last) {
				missingSeconds += last.Sub(first).Seconds()
				first, last = gap.Start, gap.End
			} else if gap.End.After(last) {
				last = gap.End
			}
		}
		missingSeconds += last.Sub(first).Seconds()
	}
	return missingSeconds, nil
}

// RelativeUncertainty reports a magnitude ratio for signed energy. Zero energy
// has no defined relative ratio, even when its absolute uncertainty is zero.
func RelativeUncertainty(joules, uncertainty float64) *float64 {
	if joules == 0 || !finite(joules) || !finite(uncertainty) || uncertainty < 0 {
		return nil
	}
	ratio := uncertainty / math.Abs(joules)
	if !finite(ratio) {
		return nil
	}
	return &ratio
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
