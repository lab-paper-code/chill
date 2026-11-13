package live

import (
	"cmp"
	"slices"
	"time"

	"gonum.org/v1/gonum/stat"
)

type sloSample struct {
	finishedAt int64 // Unix nanoseconds
	ms         float64
}

// sloSeries holds one Workload's completed latencies under its current SLO.
// Metrics.mu guards coveredFrom, pending and the swap into drained; the
// Metrics.flushMu holder alone reads drained and owns sorted.
type sloSeries struct {
	slo SLO
	// coveredFrom is the finish time from which retained samples are complete.
	coveredFrom      time.Time
	pending, drained []sloSample
	sorted           []sloSample // ascending latency
}

type sloView struct {
	series      *sloSeries
	coveredFrom time.Time
}

// SetSLOWindow receives the scheduler's configured observation window. A
// longer window is covered only from the last retention floor.
func (m *Metrics) SetSLOWindow(window time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if window > m.sloWindow {
		for _, series := range m.slo {
			if series.coveredFrom.Before(m.sloFloor) {
				series.coveredFrom = m.sloFloor
			}
		}
	}
	m.sloWindow = window
}

// merge folds drained samples into the latency order in place, from the back,
// then drops samples that finished before floor and releases a shrunk buffer.
func (s *sloSeries) merge(floor int64) {
	added := s.drained
	slices.SortFunc(added, func(a, b sloSample) int { return cmp.Compare(a.ms, b.ms) })
	old := len(s.sorted)
	s.sorted = slices.Grow(s.sorted, len(added))[:old+len(added)]
	for i, j, w := old-1, len(added)-1, len(s.sorted)-1; j >= 0; w-- {
		if i >= 0 && s.sorted[i].ms > added[j].ms {
			s.sorted[w], i = s.sorted[i], i-1
		} else {
			s.sorted[w], j = added[j], j-1
		}
	}
	s.sorted = slices.DeleteFunc(s.sorted, func(sample sloSample) bool { return sample.finishedAt < floor })
	if cap(s.sorted) > 4*len(s.sorted) {
		s.sorted = slices.Clone(s.sorted)
	}
}

// judge reads responses that finished in [end-window,end). A window starting
// before the series coverage keeps its counts but has no quantile or verdict.
func (m *Metrics) judge(view sloView, window time.Duration, end time.Time) *SLOWindow {
	slo := view.series.slo
	from := end.Add(-window)
	target := float64(slo.TargetLatencyNanos) / float64(time.Millisecond)
	result := &SLOWindow{WindowSeconds: window.Seconds(), Percentile: slo.Percentile, TargetMS: target}
	m.sloScratch = m.sloScratch[:0]
	for _, sample := range view.series.sorted {
		if sample.finishedAt >= from.UnixNano() && sample.finishedAt < end.UnixNano() {
			m.sloScratch = append(m.sloScratch, sample.ms)
			if sample.ms <= target {
				result.WithinTarget++
			}
		}
	}
	result.Samples = uint64(len(m.sloScratch))
	if result.Samples > 0 && !from.Before(view.coveredFrom) {
		// Ascending already: sorted is in latency order.
		quantile := stat.Quantile(slo.Percentile/100, stat.Empirical, m.sloScratch, nil)
		met := quantile <= target
		result.QuantileMS, result.Met = &quantile, &met
	}
	return result
}
