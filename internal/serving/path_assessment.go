package serving

import (
	"context"
	"sort"
	"time"

	"gonum.org/v1/gonum/stat"
)

// PathAssessment keeps a selected candidate's replay separate from observations.
// It is an offline diagnostic, never an admission or profile-reuse decision.
type PathAssessment struct {
	At              time.Time             `json:"at"`
	From            time.Time             `json:"from"`
	To              time.Time             `json:"to"`
	Seed            uint64                `json:"seed"`
	CandidateID     string                `json:"candidateID"`
	Keep            bool                  `json:"keep"`
	Selected        []PathPlacement       `json:"selected"`
	PredictedStatus string                `json:"predictedStatus"`
	PredictedSLO    []SchedulerEstimate    `json:"predictedSLO"`
	ReplayReasons   []string              `json:"replayReasons,omitempty"`
	ReplayService   *ConditionalServiceEvidence `json:"replayService,omitempty"`
	SelectedBudgets []PathBudget          `json:"selectedBudgets"`
	Requests        int                   `json:"requests"`
	OutsideCohort   int                   `json:"outsideCohort"`
	Unmatched       map[string]int        `json:"unmatched"`
	Observed        []PathObserved        `json:"observed"`
	Limitations     []string              `json:"limitations"`
}

// PathPlacement retains selected configuration references, never profile bodies.
type PathPlacement struct {
	Instance string `json:"instance"`
	WorkloadUID string `json:"workloadUID"`
	Node NodeReference `json:"node"`
	ProfileID string `json:"profileID"`
	Weight int32 `json:"weight"`
	MaxBatch int `json:"maxBatch"`
	MaxWait time.Duration `json:"maxWaitNanos"`
}

// PathObserved is request-weighted except Physical, which counts each physical
// (instance,batchID) once. Unknown values stay nil, including unknown idle G.
type PathObserved struct {
	WorkloadUID       string         `json:"workloadUID"`
	DefinitionID      string         `json:"definitionID"`
	ProfileID         string         `json:"profileID"`
	Node              NodeReference  `json:"node"`
	DefinitionMatched bool           `json:"definitionMatched"`
	ProfileMatched    bool           `json:"profileMatched"`
	Requests          int            `json:"requests"`
	Completed         int            `json:"completed"`
	Failed            int            `json:"failed"`
	Incomplete        int            `json:"incomplete"`
	Outcomes          map[string]int `json:"outcomes"`
	SelectedPlacement int            `json:"selectedPlacement"`
	DifferentPlacement int           `json:"differentPlacement"`
	SLOPercentile     float64        `json:"sloPercentile,omitempty"`
	LatencySamples    int            `json:"latencySamples"`
	LatencyMS         *float64       `json:"latencyMS"`
	SLOExceedances    int            `json:"sloExceedances"`
	SLOLowerBoundExceedances int     `json:"sloLowerBoundExceedances"`
	SLOUnknown        int            `json:"sloUnknown"`
	PathPercentile    float64        `json:"pathPercentile,omitempty"`
	PathSamples       int            `json:"pathSamples"`
	TotalMS           *float64       `json:"totalMS"`
	PostMS            *float64       `json:"postMS"`
	MissingPath       map[string]int `json:"missingPath"`
	FrozenPostSamples int            `json:"frozenPostSamples"`
	MissingFrozenPost int            `json:"missingFrozenPost"`
	FrozenPostExceedances int         `json:"frozenPostExceedances"`
	MaximumFrozenPostExcessMS *float64 `json:"maximumFrozenPostExcessMS"`
	Physical          PathPhysical   `json:"physical"`
}

type PathPhysical struct {
	Batches           int            `json:"batches"`
	ServiceSamples    int            `json:"serviceSamples"`
	IdleSamples       int            `json:"idleSamples"`
	StartupOrUnknownIdle int         `json:"startupOrUnknownIdle"`
	ServiceMS         *float64       `json:"serviceMS"`
	IdleMS            *float64       `json:"idleMS"`
	ComparedBatches   int            `json:"comparedBatches"`
	// This mean summarizes per-batch conditional percentile queries at actual G;
	// it is neither a worker mean nor a candidate end-to-end percentile.
	MeanConditionalPercentileMS *float64 `json:"meanConditionalPercentileMS"`
	ConditionalExceedances int       `json:"conditionalExceedances"`
	Support           ConditionalServiceEvidence `json:"support"`
	Missing           map[string]int `json:"missing"`
}

type assessmentKey struct {
	uid, definition, profile string
	node NodeReference
}
type assessmentSamples struct {
	observed PathObserved
	definition *Definition
	profile *Profile
	latency, total, post, service, idle []float64
	conditionalSum float64
}

// AssessPath replays the chosen candidate with the supplied seed using the same
// replay as capacity and scheduler diagnostics. rows must contain one settled or
// open HTTP row per request plus optional native supplements. Observations use
// the received cohort [ObservationEnd-ObservationWindow,ObservationEnd), as of At.
// Native supplements contribute physical facts only; they never turn an HTTP
// failure or incomplete request into a successful path sample.
func AssessPath(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, candidate SchedulerCandidate, seed uint64, rows []*RequestRow) (PathAssessment, error) {
	// Planner estimates and prior replay output are not inputs to this replay.
	candidate.SLO, candidate.Reasons, candidate.Assumptions, candidate.Sources = nil, nil, nil, nil
	candidate.ExpectedJoules, candidate.ConditionalService, candidate.WorstExceedance = nil, nil, 0
	replayed, err := schedulerReplayEstimate(ctx, settings, snapshot, candidate, seed, nil, nil)
	if err != nil {
		return PathAssessment{}, err
	}
	report := PathAssessment{
		At: snapshot.At, From: snapshot.ObservationEnd.Add(-settings.ObservationWindow), To: snapshot.ObservationEnd,
		Seed: seed, CandidateID: candidate.ID, Keep: candidate.Keep,
		PredictedStatus: replayed.Status, PredictedSLO: replayed.SLO, ReplayReasons: replayed.Reasons, ReplayService: replayed.ConditionalService,
		Unmatched: map[string]int{}, Observed: []PathObserved{}, Selected: []PathPlacement{}, SelectedBudgets: []PathBudget{},
		Limitations: []string{
			"Replay repeats historical compute arrivals over the comparison period and includes outstanding requests; observed counts describe a received cohort, not the same population.",
			"Different placement observations cannot validate the selected target. Matching references do not prove historical routing, batch policy, representative coverage or independent trials.",
			"Path percentiles are request-weighted completed samples; physical service/G are batch-weighted. Missing, failed and incomplete requests remain separate.",
			"Only acceptance-frozen completionMarginMS is compared with actual POST; selected budgets describe this replay, not old requests.",
			"Configured path margins can double-charge pre-freeze costs. This report does not classify causes, reject profiles or certify SLO compliance.",
			"Input must be a cutoff-consistent raw export. File presence alone supplies no archive coverage or physical-supplement receipt-time guarantee.",
		},
	}
	definitions := map[string]*Definition{}
	for _, w := range snapshot.Workloads {
		if w != nil {
			definitions[w.Identity()] = w
			report.SelectedBudgets = append(report.SelectedBudgets, ResolvePathBudget(settings, snapshot.PathBudgets, w))
		}
	}
	type profileKey struct { node NodeReference; id string }
	profiles := map[profileKey]*Profile{}
	addProfile := func(i SchedulerInstance) {
		if i.Profile != nil {
			profiles[profileKey{i.Node, i.Profile.ID}] = i.Profile
		}
	}
	for _, i := range snapshot.Instances { addProfile(i) }
	for _, o := range snapshot.Options { addProfile(o.Instance) }
	selected := map[string]SchedulerInstance{}
	for _, i := range candidate.Instances {
		addProfile(i)
		selected[i.ID] = i
		id := i.ProfileID
		if i.Profile != nil { id = i.Profile.ID }
		weight := i.Weight
		if route, explicit := candidate.Routing[i.WorkloadUID]; explicit { weight = route[i.ID] }
		report.Selected = append(report.Selected, PathPlacement{Instance: i.ID, WorkloadUID: i.WorkloadUID, Node: i.Node, ProfileID: id, Weight: weight, MaxBatch: i.MaxBatch, MaxWait: i.MaxWait})
	}
	groups := map[assessmentKey]*assessmentSamples{}
	groupFor := func(row *RequestRow) *assessmentSamples {
		key := assessmentKey{uid: row.WorkloadUID, definition: row.DefinitionID, profile: row.ProfileID}
		if row.Node != nil { key.node = *row.Node }
		g := groups[key]
		if g == nil {
			d := definitions[key.definition]
			if d != nil && d.UID != key.uid { d = nil }
			g = &assessmentSamples{definition: d, profile: profiles[profileKey{key.node, key.profile}], observed: PathObserved{
				WorkloadUID: key.uid, DefinitionID: key.definition, ProfileID: key.profile, Node: key.node,
				DefinitionMatched: d != nil, Outcomes: map[string]int{}, MissingPath: map[string]int{}, Physical: PathPhysical{Missing: map[string]int{}},
			}}
			if d != nil && !MatchesProfile(d, g.profile) { g.profile = nil }
			g.observed.ProfileMatched = g.profile != nil
			if d != nil {
				g.observed.SLOPercentile = d.SLO.Percentile
				g.observed.PathPercentile = ResolvePathBudget(settings, snapshot.PathBudgets, d).JudgmentPercentile
			}
			groups[key] = g
		}
		return g
	}
	cohort := map[string]*RequestRow{}
	for _, row := range rows {
		if err := ctx.Err(); err != nil { return PathAssessment{}, err }
		if row == nil || row.Kind == RequestRowNative { continue }
		if !ObservationInInterval(row.ReceivedAt, report.From, report.To) { report.OutsideCohort++; continue }
		cohort[row.RequestID] = row
		report.Requests++
		g := groupFor(row)
		o := &g.observed
		o.Requests++
		if !o.DefinitionMatched { report.Unmatched["definition"]++ }
		if !o.ProfileMatched { report.Unmatched["profile"]++ }
		i, exists := selected[row.Instance]
		profileID := i.ProfileID
		if i.Profile != nil { profileID = i.Profile.ID }
		weight := i.Weight
		if route, explicit := candidate.Routing[row.WorkloadUID]; explicit { weight = route[i.ID] }
		if o.DefinitionMatched && o.ProfileMatched && exists && row.Node != nil && i.Node == *row.Node && profileID == row.ProfileID && i.WorkloadUID == row.WorkloadUID && weight > 0 {
			o.SelectedPlacement++
		} else { o.DifferentPlacement++; report.Unmatched["selected_placement"]++ }
		assessmentObserveRequest(g, row, snapshot.At)
	}
	// A request may repeat the same canonical physical exchange in final and
	// native rows. Select direct completed evidence once, independently of HTTP.
	type batchKey struct { instance string; id uint64 }
	type physicalBatch struct { row *RequestRow; group *assessmentSamples }
	batches := map[batchKey]physicalBatch{}
	batchKeys := []batchKey{}
	for _, row := range rows {
		if row == nil { continue }
		if cohort[row.RequestID] == nil {
			if row.Kind == RequestRowNative { report.Unmatched["native_request"]++ }
			continue
		}
		if row.Instance == "" || row.BatchID == 0 {
			if row.Kind != RequestRowNative { groupFor(row).observed.Physical.Missing["batch_identity"]++ }
			continue
		}
		key := batchKey{row.Instance, row.BatchID}
		previous, exists := batches[key]
		if !exists { batchKeys = append(batchKeys, key) }
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
	for _, g := range groups {
		o := &g.observed
		o.LatencyMS = assessmentQuantile(g.latency, o.SLOPercentile)
		o.TotalMS, o.PostMS = assessmentQuantile(g.total, o.PathPercentile), assessmentQuantile(g.post, o.PathPercentile)
		o.Physical.ServiceMS, o.Physical.IdleMS = assessmentQuantile(g.service, o.SLOPercentile), assessmentQuantile(g.idle, o.SLOPercentile)
		if o.Physical.ComparedBatches > 0 { v := g.conditionalSum / float64(o.Physical.ComparedBatches); o.Physical.MeanConditionalPercentileMS = &v }
		report.Observed = append(report.Observed, *o)
	}
	sort.Slice(report.Observed, func(i, j int) bool {
		a, b := report.Observed[i], report.Observed[j]
		if a.WorkloadUID != b.WorkloadUID { return a.WorkloadUID < b.WorkloadUID }
		if a.DefinitionID != b.DefinitionID { return a.DefinitionID < b.DefinitionID }
		if a.ProfileID != b.ProfileID { return a.ProfileID < b.ProfileID }
		if a.Node.UID != b.Node.UID { return a.Node.UID < b.Node.UID }
		return a.Node.Name < b.Node.Name
	})
	return report, ctx.Err()
}

func assessmentObserveRequest(g *assessmentSamples, row *RequestRow, at time.Time) {
	o := &g.observed
	settled := row.ElapsedNanos != nil && *row.ElapsedNanos >= 0 && !row.ReceivedAt.Add(time.Duration(*row.ElapsedNanos)).After(at)
	outcome := row.Outcome
	if !settled || outcome == "" { outcome = "unknown" }
	o.Outcomes[outcome]++
	switch {
	case !settled || outcome == "unknown" || outcome == outcomeIncomplete:
		o.Incomplete++
	case outcome != outcomeCompleted || row.Error != "":
		o.Failed++
	case !row.ExecutionComplete:
		o.Incomplete++
	default:
		o.Completed++
		if g.definition != nil { g.latency = append(g.latency, float64(*row.ElapsedNanos)/1e6); o.LatencySamples++ }
	}
	if g.definition != nil {
		// Settled HTTP durations and the observed age of open requests are lower
		// bounds even when completion/native timings are unavailable.
		lower := at.Sub(row.ReceivedAt)
		if settled { lower = time.Duration(*row.ElapsedNanos) }
		if lower > g.definition.SLO.TargetLatency {
			o.SLOExceedances++
			if !settled { o.SLOLowerBoundExceedances++ }
		} else if !settled { o.SLOUnknown++ }
	} else { o.SLOUnknown++ }
	if !settled { o.MissingPath["incomplete"]++; return }
	path, reason := RequestPathSample(row)
	if reason != "" { o.MissingPath[reason]++; return }
	o.PathSamples++
	g.total, g.post = append(g.total, float64(path.TotalNanos)/1e6), append(g.post, float64(path.PostNanos)/1e6)
	if row.CompletionMarginMS == nil || !schedulerFinite(*row.CompletionMarginMS) || *row.CompletionMarginMS < 0 { o.MissingFrozenPost++; return }
	o.FrozenPostSamples++
	excess := float64(path.PostNanos)/1e6 - *row.CompletionMarginMS
	if excess > 0 {
		o.FrozenPostExceedances++
		if o.MaximumFrozenPostExcessMS == nil || excess > *o.MaximumFrozenPostExcessMS { o.MaximumFrozenPostExcessMS = &excess }
	}
}

func assessmentPhysicalRank(row *RequestRow) int {
	rank := 0
	if row.ServiceDurationNanos > 0 { rank += 2 }
	if row.NativeExecutionComplete && row.NativeOutcome == outcomeCompleted && row.NativeError == "" { rank += 4 }
	if row.PrecedingIdleNanos != nil { rank++ }
	return rank
}

func assessmentObservePhysical(g *assessmentSamples, row *RequestRow) {
	p := &g.observed.Physical
	p.Batches++
	if !row.NativeExecutionComplete || row.NativeOutcome != outcomeCompleted || row.NativeError != "" { p.Missing["native_incomplete_or_failed"]++; return }
	if row.ServiceDurationNanos <= 0 { p.Missing["service"]++; return }
	p.ServiceSamples++
	g.service = append(g.service, float64(row.ServiceDurationNanos)/1e6)
	if row.PrecedingIdleNanos == nil { p.StartupOrUnknownIdle++ } else if *row.PrecedingIdleNanos >= 0 { p.IdleSamples++; g.idle = append(g.idle, float64(*row.PrecedingIdleNanos)/1e6) } else { p.Missing["idle_inverted"]++; return }
	if g.definition == nil { p.Missing["definition"]++; return }
	if g.profile == nil { p.Missing["profile"]++; return }
	for _, b := range g.profile.Batches {
		if b.Batch != row.BatchSize { continue }
		if b.ServiceDistribution == nil { p.Missing["distribution"]++; return }
		service, support, ok := b.ServiceDistribution.QuantileNanos(row.PrecedingIdleNanos, g.definition.SLO.Percentile)
		p.Support.record(row.PrecedingIdleNanos, support)
		if !ok { p.Missing["conditional_support"]++; return }
		p.ComparedBatches++
		g.conditionalSum += float64(service)/1e6
		if row.ServiceDurationNanos > service { p.ConditionalExceedances++ }
		return
	}
	p.Missing["batch_profile"]++
}

func assessmentQuantile(values []float64, percentile float64) *float64 {
	if len(values) == 0 || !schedulerFinite(percentile) || percentile <= 0 || percentile > 100 { return nil }
	sort.Float64s(values)
	q := stat.Quantile(percentile/100, stat.Empirical, values, nil)
	return &q
}
