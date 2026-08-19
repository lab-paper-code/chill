// Package powerwindow integrates bounded wall-power observations.
package powerwindow

import (
	"errors"
	"math"
	"sort"
	"time"
)

// Sample is one wall-power observation at a timestamp.
type Sample struct {
	ObservedAt time.Time
	Watts      float64
}

// Window bounds the samples included in one integration.
type Window struct {
	Start   time.Time
	End     time.Time
	Samples []Sample
}

// Policy bounds the accepted sample coverage within a window.
type Policy struct {
	MaximumBoundaryDistance time.Duration
	MinimumCoverage         float64
	MaximumGap              time.Duration
}

// Integrate returns the time-weighted wall energy within window in joules.
func Integrate(window Window, policy Policy) (float64, error) {
	samples := append([]Sample(nil), window.Samples...)
	sort.Slice(samples, func(i, j int) bool {
		return samples[i].ObservedAt.Before(samples[j].ObservedAt)
	})
	inside := make([]Sample, 0, len(samples))
	for _, sample := range samples {
		if math.IsNaN(sample.Watts) || math.IsInf(sample.Watts, 0) || sample.Watts < 0 {
			return 0, errors.New("invalid power sample")
		}
		if !sample.ObservedAt.Before(window.Start) && !sample.ObservedAt.After(window.End) {
			inside = append(inside, sample)
		}
	}
	if len(inside) < 2 {
		return 0, errors.New("fewer than two samples overlap the runtime window")
	}
	if inside[0].ObservedAt.Sub(window.Start) > policy.MaximumBoundaryDistance ||
		window.End.Sub(inside[len(inside)-1].ObservedAt) > policy.MaximumBoundaryDistance {
		return 0, errors.New("sample boundary distance exceeds policy")
	}
	coverage := inside[len(inside)-1].ObservedAt.Sub(inside[0].ObservedAt).
		Seconds() /
		window.End.Sub(window.Start).
			Seconds()
	if coverage < policy.MinimumCoverage {
		return 0, errors.New("sample coverage is below policy")
	}
	points := make([]Sample, 0, len(inside)+2)
	points = append(points, Sample{ObservedAt: window.Start, Watts: inside[0].Watts})
	points = append(points, inside...)
	points = append(points, Sample{ObservedAt: window.End, Watts: inside[len(inside)-1].Watts})
	var joules float64
	for index := 1; index < len(points); index++ {
		duration := points[index].ObservedAt.Sub(points[index-1].ObservedAt)
		if duration <= 0 {
			return 0, errors.New("duplicate or non-monotonic sample timestamp")
		}
		if duration > policy.MaximumGap {
			return 0, errors.New("maximum sample gap exceeds policy")
		}
		joules += (points[index-1].Watts + points[index].Watts) / 2 * duration.Seconds()
	}
	return joules, nil
}
