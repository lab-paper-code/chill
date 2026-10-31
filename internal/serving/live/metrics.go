package live

import (
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
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
	mu                        sync.Mutex
	hub                       *Hub
	next                      time.Time
	buckets                   map[int64]*metricBucket
	workloads, meters, active map[string]struct{}
	membershipKnown           bool
	pendingGaps               []Gap
}

func NewMetrics(hub *Hub, now time.Time) *Metrics {
	return &Metrics{hub: hub, next: now.UTC().Truncate(time.Second), buckets: make(map[int64]*metricBucket),
		workloads: make(map[string]struct{}), meters: make(map[string]struct{}), active: make(map[string]struct{})}
}

func uidSet(uids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(uids))
	for _, uid := range uids {
		set[uid] = struct{}{}
	}
	return set
}

func (m *Metrics) SetWorkloads(uids []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workloads = uidSet(uids)
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

func (m *Metrics) Finished(uid string, at time.Time, completed bool, latency *time.Duration, slo time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	for _, key := range keys {
		bucket := m.buckets[key]
		message := bucket.frame()
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
