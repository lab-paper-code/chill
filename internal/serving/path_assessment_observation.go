package serving

import (
	"context"
	"sort"
	"time"

	"gonum.org/v1/gonum/stat"
)

const outcomeUnknown = "unknown"

type assessmentKey struct {
	uid, definition, profile string
	node                     NodeReference
}
type assessmentSamples struct {
	observed                            PathObserved
	definition                          *Definition
	profile                             *Profile
	latency, total, post, service, idle []float64
	conditionalSum                      float64
}

// assessmentGroups resolves existing catalog references once and retains only
// the samples needed to project each observed configuration.
func assessmentGroups(settings SchedulerSettings, snapshot SchedulerSnapshot, candidate SchedulerCandidate, groups map[assessmentKey]*assessmentSamples) func(*RequestRow) *assessmentSamples {
	definitions := map[string]*Definition{}
	for _, w := range snapshot.Workloads {
		if w != nil {
			definitions[w.Identity()] = w
		}
	}
	type profileKey struct {
		node NodeReference
		id   string
	}
	profiles := map[profileKey]*Profile{}
	addProfile := func(i SchedulerInstance) {
		if i.Profile != nil {
			profiles[profileKey{i.Node, i.Profile.ID}] = i.Profile
		}
	}
	for _, i := range snapshot.Instances {
		addProfile(i)
	}
	for _, o := range snapshot.Options {
		addProfile(o.Instance)
	}
	for _, i := range candidate.Instances {
		addProfile(i)
	}
	return func(row *RequestRow) *assessmentSamples {
		key := assessmentKey{uid: row.WorkloadUID, definition: row.DefinitionID, profile: row.ProfileID}
		if row.Node != nil {
			key.node = *row.Node
		}
		g := groups[key]
		if g == nil {
			d := definitions[key.definition]
			if d != nil && d.UID != key.uid {
				d = nil
			}
			g = &assessmentSamples{definition: d, profile: profiles[profileKey{key.node, key.profile}], observed: PathObserved{
				WorkloadUID: key.uid, DefinitionID: key.definition, ProfileID: key.profile, Node: key.node,
				DefinitionMatched: d != nil, Outcomes: map[string]int{}, MissingPath: map[string]int{}, Physical: PathPhysical{Missing: map[string]int{}},
			}}
			if d != nil && !MatchesProfile(d, g.profile) {
				g.profile = nil
			}
			g.observed.ProfileMatched = g.profile != nil
			if d != nil {
				g.observed.SLOPercentile = d.SLO.Percentile
				g.observed.PathPercentile = ResolvePathBudget(settings, snapshot.PathBudgets, d).JudgmentPercentile
			}
			groups[key] = g
		}
		return g
	}
}

func assessmentObserveCohort(ctx context.Context, report *PathAssessment, candidate SchedulerCandidate, rows []*RequestRow, groupFor func(*RequestRow) *assessmentSamples) (map[string]*RequestRow, error) {
	selected := map[string]SchedulerInstance{}
	for _, i := range candidate.Instances {
		selected[i.ID] = i
	}
	cohort := map[string]*RequestRow{}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if row == nil || row.Kind == RequestRowNative {
			continue
		}
		if !ObservationInInterval(row.ReceivedAt, report.From, report.To) {
			report.OutsideCohort++
			continue
		}
		cohort[row.RequestID] = row
		report.Requests++
		g := groupFor(row)
		o := &g.observed
		o.Requests++
		if !o.DefinitionMatched {
			report.Unmatched["definition"]++
		}
		if !o.ProfileMatched {
			report.Unmatched["profile"]++
		}
		i, exists := selected[row.Instance]
		profileID := i.ProfileID
		if i.Profile != nil {
			profileID = i.Profile.ID
		}
		weight := i.Weight
		if route, explicit := candidate.Routing[row.WorkloadUID]; explicit {
			weight = route[i.ID]
		}
		if o.DefinitionMatched && o.ProfileMatched && exists && row.Node != nil && i.Node == *row.Node && profileID == row.ProfileID && i.WorkloadUID == row.WorkloadUID && weight > 0 {
			o.SelectedPlacement++
		} else {
			o.DifferentPlacement++
			report.Unmatched["selected_placement"]++
		}
		assessmentObserveRequest(g, row, report.At)
	}
	return cohort, nil
}

func assessmentObserveBatches(report *PathAssessment, rows []*RequestRow, cohort map[string]*RequestRow, groupFor func(*RequestRow) *assessmentSamples) {
	// A request may repeat the same canonical physical exchange in final and
	// native rows. Select direct completed evidence once, independently of HTTP.
	type batchKey struct {
		instance string
		id       uint64
	}
	type physicalBatch struct {
		row   *RequestRow
		group *assessmentSamples
	}
	batches := map[batchKey]physicalBatch{}
	batchKeys := []batchKey{}
	for _, row := range rows {
		if row == nil {
			continue
		}
		if cohort[row.RequestID] == nil {
			if row.Kind == RequestRowNative {
				report.Unmatched["native_request"]++
			}
			continue
		}
		if row.Instance == "" || row.BatchID == 0 {
			if row.Kind != RequestRowNative {
				groupFor(row).observed.Physical.Missing["batch_identity"]++
			}
			continue
		}
		key := batchKey{row.Instance, row.BatchID}
		previous, exists := batches[key]
		if !exists {
			batchKeys = append(batchKeys, key)
		}
		if !exists || assessmentPhysicalRank(row) > assessmentPhysicalRank(previous.row) {
			contextRow := row
			base := cohort[row.RequestID]
			// Raw native supplements omit placement references. Only the same
			// request and physical instance can supply the HTTP assignment.
			if row.Kind == RequestRowNative && row.Node == nil && row.ProfileID == "" && row.Instance == base.Instance && row.WorkloadUID == base.WorkloadUID && row.DefinitionID == base.DefinitionID {
				contextRow = base
			}
			batches[key] = physicalBatch{row: row, group: groupFor(contextRow)}
		}
	}
	for _, key := range batchKeys {
		batch := batches[key]
		assessmentObservePhysical(batch.group, batch.row)
	}
}

func assessmentProject(groups map[assessmentKey]*assessmentSamples) []PathObserved {
	observed := make([]PathObserved, 0, len(groups))
	for _, g := range groups {
		o := &g.observed
		o.LatencyMS = assessmentQuantile(g.latency, o.SLOPercentile)
		o.TotalMS, o.PostMS = assessmentQuantile(g.total, o.PathPercentile), assessmentQuantile(g.post, o.PathPercentile)
		o.Physical.ServiceMS, o.Physical.IdleMS = assessmentQuantile(g.service, o.SLOPercentile), assessmentQuantile(g.idle, o.SLOPercentile)
		if o.Physical.ComparedBatches > 0 {
			v := g.conditionalSum / float64(o.Physical.ComparedBatches)
			o.Physical.MeanConditionalPercentileMS = &v
		}
		observed = append(observed, *o)
	}
	sort.Slice(observed, func(i, j int) bool {
		a, b := observed[i], observed[j]
		if a.WorkloadUID != b.WorkloadUID {
			return a.WorkloadUID < b.WorkloadUID
		}
		if a.DefinitionID != b.DefinitionID {
			return a.DefinitionID < b.DefinitionID
		}
		if a.ProfileID != b.ProfileID {
			return a.ProfileID < b.ProfileID
		}
		if a.Node.UID != b.Node.UID {
			return a.Node.UID < b.Node.UID
		}
		return a.Node.Name < b.Node.Name
	})
	return observed
}

func assessmentObserveRequest(g *assessmentSamples, row *RequestRow, at time.Time) {
	o := &g.observed
	settled := row.ElapsedNanos != nil && *row.ElapsedNanos >= 0 && !row.ReceivedAt.Add(time.Duration(*row.ElapsedNanos)).After(at)
	outcome := row.Outcome
	if !settled || outcome == "" {
		outcome = outcomeUnknown
	}
	o.Outcomes[outcome]++
	switch {
	case !settled || outcome == outcomeUnknown || outcome == outcomeIncomplete:
		o.Incomplete++
	case outcome != outcomeCompleted || row.Error != "":
		o.Failed++
	case !row.ExecutionComplete:
		o.Incomplete++
	default:
		o.Completed++
		if g.definition != nil {
			g.latency = append(g.latency, float64(*row.ElapsedNanos)/1e6)
			o.LatencySamples++
		}
	}
	if g.definition != nil {
		// Settled HTTP durations and the observed age of open requests are lower
		// bounds even when completion/native timings are unavailable.
		lower := at.Sub(row.ReceivedAt)
		if settled {
			lower = time.Duration(*row.ElapsedNanos)
		}
		if lower > g.definition.SLO.TargetLatency {
			o.SLOExceedances++
			if !settled {
				o.SLOLowerBoundExceedances++
			}
		} else if !settled {
			o.SLOUnknown++
		}
	} else {
		o.SLOUnknown++
	}
	if !settled {
		o.MissingPath["incomplete"]++
		return
	}
	path, reason := RequestPathSample(row)
	if reason != "" {
		o.MissingPath[reason]++
		return
	}
	o.PathSamples++
	g.total, g.post = append(g.total, float64(path.TotalNanos)/1e6), append(g.post, float64(path.PostNanos)/1e6)
	if row.CompletionMarginMS == nil || !schedulerFinite(*row.CompletionMarginMS) || *row.CompletionMarginMS < 0 {
		o.MissingFrozenPost++
		return
	}
	o.FrozenPostSamples++
	excess := float64(path.PostNanos)/1e6 - *row.CompletionMarginMS
	if excess > 0 {
		o.FrozenPostExceedances++
		if o.MaximumFrozenPostExcessMS == nil || excess > *o.MaximumFrozenPostExcessMS {
			o.MaximumFrozenPostExcessMS = &excess
		}
	}
}

func assessmentPhysicalRank(row *RequestRow) int {
	rank := 0
	if row.ServiceDurationNanos > 0 {
		rank += 2
	}
	if row.NativeExecutionComplete && row.NativeOutcome == outcomeCompleted && row.NativeError == "" {
		rank += 4
	}
	if row.PrecedingIdleNanos != nil {
		rank++
	}
	return rank
}

func assessmentObservePhysical(g *assessmentSamples, row *RequestRow) {
	p := &g.observed.Physical
	p.Batches++
	if !row.NativeExecutionComplete || row.NativeOutcome != outcomeCompleted || row.NativeError != "" {
		p.Missing["native_incomplete_or_failed"]++
		return
	}
	if row.ServiceDurationNanos <= 0 {
		p.Missing["service"]++
		return
	}
	p.ServiceSamples++
	g.service = append(g.service, float64(row.ServiceDurationNanos)/1e6)
	if row.PrecedingIdleNanos == nil {
		p.StartupOrUnknownIdle++
	} else if *row.PrecedingIdleNanos >= 0 {
		p.IdleSamples++
		g.idle = append(g.idle, float64(*row.PrecedingIdleNanos)/1e6)
	} else {
		p.Missing["idle_inverted"]++
		return
	}
	if g.definition == nil {
		p.Missing["definition"]++
		return
	}
	if g.profile == nil {
		p.Missing["profile"]++
		return
	}
	for _, b := range g.profile.Batches {
		if b.Batch != row.BatchSize {
			continue
		}
		if b.ServiceDistribution == nil {
			p.Missing["distribution"]++
			return
		}
		service, support, ok := b.ServiceDistribution.QuantileNanos(row.PrecedingIdleNanos, g.definition.SLO.Percentile)
		p.Support.record(row.PrecedingIdleNanos, support)
		if !ok {
			p.Missing["conditional_support"]++
			return
		}
		p.ComparedBatches++
		g.conditionalSum += float64(service) / 1e6
		if row.ServiceDurationNanos > service {
			p.ConditionalExceedances++
		}
		return
	}
	p.Missing["batch_profile"]++
}

func assessmentQuantile(values []float64, percentile float64) *float64 {
	if len(values) == 0 || !schedulerFinite(percentile) || percentile <= 0 || percentile > 100 {
		return nil
	}
	sort.Float64s(values)
	q := stat.Quantile(percentile/100, stat.Empirical, values, nil)
	return &q
}
