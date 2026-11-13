package live

import (
	"cmp"
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

type sloSample struct {
	receivedAt int64 // Unix nanoseconds
	ms         float64
}

// sloSamples keeps a Workload's receipts sorted by latency so a window read is
// one filtering pass instead of a sort under the flush lock.
type sloSamples struct {
	sorted, pending []sloSample
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
	sloSamples          map[string]*sloSamples
	sloSpare            []sloSample
	sloScratch          []float64
}

func NewMetrics(hub *Hub, now time.Time, queueLength, queueOldestEnqueued *atomic.Int64) *Metrics {
	return &Metrics{hub: hub, next: now.UTC().Truncate(time.Second), buckets: make(map[int64]*metricBucket),
		workloads: make(map[string]SLO), meters: make(map[string]struct{}), active: make(map[string]struct{}),
		sloSamples: make(map[string]*sloSamples), queueLength: queueLength, queueOldestEnqueued: queueOldestEnqueued}
}

func uidSet(uids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(uids))
	for _, uid := range uids {
		set[uid] = struct{}{}
	}
	return set
}

// SetWorkloads receives every registered Workload with its configured SLO.
func (m *Metrics) SetWorkloads(workloads []Workload) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workloads = make(map[string]SLO, len(workloads))
	for _, w := range workloads {
		m.workloads[w.UID] = w.SLO
	}
	for _, bucket := range m.buckets {
		if !bucket.at.Before(m.next) {
			for uid := range m.workloads {
				bucket.workload(uid)
			}
		}
	}
}

// SetSLOWindow receives the scheduler's configured observation window.
func (m *Metrics) SetSLOWindow(window time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sloWindow = window
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

// Finished takes the request's receipt time because the SLO window selects
// requests by receipt, like the observation-log cohorts.
func (m *Metrics) Finished(uid string, at, receivedAt time.Time, completed bool, latency *time.Duration, slo time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if completed && latency != nil && *latency > 0 {
		samples := m.sloSamples[uid]
		if samples == nil {
			samples = &sloSamples{}
			m.sloSamples[uid] = samples
		}
		samples.pending = append(samples.pending, sloSample{receivedAt.UnixNano(), float64(*latency) / float64(time.Millisecond)})
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

// Flush emits completed seconds plus complete replacements of dirty retained
// buckets. Corrections never add a partial bucket to the client's old counts.
func (m *Metrics) Flush(now time.Time) error {
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
	// Retain receipts for the oldest correctable bucket's window.
	m.mergeSLOSamplesLocked(m.next.Add(-10*time.Second - m.sloWindow).UnixNano())
	for _, key := range keys {
		bucket := m.buckets[key]
		message := bucket.frame()
		for uid, row := range message.Workloads {
			if row.SLO = m.sloLocked(uid, bucket.at); row.SLO != nil {
				message.Workloads[uid] = row
			}
		}
		message.ObslogQueueLength = queueLength
		message.ObslogOldestWaitSeconds = oldestWait
		message.Gaps = append(message.Gaps, m.pendingGaps...)
		if err := m.hub.PublishMetrics(now, message); err != nil {
			return err
		}
		m.pendingGaps = nil
		bucket.dirty = false
	}
	if boundary.After(m.next) {
		m.next = boundary
	}
	for key, bucket := range m.buckets {
		if bucket.at.Before(m.next.Add(-10 * time.Second)) {
			delete(m.buckets, key)
		}
	}
	return nil
}

// mergeSLOSamplesLocked folds pending receipts into the latency order and drops
// receipts older than floor in the same linear pass.
func (m *Metrics) mergeSLOSamplesLocked(floor int64) {
	for uid, samples := range m.sloSamples {
		slices.SortFunc(samples.pending, func(a, b sloSample) int { return cmp.Compare(a.ms, b.ms) })
		merged := m.sloSpare[:0]
		for sorted, pending := samples.sorted, samples.pending; len(sorted) > 0 || len(pending) > 0; {
			var next sloSample
			if len(pending) == 0 || len(sorted) > 0 && sorted[0].ms <= pending[0].ms {
				next, sorted = sorted[0], sorted[1:]
			} else {
				next, pending = pending[0], pending[1:]
			}
			if next.receivedAt >= floor {
				merged = append(merged, next)
			}
		}
		m.sloSpare, samples.sorted, samples.pending = samples.sorted[:0], merged, samples.pending[:0]
		if len(merged) == 0 {
			delete(m.sloSamples, uid)
		}
	}
}

// sloLocked judges the window ending at bucketAt with the observation-log
// cohort population and method (observation.ObservationResult.aggregate):
// requests received in [end-window,end), completed, positive elapsed latency,
// gonum stat.Empirical at the configured percentile.
func (m *Metrics) sloLocked(uid string, bucketAt time.Time) *SLOWindow {
	slo, ok := m.workloads[uid]
	if !ok || m.sloWindow <= 0 || slo.TargetLatencyNanos <= 0 || !(slo.Percentile > 0 && slo.Percentile <= 100) {
		return nil
	}
	end := bucketAt.Add(time.Second)
	from := end.Add(-m.sloWindow)
	target := float64(slo.TargetLatencyNanos) / float64(time.Millisecond)
	result := &SLOWindow{WindowSeconds: m.sloWindow.Seconds(), Percentile: slo.Percentile, TargetMS: target}
	m.sloScratch = m.sloScratch[:0]
	if samples := m.sloSamples[uid]; samples != nil {
		for _, sample := range samples.sorted {
			if sample.receivedAt >= from.UnixNano() && sample.receivedAt < end.UnixNano() {
				m.sloScratch = append(m.sloScratch, sample.ms)
				if sample.ms <= target {
					result.WithinTarget++
				}
			}
		}
	}
	result.Samples = uint64(len(m.sloScratch))
	if result.Samples > 0 {
		// Already ascending: samples.sorted is in latency order.
		quantile := stat.Quantile(slo.Percentile/100, stat.Empirical, m.sloScratch, nil)
		met := quantile <= target
		result.QuantileMS, result.Met = &quantile, &met
	}
	return result
}
