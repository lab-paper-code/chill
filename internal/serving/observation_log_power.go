package serving

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	jsoniter "github.com/json-iterator/go"

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
	GapUntil            *time.Time           `json:"gapUntil,omitempty"`
	Coverage            ObservationCoverage  `json:"coverage"`
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
	records []*SchedulerObservation
}

// OperatingPowerRecord decodes only the power series and snapshot membership
// of one stored scheduler line; decisions can carry large unrelated payloads.
// A nil catalog keeps membership only.
func OperatingPowerRecord(raw []byte, catalog *ObservationCatalog) (*SchedulerObservation, error) {
	var projection struct {
		At          time.Time       `json:"at"`
		Node        *NodeReference  `json:"node"`
		Meter       string          `json:"meter"`
		StartedAt   *time.Time      `json:"startedAt"`
		Samples     json.RawMessage `json:"samples"`
		Failures    json.RawMessage `json:"failures"`
		PowerJoules json.RawMessage `json:"powerJoules"`
		PowerError  string          `json:"powerError"`
		Snapshot    *struct {
			At        time.Time `json:"at"`
			Instances []struct {
				Node             NodeReference `json:"node"`
				Ready            bool          `json:"ready"`
				Stopped          bool          `json:"stopped"`
				WorkloadUID      string        `json:"workloadUID"`
				PodUID           string        `json:"podUID"`
				OwnerWorkloadUID string        `json:"ownerWorkloadUID"`
			} `json:"instances"`
		} `json:"snapshot"`
	}
	if err := observationJSONSyntaxError(raw); err != nil {
		return nil, err
	}
	if err := jsoniter.ConfigCompatibleWithStandardLibrary.Unmarshal(raw, &projection); err != nil {
		return nil, err
	}
	event := &SchedulerObservation{At: projection.At}
	if snapshot := projection.Snapshot; snapshot != nil {
		membership := &SchedulerSnapshot{At: snapshot.At, Instances: make([]SchedulerInstance, len(snapshot.Instances))}
		for index, instance := range snapshot.Instances {
			membership.Instances[index] = SchedulerInstance{
				Node: instance.Node, Ready: instance.Ready, Stopped: instance.Stopped, WorkloadUID: instance.WorkloadUID,
				PodUID: instance.PodUID, OwnerWorkloadUID: instance.OwnerWorkloadUID,
			}
		}
		event.Snapshot = membership
	}
	if projection.Meter == "" || catalog == nil {
		return event, nil
	}
	// Preserve standard numeric decoding for the consumed measurement blocks.
	line := schedulerLine{At: projection.At, Node: projection.Node, Meter: projection.Meter, StartedAt: projection.StartedAt, PowerError: projection.PowerError}
	for _, field := range []struct {
		raw   json.RawMessage
		value any
	}{{projection.Samples, &line.Samples}, {projection.Failures, &line.Failures}, {projection.PowerJoules, &line.PowerJoules}} {
		if len(field.raw) > 0 {
			if err := json.Unmarshal(field.raw, field.value); err != nil {
				return nil, err
			}
		}
	}
	return event, resolvePower(event, &line, catalog)
}

// OperatingPower reads boundary samples and searches older source hours in
// reverse order until it finds the membership snapshot in force at from.
func (l *ObservationLog) OperatingPower(ctx context.Context, from, to time.Time, meters []OperatingPowerMeter, limit int) (OperatingPowerWindow, error) {
	if from.IsZero() || !from.Before(to) || limit <= 0 {
		return ReduceOperatingPower(from, to, meters, limit, nil)
	}
	start, end := from.Add(-time.Minute), to.Add(time.Minute)
	read, err := l.prepare(ctx, map[string][2]time.Time{observationSchedulerStream: {{}, end}})
	if err != nil {
		return OperatingPowerWindow{}, err
	}
	defer read.close()
	baseline := false
	coverageFrom := start
	result, err := ReduceOperatingPower(from, to, meters, limit, func(yield func(*SchedulerObservation) error) error {
		var scanner observationScanner
		visit := func(raw []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			record, err := OperatingPowerRecord(raw, read.catalog())
			if err != nil {
				return err
			}
			baseline = baseline || record.Snapshot != nil && !record.Snapshot.At.After(from)
			return yield(record)
		}
		sources := read.sources[observationSchedulerStream]
		for _, source := range sources {
			if source.hour.Add(time.Hour).After(start) && !source.hour.After(end) {
				if err := l.readCatalog(read, source.hour); err != nil {
					return err
				}
				if source.object != nil {
					if err := read.scanSource(observationSchedulerStream, source, &scanner, visit); err != nil {
						return err
					}
				} else {
					read.scanningHour = source.hour
					for _, file := range read.files[observationSchedulerStream] {
						if !file.at.Truncate(time.Hour).Equal(source.hour) || file.at.Before(start.Truncate(time.Minute)) || file.at.After(end) {
							continue
						}
						if err := scanner.scan(observationContextReader{ctx: ctx, source: file.file}, file.size, visit); err != nil {
							return err
						}
					}
				}
			}
		}
		return l.scanOlderPowerSources(read, start, &coverageFrom, &baseline, &scanner, visit)
	})
	result.Coverage = read.coverage(map[string][2]time.Time{observationSchedulerStream: {coverageFrom, end}})
	result.GapUntil = result.Coverage.gapUntil(from)
	// Coverage includes the wider boundary/baseline search. Missing history
	// outside the requested interval is not missing energy: the reducer checks
	// the actual boundary samples and membership needed by [from,to].
	for _, gap := range result.Coverage.Gaps {
		if gap.From.Before(to) && gap.To.After(from) {
			result.Complete = false
			result.Reasons = append(result.Reasons, "scheduler stream unavailable in observation coverage")
			break
		}
	}
	return result, err
}

func (l *ObservationLog) scanOlderPowerSources(read *observationRead, start time.Time, coverageFrom *time.Time, baseline *bool, scanner *observationScanner, visit func([]byte) error) error {
	sources := read.sources[observationSchedulerStream]
	for index := len(sources) - 1; index >= 0 && !*baseline; index-- {
		source := sources[index]
		if !source.hour.Before(start) {
			continue
		}
		if err := l.readCatalog(read, source.hour); err != nil {
			return err
		}
		if source.object != nil {
			// The boundary scan already read the entire archived hour.
			if source.hour.Add(time.Hour).After(start) {
				continue
			}
			if source.hour.Before(*coverageFrom) {
				*coverageFrom = source.hour
			}
			if err := read.scanSource(observationSchedulerStream, source, scanner, visit); err != nil {
				return err
			}
			continue
		}
		read.scanningHour = source.hour
		files := read.files[observationSchedulerStream]
		for file := len(files) - 1; file >= 0 && !*baseline; file-- {
			if !files[file].at.Truncate(time.Hour).Equal(source.hour) || !files[file].at.Before(start.Truncate(time.Minute)) {
				continue
			}
			if files[file].at.Before(*coverageFrom) {
				*coverageFrom = files[file].at
			}
			if err := scanner.scan(observationContextReader{ctx: read.ctx, source: files[file].file}, files[file].size, visit); err != nil {
				return err
			}
		}
	}
	return read.ctx.Err()
}

// ReduceOperatingPower integrates the whole and active POOL energy of
// [from,to] from scheduler records given in any order. The ChillServer and the
// offline table tool share it.
func ReduceOperatingPower(from, to time.Time, meters []OperatingPowerMeter, limit int, records func(yield func(*SchedulerObservation) error) error) (OperatingPowerWindow, error) {
	result := OperatingPowerWindow{From: from, To: to, Scope: "whole-pool-wall", Nodes: []OperatingNodePower{}, Reasons: []string{}}
	if from.IsZero() || !from.Before(to) || limit <= 0 {
		return result, failure("InvalidQuery", "positive explicit power interval and observation bound are required")
	}
	byUID, history := configuredOperatingMeters(&result, meters)
	var snapshots []SchedulerSnapshot
	var baseline *SchedulerSnapshot
	matched := 0
	err := records(func(event *SchedulerObservation) error {
		selected := false
		if snapshot := event.Snapshot; snapshot != nil && !snapshot.At.IsZero() {
			if !snapshot.At.After(from) {
				if baseline == nil || !snapshot.At.Before(baseline.At) {
					baseline = snapshot
				}
			} else if snapshot.At.Before(to) {
				selected = true
				if matched < limit {
					snapshots = append(snapshots, *snapshot)
				}
			}
		}
		power := event.Power
		if power != nil {
			if event.Node == nil {
				return nil
			}
			index, expected := byUID[event.Node.UID]
			if !expected || !matchesOperatingMeter(history[index].meter, event) {
				return nil
			}
			// Only configured physical series may compete for boundary support.
			retainOperatingNeighbors(&history[index], event, from, to, limit)
			if !power.StartedAt.After(to) && !power.EndedAt.Before(from) {
				selected = true
				if matched < limit {
					mergeOperatingPower(&result.Nodes[index], &history[index], event, from, to)
				}
			}
		}
		if selected {
			matched++
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	matched = mergeOperatingNeighbors(&result, history, matched, limit)
	if matched > limit {
		result.Reasons = append(result.Reasons, "selected raw observation count exceeds query bound")
	}
	rawComplete := len(result.Reasons) == 0
	snapshots, activeValid := operatingMembership(&result, baseline, snapshots, byUID, rawComplete)
	sumOperatingPower(&result, history, snapshots, rawComplete, activeValid)
	return result, nil
}

func configuredOperatingMeters(result *OperatingPowerWindow, meters []OperatingPowerMeter) (map[string]int, []operatingPowerHistory) {
	byUID := map[string]int{}
	history := []operatingPowerHistory{}
	for _, meter := range meters {
		if index, found := byUID[meter.Node.UID]; found {
			node := &result.Nodes[index]
			if node.Meter != meter.Meter || node.Scope != meter.Scope || node.Node != meter.Node {
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

// The meter ID names one source; the catalog holds its body.
func matchesOperatingMeter(meter OperatingPowerMeter, event *SchedulerObservation) bool {
	return event.Node != nil && *event.Node == meter.Node && event.Meter == meter.Meter && event.PowerScope == meter.Scope
}

func mergeOperatingPower(node *OperatingNodePower, raw *operatingPowerHistory, event *SchedulerObservation, from, to time.Time) {
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

// Records outside the interval can supply the nearest real boundary samples.
func retainOperatingNeighbors(raw *operatingPowerHistory, event *SchedulerObservation, from, to time.Time, limit int) {
	for _, sample := range event.Power.Samples {
		if policy := event.PowerCoverage; policy != nil &&
			(sample.ObservedAt.Before(from) && from.Sub(sample.ObservedAt) > policy.MaximumBoundaryDistance ||
				sample.ObservedAt.After(to) && sample.ObservedAt.Sub(to) > policy.MaximumBoundaryDistance) {
			continue // Outside samples must satisfy the recorded boundary policy.
		}
		if sample.ObservedAt.Before(from) {
			retainOperatingNeighbor(&raw.before, event, sample.ObservedAt, true, limit)
		}
		if sample.ObservedAt.After(to) {
			retainOperatingNeighbor(&raw.after, event, sample.ObservedAt, false, limit)
		}
	}
}

func retainOperatingNeighbor(neighbor *operatingPowerNeighbor, event *SchedulerObservation, at time.Time, before bool, limit int) {
	if neighbor.at.IsZero() || before && at.After(neighbor.at) || !before && at.Before(neighbor.at) {
		neighbor.at = at
		neighbor.records = neighbor.records[:0]
	}
	if len(neighbor.records) > 0 && neighbor.records[len(neighbor.records)-1] == event {
		return
	}
	if at.Equal(neighbor.at) && len(neighbor.records) <= limit {
		neighbor.records = append(neighbor.records, event)
	}
}

func mergeOperatingNeighbors(result *OperatingPowerWindow, history []operatingPowerHistory, matched, limit int) int {
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
			for _, event := range neighbor.records {
				power := event.Power
				if !power.StartedAt.After(result.To) && !power.EndedAt.Before(result.From) {
					continue // Already selected by the interval scan.
				}
				if seen[event] {
					continue
				}
				seen[event] = true
				if matched < limit {
					mergeOperatingPower(node, raw, event, result.From, result.To)
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

func operatingMembership(result *OperatingPowerWindow, baseline *SchedulerSnapshot, snapshots []SchedulerSnapshot, byUID map[string]int, rawComplete bool) ([]SchedulerSnapshot, bool) {
	activeValid := baseline != nil && rawComplete
	if baseline == nil {
		result.Reasons = append(result.Reasons, "active membership has no retained snapshot at interval start")
	} else {
		snapshots = append(snapshots, *baseline)
	}
	sort.SliceStable(snapshots, func(a, b int) bool { return snapshots[a].At.Before(snapshots[b].At) })
	for _, snapshot := range snapshots {
		for _, instance := range snapshot.Instances {
			if instance.WorkloadUID != "" || instance.OwnerWorkloadUID != "" {
				if _, found := byUID[instance.Node.UID]; !found {
					activeValid = false
					result.Reasons = append(result.Reasons, "active Node has no configured whole-node meter: "+instance.Node.UID)
				}
			}
			if instance.PodUID != "" && instance.WorkloadUID == "" && instance.OwnerWorkloadUID == "" {
				activeValid = false
				result.Reasons = appendUnique(result.Reasons, "active physical ownership unknown: "+instance.PodUID)
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
			if instance.Node.UID == uid && (instance.WorkloadUID != "" || instance.OwnerWorkloadUID != "") {
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
