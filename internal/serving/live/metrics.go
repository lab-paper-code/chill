package live

import (
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gonum.org/v1/gonum/stat"
)

type workloadBucket struct {
	metric    Metric
	latencies []float64
}

type powerBucket struct {
	total      float64
	count      int
	invalid    bool
	observedAt time.Time
}

type metricBucket struct {
	at              time.Time
	workloads       map[string]*workloadBucket
	nodes           map[string]*powerBucket
	meters, active  map[string]struct{}
	membershipKnown bool
	gaps            []Gap
	dirty           bool
}

// Metrics only consumes accepted source events. Call Received once for the
// received event and Finished once for response_finished, not for native finish,
// a retryable durable append, or a later native supplement.
type Metrics struct {
	mu                  sync.Mutex
	hub                 *Hub
	next                time.Time
	buckets             map[int64]*metricBucket
	workloads           map[string]SLO
	meters, active      map[string]struct{}
	membershipKnown     bool
	pendingGaps         []Gap
	queueLength         *atomic.Int64
	queueOldestEnqueued *atomic.Int64
	sloWindow           time.Duration
	sloFloor            time.Time // retention floor of the last flush
	slo                 map[string]*sloSeries

	// flushMu serializes Flush. Its holder alone touches sloViews, sloScratch
	// and every sloSeries.sorted, outside mu.
	flushMu    sync.Mutex
	sloViews   map[string]sloView
	sloScratch []float64
}

func NewMetrics(hub *Hub, now time.Time, queueLength, queueOldestEnqueued *atomic.Int64) *Metrics {
	return &Metrics{hub: hub, next: now.UTC().Truncate(time.Second), buckets: make(map[int64]*metricBucket),
		workloads: make(map[string]SLO), meters: make(map[string]struct{}), active: make(map[string]struct{}),
		slo: make(map[string]*sloSeries), sloViews: make(map[string]sloView),
		queueLength: queueLength, queueOldestEnqueued: queueOldestEnqueued}
}

func uidSet(uids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(uids))
	for _, uid := range uids {
		set[uid] = struct{}{}
	}
	return set
}

// SetWorkloads receives every registered Workload with its configured SLO at
// at. A new Workload or a changed percentile or target restarts its SLO
// samples and coverage at at.
func (m *Metrics) SetWorkloads(workloads []Workload, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workloads = make(map[string]SLO, len(workloads))
	for _, w := range workloads {
		m.workloads[w.UID] = w.SLO
		if w.SLO.TargetLatencyNanos <= 0 || !(w.SLO.Percentile > 0 && w.SLO.Percentile <= 100) {
			delete(m.slo, w.UID)
		} else if series := m.slo[w.UID]; series == nil || series.slo != w.SLO {
			m.slo[w.UID] = &sloSeries{slo: w.SLO, coveredFrom: at}
		}
	}
	for uid := range m.slo {
		if _, ok := m.workloads[uid]; !ok {
			delete(m.slo, uid)
		}
	}
	for _, bucket := range m.buckets {
		if !bucket.at.Before(m.next) {
			for uid := range m.workloads {
				bucket.workload(uid)
			}
		}
	}
}

// SetPowerScope receives the complete meter set and actual active membership,
// not declared or target placements. Unknown membership makes pool sums null.
func (m *Metrics) SetPowerScope(meters, active []string, known bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.meters, m.active, m.membershipKnown = uidSet(meters), uidSet(active), known
	for _, bucket := range m.buckets {
		if bucket.at.Before(m.next) {
			continue
		}
		if !known || !maps.Equal(bucket.meters, m.meters) || !maps.Equal(bucket.active, m.active) {
			// A membership change within a second does not prove full coverage
			// of either membership for that complete second.
			bucket.membershipKnown = false
			for uid := range m.meters {
				bucket.meters[uid] = struct{}{}
			}
			for uid := range m.active {
				bucket.active[uid] = struct{}{}
			}
		}
	}
}

func (m *Metrics) bucketLocked(at time.Time, scope string) *metricBucket {
	at = at.UTC().Truncate(time.Second)
	if at.Before(m.next.Add(-10 * time.Second)) {
		m.pendingGaps = appendGap(m.pendingGaps, Gap{timestamp(at), timestamp(at.Add(time.Second)), scope, "late event exceeds 10-second correction window"})
		return nil
	}
	key := at.Unix()
	if bucket := m.buckets[key]; bucket != nil {
		bucket.dirty = true
		return bucket
	}
	bucket := &metricBucket{at: at, workloads: make(map[string]*workloadBucket, len(m.workloads)), nodes: make(map[string]*powerBucket),
		meters: maps.Clone(m.meters), active: maps.Clone(m.active), membershipKnown: m.membershipKnown, dirty: true}
	for uid := range m.workloads {
		bucket.workloads[uid] = &workloadBucket{}
	}
	m.buckets[key] = bucket
	return bucket
}

func appendGap(gaps []Gap, gap Gap) []Gap {
	if slices.Contains(gaps, gap) {
		return gaps
	}
	return append(gaps, gap)
}

func (b *metricBucket) workload(uid string) *workloadBucket {
	if b.workloads[uid] == nil {
		b.workloads[uid] = &workloadBucket{}
	}
	return b.workloads[uid]
}

func (m *Metrics) Received(uid string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if bucket := m.bucketLocked(at, "workload:"+uid); bucket != nil {
		bucket.workload(uid).metric.Received++
	}
}

// Finished counts at, the response_finished time. A completed positive latency
// joins the SLO window only under the Workload's current target.
func (m *Metrics) Finished(uid string, at time.Time, completed bool, latency *time.Duration, slo time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if series := m.slo[uid]; series != nil && completed && latency != nil && *latency > 0 && int64(slo) == series.slo.TargetLatencyNanos {
		series.pending = append(series.pending, sloSample{at.UnixNano(), float64(*latency) / float64(time.Millisecond)})
	}
	bucket := m.bucketLocked(at, "workload:"+uid)
	if bucket == nil {
		return
	}
	row := bucket.workload(uid)
	if completed {
		row.metric.Completed++
	} else {
		row.metric.Failed++
	}
	if latency != nil && *latency >= 0 {
		row.latencies = append(row.latencies, float64(*latency)/float64(time.Millisecond))
		if completed && slo > 0 && *latency <= slo {
			row.metric.WithinSLO++
		}
	}
}

// Power timestamps are collector receipt times. A nil or invalid sample records
// missing coverage, never carries a previous bucket's W into this bucket.
func (m *Metrics) Power(uid string, at time.Time, watts *float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bucket := m.bucketLocked(at, "node:"+uid)
	if bucket == nil {
		return
	}
	row := bucket.nodes[uid]
	if row == nil {
		row = &powerBucket{}
		bucket.nodes[uid] = row
	}
	if row.observedAt.IsZero() || at.After(row.observedAt) {
		row.observedAt = at
	}
	if watts == nil || math.IsNaN(*watts) || math.IsInf(*watts, 0) || *watts < 0 {
		row.invalid = true
		bucket.gaps = appendGap(bucket.gaps, Gap{timestamp(bucket.at), timestamp(bucket.at.Add(time.Second)), "node:" + uid, "power sample unavailable"})
		return
	}
	row.total += *watts
	row.count++
}

// Gap marks an explicitly unobservable interval. Affected workload rows are
// omitted, not synthesized as zero traffic. Scope is "all", "workloads",
// "workload:<uid>", "power", or "node:<uid>".
func (m *Metrics) Gap(from, to time.Time, scope, reason string) {
	if !to.After(from) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	gap := Gap{timestamp(from), timestamp(to), scope, reason}
	for at := from.UTC().Truncate(time.Second); at.Before(to); at = at.Add(time.Second) {
		if at.Before(m.next.Add(-10 * time.Second)) {
			m.pendingGaps = appendGap(m.pendingGaps, gap)
			at = m.next.Add(-10 * time.Second).Add(-time.Second)
			continue
		}
		bucket := m.bucketLocked(at, scope)
		bucket.gaps = appendGap(bucket.gaps, gap)
	}
}

func (b *metricBucket) frame() MetricsFrame {
	result := MetricsFrame{BucketAt: timestamp(b.at), Workloads: make(map[string]Metric, len(b.workloads)), Nodes: make(map[string]NodeMetric), Gaps: slices.Clone(b.gaps)}
	for uid, row := range b.workloads {
		metric := row.metric
		metric.LatencySamples = uint64(len(row.latencies))
		if len(row.latencies) > 0 {
			sort.Float64s(row.latencies)
			p50, p95, p99 := stat.Quantile(.50, stat.Empirical, row.latencies, nil), stat.Quantile(.95, stat.Empirical, row.latencies, nil), stat.Quantile(.99, stat.Empirical, row.latencies, nil)
			metric.P50MS, metric.P95MS, metric.P99MS = &p50, &p95, &p99
			p90 := stat.Quantile(.90, stat.Empirical, row.latencies, nil)
			metric.P90MS = &p90
		}
		result.Workloads[uid] = metric
	}
	for uid := range b.meters {
		result.Nodes[uid] = NodeMetric{}
	}
	for uid := range b.active {
		result.Nodes[uid] = NodeMetric{}
	}
	for uid, row := range b.nodes {
		metric := NodeMetric{}
		if !row.observedAt.IsZero() {
			observedAt := timestamp(row.observedAt)
			metric.ObservedAt = &observedAt
		}
		if row.count > 0 && !row.invalid {
			watts := row.total / float64(row.count)
			metric.Watts = &watts
		}
		result.Nodes[uid] = metric
	}
	for _, gap := range b.gaps {
		switch {
		case gap.Scope == "all" || gap.Scope == "workloads":
			clear(result.Workloads)
		case strings.HasPrefix(gap.Scope, "workload:"):
			delete(result.Workloads, strings.TrimPrefix(gap.Scope, "workload:"))
		}
		if gap.Scope == "all" || gap.Scope == "power" {
			for uid, row := range result.Nodes {
				row.Watts = nil
				result.Nodes[uid] = row
			}
		} else if strings.HasPrefix(gap.Scope, "node:") {
			uid := strings.TrimPrefix(gap.Scope, "node:")
			row := result.Nodes[uid]
			row.Watts = nil
			result.Nodes[uid] = row
		}
	}
	if b.membershipKnown {
		result.Pool.WholeWatts = sumPower(result.Nodes, b.meters)
		result.Pool.ActiveWatts = sumPower(result.Nodes, b.active)
		for uid := range b.active {
			if _, ok := b.meters[uid]; !ok {
				result.Pool.ActiveWatts = nil
				break
			}
		}
	}
	return result
}

func sumPower(nodes map[string]NodeMetric, uids map[string]struct{}) *float64 {
	total := 0.0
	for uid := range uids {
		row, ok := nodes[uid]
		if !ok || row.Watts == nil {
			return nil
		}
		total += *row.Watts
	}
	return &total
}

// metricsFlush is what Flush takes from Metrics under mu.
type metricsFlush struct {
	frames []MetricsFrame
	ends   []time.Time
	window time.Duration
	floor  time.Time
}

// Flush emits completed seconds plus complete replacements of dirty retained
// buckets. Corrections never add a partial bucket to the client's old counts.
// Only bucket projection and the SLO sample swap hold mu; the SLO merge,
// quantiles and publication run outside it.
func (m *Metrics) Flush(now time.Time) error {
	m.flushMu.Lock()
	defer m.flushMu.Unlock()
	work := m.collect(now)
	retained := 0
	for _, view := range m.sloViews {
		view.series.merge(work.floor.UnixNano())
		retained = max(retained, len(view.series.sorted))
	}
	if cap(m.sloScratch) > 4*retained {
		m.sloScratch = nil // release a shrunk window's buffer
	}
	for i, frame := range work.frames {
		if work.window > 0 {
			for uid, row := range frame.Workloads {
				if view, ok := m.sloViews[uid]; ok {
					row.SLO = m.judge(view, work.window, work.ends[i])
					frame.Workloads[uid] = row
				}
			}
		}
		if err := m.hub.PublishMetrics(now, frame); err != nil {
			return err
		}
	}
	return nil
}

func (m *Metrics) collect(now time.Time) metricsFlush {
	m.mu.Lock()
	defer m.mu.Unlock()
	var queueLength int64
	var oldestEnqueued int64
	if m.queueLength != nil {
		queueLength = m.queueLength.Load()
	}
	if m.queueOldestEnqueued != nil {
		oldestEnqueued = m.queueOldestEnqueued.Load()
	}
	var oldestWait float64
	if queueLength != 0 && oldestEnqueued != 0 {
		oldestWait = now.Sub(time.Unix(0, oldestEnqueued)).Seconds()
		if oldestWait < 0 {
			oldestWait = 0
		}
	}
	boundary := now.UTC().Truncate(time.Second)
	for at := m.next; at.Before(boundary); at = at.Add(time.Second) {
		m.bucketLocked(at, "all")
	}
	keys := make([]int64, 0, len(m.buckets))
	for key, bucket := range m.buckets {
		if bucket.dirty && bucket.at.Before(boundary) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	work := metricsFlush{frames: make([]MetricsFrame, 0, len(keys)), ends: make([]time.Time, 0, len(keys))}
	for _, key := range keys {
		bucket := m.buckets[key]
		message := bucket.frame()
		message.ObslogQueueLength = queueLength
		message.ObslogOldestWaitSeconds = oldestWait
		message.Gaps = append(message.Gaps, m.pendingGaps...)
		m.pendingGaps = nil
		bucket.dirty = false
		work.frames, work.ends = append(work.frames, message), append(work.ends, bucket.at.Add(time.Second))
	}
	// Retain samples for the window of the oldest bucket this flush can emit,
	// before next advances: every later correction is newer than it.
	m.sloFloor = m.next.Add(-10*time.Second - m.sloWindow)
	if boundary.After(m.next) {
		m.next = boundary
	}
	for key, bucket := range m.buckets {
		if bucket.at.Before(m.next.Add(-10 * time.Second)) {
			delete(m.buckets, key)
		}
	}
	work.window, work.floor = m.sloWindow, m.sloFloor
	clear(m.sloViews)
	for uid, series := range m.slo {
		series.pending, series.drained = series.drained[:0], series.pending
		m.sloViews[uid] = sloView{series, series.coveredFrom}
	}
	return work
}
