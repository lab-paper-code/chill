package serving

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
)

// OperatingPowerMeter is the configured whole-node meter, including inactive Nodes.
type OperatingPowerMeter struct {
	Node   NodeReference
	Meter  string
	Scope  string
	Source metricobserver.SourceIdentity
}

type OperatingNodePower struct {
	Node                  NodeReference `json:"node"`
	Meter                 string        `json:"meter"`
	Scope                 string        `json:"scope"`
	Joules                *float64      `json:"joules"`
	MeanWatts             *float64      `json:"meanWatts"`
	Samples               int           `json:"samples"`
	SourceTimestampAbsent bool          `json:"sourceTimestampAbsent"`
	Reasons               []string      `json:"reasons"`
}

type OperatingPowerWindow struct {
	From                time.Time            `json:"from"`
	To                  time.Time            `json:"to"`
	Scope               string               `json:"scope"`
	Nodes               []OperatingNodePower `json:"nodes"`
	WholePoolJoules     *float64             `json:"wholePoolJoules"`
	WholePoolMeanWatts  *float64             `json:"wholePoolMeanWatts"`
	ActivePoolJoules    *float64             `json:"activePoolJoules"`
	ActivePoolMeanWatts *float64             `json:"activePoolMeanWatts"`
	Complete            bool                 `json:"complete"`
	Reasons             []string             `json:"reasons"`
	IncompleteWindows   []string             `json:"incompleteWindows"`
}

type operatingPowerHistory struct {
	meter         OperatingPowerMeter
	samples       map[time.Time]float64
	policy        powerwindow.Policy
	hasPolicy     bool
	before, after operatingPowerNeighbor
}

type operatingPowerNeighbor struct {
	at      time.Time
	records []observationRecord
}

// Decode only the physical power series and membership used by the reducer;
// decisions and snapshot profiles can contain large unrelated distributions.
func operatingPowerRecord(window string, raw []byte) (observationRecord, error) {
	var projection struct {
		SchemaVersion string `json:"schemaVersion"`
		Window        string `json:"window"`
		Scheduler     *struct {
			Power         *metricobserver.Result `json:"power"`
			Node          *NodeReference         `json:"node"`
			Meter         string                 `json:"meter"`
			PowerScope    string                 `json:"powerScope"`
			PowerCoverage *powerwindow.Policy    `json:"powerCoverage"`
			Snapshot      *struct {
				At        time.Time `json:"at"`
				Instances []struct {
					Node         NodeReference `json:"node"`
					Ready        bool          `json:"ready"`
					Stopped      bool          `json:"stopped"`
					WorkloadUIDs []string      `json:"workloadUIDs"`
				} `json:"instances"`
			} `json:"snapshot"`
		} `json:"scheduler"`
	}
	if err := json.Unmarshal(raw, &projection); err != nil {
		return observationRecord{}, err
	}
	if projection.SchemaVersion != observationSchema || projection.Window != window {
		return observationRecord{}, errObservationRecordIdentity
	}
	record := observationRecord{SchemaVersion: projection.SchemaVersion, Window: projection.Window}
	if event := projection.Scheduler; event != nil {
		record.Scheduler = &SchedulerObservation{
			Power: event.Power, Node: event.Node, Meter: event.Meter,
			PowerScope: event.PowerScope, PowerCoverage: event.PowerCoverage,
		}
		if snapshot := event.Snapshot; snapshot != nil {
			membership := &SchedulerSnapshot{At: snapshot.At, Instances: make([]SchedulerInstance, len(snapshot.Instances))}
			for index, instance := range snapshot.Instances {
				membership.Instances[index] = SchedulerInstance{
					Node: instance.Node, Ready: instance.Ready,
					Stopped: instance.Stopped, WorkloadUIDs: instance.WorkloadUIDs,
				}
			}
			record.Scheduler.Snapshot = membership
		}
	}
	return record, nil
}

// OperatingPower integrates raw samples over one interval, never record totals.
func (j *Journal) OperatingPower(from, to time.Time, meters []OperatingPowerMeter, limit int) (OperatingPowerWindow, error) {
	result := OperatingPowerWindow{From: from, To: to, Scope: "whole-pool-wall", Nodes: []OperatingNodePower{}, Reasons: []string{}, IncompleteWindows: []string{}}
	if from.IsZero() || !from.Before(to) || limit <= 0 {
		return result, failure("InvalidQuery", "positive explicit power interval and observation bound are required")
	}
	snapshot, err := j.snapshot("scheduler")
	if err != nil {
		return result, err
	}
	byUID, history := configuredOperatingMeters(&result, meters)
	var snapshots []SchedulerSnapshot
	var baseline *SchedulerSnapshot
	baselineWindow := ""
	rawWindows := map[string]bool{}
	matched := 0
	err = snapshot.scanRaw("scheduler", func(window string, raw []byte) (bool, error) {
		record, err := operatingPowerRecord(window, raw)
		if err != nil {
			return false, err
		}
		event := record.Scheduler
		if event == nil {
			return false, nil
		}
		selected := false
		if snapshot := event.Snapshot; snapshot != nil && !snapshot.At.IsZero() {
			if !snapshot.At.After(from) {
				if baseline == nil || !snapshot.At.Before(baseline.At) {
					baseline = snapshot
					baselineWindow = record.Window
				}
			} else if snapshot.At.Before(to) {
				selected = true
				if matched < limit {
					snapshots = append(snapshots, *snapshot)
				}
			}
		}
		power := event.Power
		if power != nil && event.Node != nil {
			if index, expected := byUID[event.Node.UID]; expected && matchesOperatingMeter(history[index].meter, event) {
				// Only configured physical series may compete for boundary support.
				retainOperatingNeighbors(&history[index], record, from, to, limit)
			}
			if index, expected := byUID[event.Node.UID]; !expected || !matchesOperatingMeter(history[index].meter, event) {
				return false, nil
			}
		} else if power != nil {
			return false, nil
		}
		if power != nil && !power.StartedAt.After(to) && !power.EndedAt.Before(from) {
			rawWindows[record.Window] = true
			selected = true
			if matched < limit && event.Node != nil {
				if index, expected := byUID[event.Node.UID]; expected && matchesOperatingMeter(history[index].meter, event) {
					mergeOperatingPower(&result.Nodes[index], &history[index], event, from, to)
				}
			}
		}
		if selected {
			matched++
		}
		return false, nil
	})
	if err != nil {
		return result, err
	}
	matched = mergeOperatingNeighbors(&result, history, rawWindows, matched, limit)
	rawComplete := operatingPowerCompleteness(&result, snapshot.windows, rawWindows, matched, limit)
	snapshots, activeValid := operatingMembership(&result, snapshot.windows, baseline, baselineWindow, snapshots, byUID, rawComplete)
	sumOperatingPower(&result, history, snapshots, rawComplete, activeValid)
	return result, nil
}

func configuredOperatingMeters(result *OperatingPowerWindow, meters []OperatingPowerMeter) (map[string]int, []operatingPowerHistory) {
	byUID := map[string]int{}
	history := []operatingPowerHistory{}
	for _, meter := range meters {
		if index, found := byUID[meter.Node.UID]; found {
			node := &result.Nodes[index]
			if node.Meter != meter.Meter || node.Scope != meter.Scope || node.Node != meter.Node || !sameOperatingSource(history[index].meter.Source, meter.Source) {
				node.Reasons = append(node.Reasons, "ambiguous configured meters for Node UID")
			}
			continue
		}
		byUID[meter.Node.UID] = len(result.Nodes)
		node := OperatingNodePower{Node: meter.Node, Meter: meter.Meter, Scope: "node-exclusive-wall", Reasons: []string{}}
		if meter.Node.UID == "" || meter.Node.Name == "" || meter.Meter == "" || meter.Scope != "node-exclusive-wall" || meter.Source.Kind == "" || meter.Source.Endpoint == "" || meter.Source.Metric == "" {
			node.Reasons = append(node.Reasons, "configured meter lacks exclusive whole-node identity/scope")
		}
		result.Nodes = append(result.Nodes, node)
		history = append(history, operatingPowerHistory{meter: meter, samples: map[time.Time]float64{}})
	}
	if len(meters) == 0 {
		result.Reasons = append(result.Reasons, "no configured whole-node meters")
	}
	return byUID, history
}

func sameOperatingSource(a, b metricobserver.SourceIdentity) bool {
	return a.Kind == b.Kind && a.Endpoint == b.Endpoint && a.Metric == b.Metric && maps.Equal(a.MetricLabels, b.MetricLabels)
}

func matchesOperatingMeter(meter OperatingPowerMeter, event *SchedulerObservation) bool {
	return event.Node != nil && *event.Node == meter.Node && event.Meter == meter.Meter &&
		event.PowerScope == meter.Scope && sameOperatingSource(meter.Source, event.Power.Source)
}

func mergeOperatingPower(node *OperatingNodePower, raw *operatingPowerHistory, event *SchedulerObservation, from, to time.Time) {
	if event.Meter != node.Meter || event.PowerScope != node.Scope {
		node.Reasons = append(node.Reasons, "ambiguous retained meter/scope for Node UID")
		return
	}
	power := event.Power
	// PowerError is the collector chunk's verdict, not this query's coverage.
	// Preserve actual source failures in the selected interval; integrate the
	// merged real samples below using the recorded policy.
	for _, failure := range power.Failures {
		if !failure.ObservedAt.Before(from) && !failure.ObservedAt.After(to) {
			node.Reasons = append(node.Reasons, fmt.Sprintf("source read failed (%s): %s", failure.Reason, failure.Message))
		}
	}
	node.SourceTimestampAbsent = node.SourceTimestampAbsent || power.Summary.SourceTimestampAbsent
	if event.PowerCoverage == nil {
		node.Reasons = append(node.Reasons, "raw observation has no recorded coverage policy")
	} else if !raw.hasPolicy {
		raw.policy, raw.hasPolicy = *event.PowerCoverage, true
	} else {
		// The intersection applies every recorded policy without
		// repeatedly copying/sorting the same raw sample set.
		raw.policy.MaximumBoundaryDistance = min(raw.policy.MaximumBoundaryDistance, event.PowerCoverage.MaximumBoundaryDistance)
		raw.policy.MaximumGap = min(raw.policy.MaximumGap, event.PowerCoverage.MaximumGap)
		raw.policy.MinimumCoverage = max(raw.policy.MinimumCoverage, event.PowerCoverage.MinimumCoverage)
	}
	for _, sample := range power.Samples {
		retainOperatingSample(node, raw, sample.ObservedAt.UTC(), sample.Watts, from, to)
	}
}

// Adjacent observation windows can supply the nearest real boundary samples.
func retainOperatingNeighbors(raw *operatingPowerHistory, record observationRecord, from, to time.Time, limit int) {
	power := record.Scheduler.Power
	for _, sample := range power.Samples {
		if policy := record.Scheduler.PowerCoverage; policy != nil &&
			(sample.ObservedAt.Before(from) && from.Sub(sample.ObservedAt) > policy.MaximumBoundaryDistance ||
				sample.ObservedAt.After(to) && sample.ObservedAt.Sub(to) > policy.MaximumBoundaryDistance) {
			continue // Outside samples must satisfy the recorded boundary policy.
		}
		if sample.ObservedAt.Before(from) {
			retainOperatingNeighbor(&raw.before, record, sample.ObservedAt, true, limit)
		}
		if sample.ObservedAt.After(to) {
			retainOperatingNeighbor(&raw.after, record, sample.ObservedAt, false, limit)
		}
	}
}

func retainOperatingNeighbor(neighbor *operatingPowerNeighbor, record observationRecord, at time.Time, before bool, limit int) {
	if neighbor.at.IsZero() || before && at.After(neighbor.at) || !before && at.Before(neighbor.at) {
		neighbor.at = at
		neighbor.records = neighbor.records[:0]
	}
	if len(neighbor.records) > 0 && neighbor.records[len(neighbor.records)-1].Scheduler == record.Scheduler {
		return
	}
	if at.Equal(neighbor.at) && len(neighbor.records) <= limit {
		neighbor.records = append(neighbor.records, record)
	}
}

func mergeOperatingNeighbors(result *OperatingPowerWindow, history []operatingPowerHistory, rawWindows map[string]bool, matched, limit int) int {
	seen := map[*SchedulerObservation]bool{}
	for index := range history {
		raw, node := &history[index], &result.Nodes[index]
		for side, neighbor := range []operatingPowerNeighbor{raw.before, raw.after} {
			boundary := result.From
			if side == 1 {
				boundary = result.To
			}
			if _, exact := raw.samples[boundary.UTC()]; exact {
				continue
			}
			for _, record := range neighbor.records {
				power := record.Scheduler.Power
				if !power.StartedAt.After(result.To) && !power.EndedAt.Before(result.From) {
					continue // Already selected by the interval scan.
				}
				if seen[record.Scheduler] {
					continue
				}
				seen[record.Scheduler] = true
				rawWindows[record.Window] = true
				if matched < limit {
					mergeOperatingPower(node, raw, record.Scheduler, result.From, result.To)
				}
				matched++
			}
		}
	}
	return matched
}

func retainOperatingSample(node *OperatingNodePower, raw *operatingPowerHistory, at time.Time, watts float64, from, to time.Time) {
	// Keep every real sample inside and only the closest real sample outside
	// either boundary; interpolated integration points never inflate Samples.
	if at.Before(from) || at.After(to) {
		for previous := range raw.samples {
			if at.Before(from) && previous.Before(from) {
				if previous.After(at) {
					return
				}
				if previous.Before(at) {
					delete(raw.samples, previous)
				}
			}
			if at.After(to) && previous.After(to) {
				if previous.Before(at) {
					return
				}
				if previous.After(at) {
					delete(raw.samples, previous)
				}
			}
		}
	}
	if previous, exists := raw.samples[at]; exists && previous != watts {
		node.Reasons = append(node.Reasons, "conflicting duplicate raw sample timestamp")
	} else {
		raw.samples[at] = watts
	}
}

func operatingPowerCompleteness(result *OperatingPowerWindow, windows []ObservationWindow, rawWindows map[string]bool, matched, limit int) bool {
	if matched > limit {
		result.Reasons = append(result.Reasons, "selected raw observation count exceeds query bound")
	}
	for _, window := range windows {
		if window.Incomplete && (rawWindows[window.ID] || window.overlaps(result.From, result.To)) {
			result.IncompleteWindows = append(result.IncompleteWindows, window.ID)
		}
	}
	if len(result.IncompleteWindows) > 0 {
		result.Reasons = append(result.Reasons, "retained observation history is incomplete")
	}
	return len(result.Reasons) == 0
}

func operatingMembership(result *OperatingPowerWindow, windows []ObservationWindow, baseline *SchedulerSnapshot, baselineWindow string, snapshots []SchedulerSnapshot, byUID map[string]int, rawComplete bool) ([]SchedulerSnapshot, bool) {
	activeValid := baseline != nil && rawComplete
	if baseline == nil {
		result.Reasons = append(result.Reasons, "active membership has no retained snapshot at interval start")
	} else {
		for _, window := range windows {
			if window.Incomplete && (window.ID == baselineWindow || window.overlaps(baseline.At, result.From)) {
				activeValid = false
				result.Reasons = append(result.Reasons, "active membership history since baseline snapshot is incomplete")
				break
			}
		}
		snapshots = append(snapshots, *baseline)
	}
	sort.SliceStable(snapshots, func(a, b int) bool { return snapshots[a].At.Before(snapshots[b].At) })
	for _, snapshot := range snapshots {
		for _, instance := range snapshot.Instances {
			if instance.Ready && !instance.Stopped && len(instance.WorkloadUIDs) > 0 {
				if _, found := byUID[instance.Node.UID]; !found {
					activeValid = false
					result.Reasons = append(result.Reasons, "active Node has no configured whole-node meter: "+instance.Node.UID)
				}
			}
		}
	}
	return snapshots, activeValid
}

func integrateOperatingNode(node *OperatingNodePower, raw *operatingPowerHistory, from, to time.Time, rawComplete bool) []powerwindow.Sample {
	points := make([]powerwindow.Sample, 0, len(raw.samples))
	_, exactFrom := raw.samples[from.UTC()]
	_, exactTo := raw.samples[to.UTC()]
	for at, watts := range raw.samples {
		if at.Before(from) && (exactFrom || raw.hasPolicy && from.Sub(at) > raw.policy.MaximumBoundaryDistance) ||
			at.After(to) && (exactTo || raw.hasPolicy && at.Sub(to) > raw.policy.MaximumBoundaryDistance) {
			continue
		}
		points = append(points, powerwindow.Sample{ObservedAt: at, Watts: watts})
	}
	sort.Slice(points, func(a, b int) bool { return points[a].ObservedAt.Before(points[b].ObservedAt) })
	node.Samples = len(points)
	var joules float64
	if !raw.hasPolicy {
		node.Reasons = append(node.Reasons, "no retained raw samples with coverage policy")
	} else {
		var coverageErr error
		points, joules, coverageErr = integrateOperatingSamples(points, from, to, raw.policy)
		if coverageErr != nil {
			node.Reasons = append(node.Reasons, coverageErr.Error())
		}
	}
	// Global incomplete history also invalidates individual reported totals.
	if !rawComplete {
		node.Reasons = append(node.Reasons, "raw history for selected interval is incomplete")
	}
	if len(node.Reasons) == 0 {
		watts := joules / to.Sub(from).Seconds()
		node.Joules, node.MeanWatts = &joules, &watts
	}
	return points
}

func integrateOperatingSamples(points []powerwindow.Sample, from, to time.Time, policy powerwindow.Policy) ([]powerwindow.Sample, float64, error) {
	if len(points) >= 2 && (points[0].ObservedAt.Before(from) || points[len(points)-1].ObservedAt.After(to)) {
		if operatingBoundaryDistance(points, from) > policy.MaximumBoundaryDistance || operatingBoundaryDistance(points, to) > policy.MaximumBoundaryDistance {
			return points, 0, errors.New("sample boundary distance exceeds policy")
		}
		// Check the original source gap/values before clipping. Interpolation
		// must not hide a gap disallowed by the recorded coverage policy.
		_, err := powerwindow.Integrate(powerwindow.Window{Start: points[0].ObservedAt, End: points[len(points)-1].ObservedAt, Samples: points}, policy)
		if err != nil {
			return points, 0, err
		}
		points = clipOperatingSamples(make([]powerwindow.Sample, 0, len(points)+2), points, from, to)
	}
	joules, err := powerwindow.Integrate(powerwindow.Window{Start: from, End: to, Samples: points}, policy)
	return points, joules, err
}

func operatingBoundaryDistance(points []powerwindow.Sample, boundary time.Time) time.Duration {
	index := sort.Search(len(points), func(i int) bool { return !points[i].ObservedAt.Before(boundary) })
	if index == 0 {
		return points[0].ObservedAt.Sub(boundary)
	}
	if index == len(points) {
		return boundary.Sub(points[index-1].ObservedAt)
	}
	return min(boundary.Sub(points[index-1].ObservedAt), points[index].ObservedAt.Sub(boundary))
}

func sumOperatingPower(result *OperatingPowerWindow, history []operatingPowerHistory, snapshots []SchedulerSnapshot, rawComplete, activeValid bool) {
	from, to := result.From, result.To
	wholeValid := rawComplete
	var whole, active float64
	for index := range result.Nodes {
		node, raw := &result.Nodes[index], &history[index]
		points := integrateOperatingNode(node, raw, from, to, rawComplete)
		if node.Joules == nil {
			wholeValid = false
			if len(node.Reasons) > 0 {
				result.Reasons = append(result.Reasons, fmt.Sprintf("Node %s (%s): %s", node.Node.Name, node.Node.UID, node.Reasons[0]))
			}
			continue
		}
		whole += *node.Joules
		if activeValid {
			value, activeErr := operatingActiveJoules(from, to, node.Node.UID, points, snapshots, raw.policy)
			if activeErr != nil {
				activeValid = false
				result.Reasons = append(result.Reasons, "active membership interval: "+activeErr.Error())
			} else {
				active += value
			}
		}
	}
	if math.IsInf(whole, 0) || math.IsNaN(whole) || math.IsInf(whole/to.Sub(from).Seconds(), 0) {
		wholeValid = false
		result.Reasons = append(result.Reasons, "whole-pool energy or mean power is non-finite")
	}
	if math.IsInf(active, 0) || math.IsNaN(active) || math.IsInf(active/to.Sub(from).Seconds(), 0) {
		activeValid = false
		result.Reasons = append(result.Reasons, "active-pool energy or mean power is non-finite")
	}
	// A missing Node excludes the whole total, but never substitutes partial zero.
	if wholeValid {
		watts := whole / to.Sub(from).Seconds()
		result.WholePoolJoules, result.WholePoolMeanWatts = &whole, &watts
	}
	if activeValid && wholeValid {
		watts := active / to.Sub(from).Seconds()
		result.ActivePoolJoules, result.ActivePoolMeanWatts = &active, &watts
	}
	result.Complete = result.WholePoolJoules != nil && result.ActivePoolJoules != nil
}

// Clip the already coverage-checked trapezoids at actual membership transitions.
// Interpolated integration boundaries are not additional source observations.
func operatingActiveJoules(from, to time.Time, uid string, points []powerwindow.Sample, snapshots []SchedulerSnapshot, policy powerwindow.Policy) (float64, error) {
	if points[0].ObservedAt.After(from) {
		points = append([]powerwindow.Sample{{ObservedAt: from, Watts: points[0].Watts}}, points...)
	}
	if points[len(points)-1].ObservedAt.Before(to) {
		points = append(points, powerwindow.Sample{ObservedAt: to, Watts: points[len(points)-1].Watts})
	}
	var joules float64
	clipped := make([]powerwindow.Sample, 0, len(points)+2)
	for index, snapshot := range snapshots {
		start, end := maxTime(from, snapshot.At), to
		if index+1 < len(snapshots) {
			end = snapshots[index+1].At
		}
		if !start.Before(end) {
			continue
		}
		member := false
		for _, instance := range snapshot.Instances {
			if instance.Node.UID == uid && instance.Ready && !instance.Stopped && len(instance.WorkloadUIDs) > 0 {
				member = true
				break
			}
		}
		if !member {
			continue
		}
		clipped = clipOperatingSamples(clipped[:0], points, start, end)
		value, err := powerwindow.Integrate(powerwindow.Window{Start: start, End: end, Samples: clipped}, policy)
		if err != nil {
			return 0, err
		}
		joules += value
	}
	return joules, nil
}

// Reuse the same trapezoid interpolation for query and membership boundaries.
// These points are integration geometry, not additional source observations.
func clipOperatingSamples(clipped, points []powerwindow.Sample, start, end time.Time) []powerwindow.Sample {
	for p := 1; p < len(points); p++ {
		a, b := points[p-1], points[p]
		left, right := maxTime(start, a.ObservedAt), minTime(end, b.ObservedAt)
		if !left.Before(right) {
			continue
		}
		duration := b.ObservedAt.Sub(a.ObservedAt).Seconds()
		leftWatts := a.Watts + (b.Watts-a.Watts)*(left.Sub(a.ObservedAt).Seconds()/duration)
		rightWatts := a.Watts + (b.Watts-a.Watts)*(right.Sub(a.ObservedAt).Seconds()/duration)
		if len(clipped) == 0 {
			clipped = append(clipped, powerwindow.Sample{ObservedAt: left, Watts: leftWatts})
		}
		clipped = append(clipped, powerwindow.Sample{ObservedAt: right, Watts: rightWatts})
	}
	return clipped
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
