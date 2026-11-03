package observation

import (
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	serving "github.com/lab-paper-code/chill/internal/serving"
)

// OperatingPowerColumns is the opt-in columnar encoding of a power series.
// Every outer array is indexed by bucket. Node arrays retain the original node
// order within that bucket; nulls and empty collections are not substituted.
type OperatingPowerColumns struct {
	From        time.Time                   `json:"from"`
	To          time.Time                   `json:"to"`
	BucketNanos time.Duration               `json:"bucketNanos"`
	Format      string                      `json:"format"`
	Windows     OperatingPowerWindowColumns `json:"windows"`
	Nodes       OperatingPowerNodeColumns   `json:"nodes"`
}

type OperatingPowerWindowColumns struct {
	Scope                   []string                      `json:"scope"`
	WholePoolJoules         []*float64                    `json:"wholePoolJoules"`
	WholePoolMeanWatts      []*float64                    `json:"wholePoolMeanWatts"`
	ActivePoolJoules        []*float64                    `json:"activePoolJoules"`
	ActivePoolMeanWatts     []*float64                    `json:"activePoolMeanWatts"`
	Complete                []bool                        `json:"complete"`
	ReadFailures            []int                         `json:"readFailures"`
	MaximumSampleGapSeconds []float64                     `json:"maximumSampleGapSeconds"`
	Reasons                 [][]string                    `json:"reasons"`
	GapUntil                []*time.Time                  `json:"gapUntil"`
	Coverage                []serving.ObservationCoverage `json:"coverage"`
}

type OperatingPowerNodeColumns struct {
	Node                    [][]serving.NodeReference         `json:"node"`
	Meter                   [][]string                        `json:"meter"`
	Scope                   [][]string                        `json:"scope"`
	MeterJoules             [][]*float64                      `json:"meterJoules"`
	MeterComplete           [][]bool                          `json:"meterComplete"`
	GapCount                [][]int                           `json:"gapCount"`
	GapSeconds              [][]float64                       `json:"gapSeconds"`
	GapFraction             [][]float64                       `json:"gapFraction"`
	SourceIdentity          [][]metricobserver.SourceIdentity `json:"sourceIdentity"`
	Joules                  [][]*float64                      `json:"joules"`
	MeanWatts               [][]*float64                      `json:"meanWatts"`
	Samples                 [][]int                           `json:"samples"`
	ReadFailures            [][]int                           `json:"readFailures"`
	MaximumSampleGapSeconds [][]float64                       `json:"maximumSampleGapSeconds"`
	SourceTimestampAbsent   [][]bool                          `json:"sourceTimestampAbsent"`
	ObservedSamples         [][]*int                          `json:"observedSamples"`
	FirstObservedAt         [][]*time.Time                    `json:"firstObservedAt"`
	LastObservedAt          [][]*time.Time                    `json:"lastObservedAt"`
	Reasons                 [][][]string                      `json:"reasons"`
}

// Columns retains every measured value. Bucket i starts at
// max(from, truncateUTC(from,bucket)+i*bucket) and ends at
// min(to, truncateUTC(from,bucket)+(i+1)*bucket),
// using the same clipped boundaries as OperatingPowerSeries.
func (s OperatingPowerSeries) Columns() OperatingPowerColumns {
	count := len(s.Buckets)
	c := OperatingPowerColumns{From: s.From, To: s.To, BucketNanos: s.BucketNanos, Format: "columns"}
	w, n := &c.Windows, &c.Nodes
	w.Scope = make([]string, count)
	w.WholePoolJoules = make([]*float64, count)
	w.WholePoolMeanWatts = make([]*float64, count)
	w.ActivePoolJoules = make([]*float64, count)
	w.ActivePoolMeanWatts = make([]*float64, count)
	w.Complete = make([]bool, count)
	w.ReadFailures = make([]int, count)
	w.MaximumSampleGapSeconds = make([]float64, count)
	w.Reasons = make([][]string, count)
	w.GapUntil = make([]*time.Time, count)
	w.Coverage = make([]serving.ObservationCoverage, count)
	n.Node = make([][]serving.NodeReference, count)
	n.Meter = make([][]string, count)
	n.Scope = make([][]string, count)
	n.MeterJoules = make([][]*float64, count)
	n.MeterComplete = make([][]bool, count)
	n.GapCount = make([][]int, count)
	n.GapSeconds = make([][]float64, count)
	n.GapFraction = make([][]float64, count)
	n.SourceIdentity = make([][]metricobserver.SourceIdentity, count)
	n.Joules = make([][]*float64, count)
	n.MeanWatts = make([][]*float64, count)
	n.Samples = make([][]int, count)
	n.ReadFailures = make([][]int, count)
	n.MaximumSampleGapSeconds = make([][]float64, count)
	n.SourceTimestampAbsent = make([][]bool, count)
	n.ObservedSamples = make([][]*int, count)
	n.FirstObservedAt = make([][]*time.Time, count)
	n.LastObservedAt = make([][]*time.Time, count)
	n.Reasons = make([][][]string, count)
	for i, b := range s.Buckets {
		w.Scope[i], w.WholePoolJoules[i], w.WholePoolMeanWatts[i] = b.Scope, b.WholePoolJoules, b.WholePoolMeanWatts
		w.ActivePoolJoules[i], w.ActivePoolMeanWatts[i], w.Complete[i] = b.ActivePoolJoules, b.ActivePoolMeanWatts, b.Complete
		w.ReadFailures[i], w.MaximumSampleGapSeconds[i] = b.ReadFailures, b.MaximumSampleGapSeconds
		w.Reasons[i], w.GapUntil[i], w.Coverage[i] = b.Reasons, b.GapUntil, b.Coverage
		if b.Nodes == nil {
			continue
		}
		length := len(b.Nodes)
		n.Node[i] = make([]serving.NodeReference, length)
		n.Meter[i] = make([]string, length)
		n.Scope[i] = make([]string, length)
		n.MeterJoules[i] = make([]*float64, length)
		n.MeterComplete[i] = make([]bool, length)
		n.GapCount[i] = make([]int, length)
		n.GapSeconds[i] = make([]float64, length)
		n.GapFraction[i] = make([]float64, length)
		n.SourceIdentity[i] = make([]metricobserver.SourceIdentity, length)
		n.Joules[i] = make([]*float64, length)
		n.MeanWatts[i] = make([]*float64, length)
		n.Samples[i] = make([]int, length)
		n.ReadFailures[i] = make([]int, length)
		n.MaximumSampleGapSeconds[i] = make([]float64, length)
		n.SourceTimestampAbsent[i] = make([]bool, length)
		n.ObservedSamples[i] = make([]*int, length)
		n.FirstObservedAt[i] = make([]*time.Time, length)
		n.LastObservedAt[i] = make([]*time.Time, length)
		n.Reasons[i] = make([][]string, length)
		for j, node := range b.Nodes {
			n.Node[i][j], n.Meter[i][j], n.Scope[i][j] = node.Node, node.Meter, node.Scope
			n.MeterJoules[i][j], n.MeterComplete[i][j] = node.MeterJoules, node.MeterComplete
			n.GapCount[i][j] = node.GapCount
			n.GapSeconds[i][j], n.GapFraction[i][j], n.SourceIdentity[i][j] = node.GapSeconds, node.GapFraction, node.SourceIdentity
			n.Joules[i][j], n.MeanWatts[i][j], n.Samples[i][j] = node.Joules, node.MeanWatts, node.Samples
			n.ReadFailures[i][j], n.MaximumSampleGapSeconds[i][j] = node.ReadFailures, node.MaximumSampleGapSeconds
			n.SourceTimestampAbsent[i][j], n.ObservedSamples[i][j] = node.SourceTimestampAbsent, node.ObservedSamples
			n.FirstObservedAt[i][j], n.LastObservedAt[i][j], n.Reasons[i][j] = node.FirstObservedAt, node.LastObservedAt, node.Reasons
		}
	}
	return c
}
