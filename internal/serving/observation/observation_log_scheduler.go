package observation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	serving "github.com/lab-paper-code/chill/internal/serving"
)

// SchedulerSettingsChange is one settings PUT; its validatedAt identifies it.
// The staticRouting pair is the ConfigMap declaration around the change, and
// Reason is the operator's selection rationale for a staticRouting change.
type SchedulerSettingsChange struct {
	ActorUsername       string                    `json:"actorUsername"`
	ActorUID            string                    `json:"actorUID"`
	Before              serving.SchedulerSettings `json:"before"`
	After               serving.SchedulerSettings `json:"after"`
	BeforeStaticRouting map[string]map[string]int `json:"beforeStaticRouting,omitempty"`
	AfterStaticRouting  map[string]map[string]int `json:"afterStaticRouting,omitempty"`
	Reason              string                    `json:"reason,omitempty"`
	ValidatedAt         time.Time                 `json:"validatedAt"`
	AppliedAt           *time.Time                `json:"appliedAt,omitempty"`
	Affected            []serving.NodeReference   `json:"affected"`
	Error               string                    `json:"error,omitempty"`
}

type SchedulerActionObservation struct {
	ID              string                         `json:"id"`
	Kind            string                         `json:"kind"`
	Node            serving.NodeReference          `json:"node"`
	Instance        string                         `json:"instance,omitempty"`
	Phase           string                         `json:"phase"`
	StartedAt       time.Time                      `json:"startedAt"`
	CompletedAt     *time.Time                     `json:"completedAt,omitempty"`
	Expected        json.RawMessage                `json:"expected,omitempty"`
	Actual          json.RawMessage                `json:"actual,omitempty"`
	Error           string                         `json:"error,omitempty"`
	Evidence        []evidencestore.ObjectIdentity `json:"evidence,omitempty"`
	ExpectedSeconds *float64                       `json:"expectedSeconds"`
	ExpectedSource  *serving.ActionExpectedSource  `json:"expectedSource"`
}

// SchedulerControlObservation is a bounded call-completion record. Elapsed ends
// before this record is submitted: it does not claim async persistence or
// physical completion. TickAt joins round/tick calls; DecisionAt identifies the
// captured decision. Only tick records own evaluation disposition.
type SchedulerControlObservation struct {
	Scope       string                            `json:"scope"`
	TickAt      time.Time                         `json:"tickAt"`
	DecisionAt  time.Time                         `json:"decisionAt"`
	StartedAt   time.Time                         `json:"startedAt"`
	FinishedAt  time.Time                         `json:"finishedAt"`
	Elapsed     time.Duration                     `json:"elapsedNanos"`
	Phase       string                            `json:"phase"`
	Status      string                            `json:"status,omitempty"`
	TargetID    string                            `json:"targetID,omitempty"`
	Fallback    string                            `json:"fallback,omitempty"`
	Error       string                            `json:"error,omitempty"`
	AdmitError  string                            `json:"admitError,omitempty"`
	LockWait    time.Duration                     `json:"lockWaitNanos"`
	Admit       time.Duration                     `json:"admitNanos"`
	Tick        time.Duration                     `json:"tickNanos"`
	Capture     time.Duration                     `json:"captureNanos"`
	Window      time.Duration                     `json:"windowNanos"`
	RefreshList time.Duration                     `json:"refreshListNanos"`
	SourceProbe time.Duration                     `json:"sourceProbeNanos"`
	Rest        time.Duration                     `json:"restNanos"`
	Evaluate    time.Duration                     `json:"evaluateNanos"`
	Publish     time.Duration                     `json:"publishNanos"`
	Record      time.Duration                     `json:"recordNanos"`
	Launch      time.Duration                     `json:"launchNanos"`
	Evaluation  serving.SchedulerEvaluationTiming `json:"evaluation"`

	RefreshNodes         time.Duration `json:"refreshNodesNanos"`
	RefreshPods          time.Duration `json:"refreshPodsNanos"`
	RefreshResources     time.Duration `json:"refreshResourcesNanos"`
	RefreshDeviceClasses time.Duration `json:"refreshDeviceClassesNanos"`
	RefreshServices      time.Duration `json:"refreshServicesNanos"`

	WindowBarrier time.Duration `json:"windowBarrierNanos"`
	WindowScan    time.Duration `json:"windowScanNanos"`
	WindowDecode  time.Duration `json:"windowDecodeNanos"`
	WindowJoin    time.Duration `json:"windowJoinNanos"`
	WindowSort    time.Duration `json:"windowSortNanos"`
	WindowCache   time.Duration `json:"windowCacheNanos"`
	// Kept at zero for the stored observations.v1alpha7 contract; retention uses time span only.
	WindowBudgetFallbacks int `json:"windowBudgetFallbacks"`
}

type SchedulerObservation struct {
	At            time.Time                    `json:"at"`
	Control       *SchedulerControlObservation `json:"control,omitempty"`
	Decision      *serving.SchedulerDecision   `json:"decision,omitempty"`
	Snapshot      *serving.SchedulerSnapshot   `json:"snapshot,omitempty"`
	Settings      *SchedulerSettingsChange     `json:"settings,omitempty"`
	Action        *SchedulerActionObservation  `json:"action,omitempty"`
	Power         *metricobserver.Result       `json:"power,omitempty"`
	Node          *serving.NodeReference       `json:"node,omitempty"`
	Meter         string                       `json:"meter,omitempty"`
	PowerScope    string                       `json:"powerScope,omitempty"`
	PowerSampling *metricobserver.Request      `json:"powerSampling,omitempty"`
	PowerCoverage *powerwindow.Policy          `json:"powerCoverage,omitempty"`
	PowerJoules   *float64                     `json:"powerJoules,omitempty"`
	PowerError    string                       `json:"powerError,omitempty"`
	// Offset is the original window's append offset.
	// schedulerLine deliberately does not persist this read-side reference.
	Offset *int `json:"offset,omitempty"`
	// References carries catalog bodies projected before action payload marshaling.
	References *SchedulerReferences `json:"-"`
}

// schedulerLine is the stored scheduler record. Immutable bodies live in the
// catalog: a power row names its meter, and a snapshot names definitions,
// costs and baselines by reference.
type schedulerLine struct {
	At          time.Time                    `json:"at"`
	Control     *SchedulerControlObservation `json:"control,omitempty"`
	Decision    *serving.SchedulerDecision   `json:"decision,omitempty"`
	Snapshot    *serving.SchedulerSnapshot   `json:"snapshot,omitempty"`
	Settings    *SchedulerSettingsChange     `json:"settings,omitempty"`
	Action      *SchedulerActionObservation  `json:"action,omitempty"`
	Node        *serving.NodeReference       `json:"node,omitempty"`
	Meter       string                       `json:"meter,omitempty"`
	StartedAt   *time.Time                   `json:"startedAt,omitempty"`
	Samples     []metricobserver.Sample      `json:"samples,omitempty"`
	Failures    []metricobserver.Failure     `json:"failures,omitempty"`
	PowerJoules *float64                     `json:"powerJoules,omitempty"`
	PowerError  string                       `json:"powerError,omitempty"`
}

// ObservationMeter is the catalog body of a meter.
type ObservationMeter struct {
	Node     serving.NodeReference         `json:"node"`
	Scope    string                        `json:"scope"`
	Source   metricobserver.SourceIdentity `json:"source"`
	Sampling *metricobserver.Request       `json:"sampling,omitempty"`
	Coverage *powerwindow.Policy           `json:"coverage,omitempty"`
}

func costRef(c serving.OperationCost) string    { return c.ID + "@" + c.Reference.Key }
func baselineRef(b serving.NodeBaseline) string { return b.ID + "@" + b.Reference.Key }
func (o *SchedulerReferences) add(kind, key string, body any) {
	o.rows = append(o.rows, observationReference{kind: kind, key: key, body: body})
}

type observationReference struct {
	kind, key string
	body      any
	encoded   []byte
}

// SchedulerReferences projects scheduler bodies into their stored catalog references.
type SchedulerReferences struct{ rows []observationReference }

func (l *ObservationLog) SchedulerRecord(event SchedulerObservation) error {
	if event.At.IsZero() {
		return &serving.Error{Code: "InvalidObservation", Message: "scheduler observation time is required"}
	}
	refs := event.References
	if refs == nil {
		refs = &SchedulerReferences{}
	}
	line := schedulerLine{At: event.At, Control: event.Control, Settings: event.Settings, Action: event.Action}
	if d := event.Decision; d != nil {
		recorded := *d
		recorded.Keep = refs.Candidate(d.Keep)
		if d.Target != nil && d.Target.ID == d.Keep.ID {
			recorded.Target = &serving.SchedulerCandidate{ID: d.Target.ID, Keep: true}
		} else if d.Target != nil {
			target := refs.Candidate(*d.Target)
			recorded.Target = &target
		}
		line.Decision = &recorded
	}
	if s := event.Snapshot; s != nil {
		recorded := *s
		if event.Decision != nil {
			recorded.DispatchOccupancy = serving.SchedulerSelectedOccupancy(event.Decision.Settings, *s)
		}
		recorded.Instances = refs.instances(s.Instances)
		recorded.Actions = refs.actions(s.Actions)
		// Window counts stay in Decision.Search; the request stream holds arrivals.
		recorded.Arrivals = nil
		recorded.Workloads, recorded.WorkloadIDs = nil, make([]string, len(s.Workloads))
		for i, d := range s.Workloads {
			recorded.WorkloadIDs[i] = d.Identity()
			refs.add(observationDefinitionCatalog, d.Identity(), d)
		}
		recorded.Costs, recorded.CostRefs = nil, make([]string, len(s.Costs))
		for i, c := range s.Costs {
			recorded.CostRefs[i] = costRef(c)
			refs.add("cost", costRef(c), c)
		}
		recorded.Baselines, recorded.BaselineRefs = nil, make([]string, len(s.Baselines))
		for i, b := range s.Baselines {
			recorded.BaselineRefs[i] = baselineRef(b)
			refs.add("baseline", baselineRef(b), b)
		}
		recorded.Outstanding = slices.Clone(s.Outstanding)
		for i, r := range recorded.Outstanding {
			if r.Definition != nil {
				refs.add(observationDefinitionCatalog, r.Definition.Identity(), r.Definition)
				recorded.Outstanding[i].DefinitionID, recorded.Outstanding[i].Definition = r.Definition.Identity(), nil
			}
		}
		recorded.Options = slices.Clone(s.Options)
		for i := range recorded.Options {
			recorded.Options[i].Instance = refs.Instance(recorded.Options[i].Instance)
		}
		recorded.Reservations = slices.Clone(s.Reservations)
		for i, reservation := range s.Reservations {
			if reservation.Target != nil {
				target := refs.Candidate(*reservation.Target)
				recorded.Reservations[i].Target = &target
			}
		}
		line.Snapshot = &recorded
	}
	if power := event.Power; power != nil {
		if event.Meter == "" || event.Node == nil {
			return &serving.Error{Code: "InvalidObservation", Message: "power observation requires its meter and Node"}
		}
		started := power.StartedAt
		line.Node, line.Meter, line.StartedAt = event.Node, event.Meter, &started
		line.Samples, line.Failures, line.PowerJoules, line.PowerError = power.Samples, power.Failures, event.PowerJoules, event.PowerError
		refs.add("meter", event.Meter, ObservationMeter{
			Node: *event.Node, Scope: event.PowerScope, Source: power.Source,
			Sampling: event.PowerSampling, Coverage: event.PowerCoverage,
		})
	}
	return l.submitRecord(observationSchedulerStream, line, refs.rows, event.At)
}

// Stored records keep profile IDs; the catalog holds profile bodies.
func (o *SchedulerReferences) Candidate(c serving.SchedulerCandidate) serving.SchedulerCandidate {
	c.Instances = o.instances(c.Instances)
	c.Actions = o.actions(c.Actions)
	return c
}

func (o *SchedulerReferences) actions(actions []serving.Action) []serving.Action {
	first := slices.IndexFunc(actions, func(a serving.Action) bool { return a.RouteTarget != nil })
	if first < 0 {
		return actions
	}
	recorded := slices.Clone(actions)
	for k := first; k < len(recorded); k++ {
		if target := recorded[k].RouteTarget; target != nil {
			stage := o.Candidate(*target)
			recorded[k].RouteTarget = &stage
		}
	}
	return recorded
}

func (o *SchedulerReferences) instances(instances []serving.SchedulerInstance) []serving.SchedulerInstance {
	recorded := slices.Clone(instances)
	for i := range recorded {
		recorded[i] = o.Instance(recorded[i])
	}
	return recorded
}

// Instance records the profile body in the catalog and retains its ID.
func (o *SchedulerReferences) Instance(instance serving.SchedulerInstance) serving.SchedulerInstance {
	if instance.Profile != nil {
		o.add(observationProfileCatalog, instance.Profile.ID, instance.Profile)
		instance.ProfileID, instance.Profile = instance.Profile.ID, nil
	}
	return instance
}

// ObservationCatalog resolves a reference to the last catalog row of its key
// at or before the referencing record; a record older than every row of its
// key uses the earliest row.
type ObservationCatalog struct {
	rows map[string][]CatalogRow
}

func (c *ObservationCatalog) Add(row CatalogRow) {
	if c.rows == nil {
		c.rows = map[string][]CatalogRow{}
	}
	id := row.Kind + "/" + row.Key
	c.rows[id] = append(c.rows[id], row)
}

func (c *ObservationCatalog) body(kind, key string, at time.Time, value any) error {
	rows := c.rows[kind+"/"+key]
	if len(rows) == 0 {
		return &serving.Error{Code: "IncompleteObservation", Message: fmt.Sprintf("catalog has no %s %s", kind, key)}
	}
	found := rows[0]
	for _, row := range rows {
		if row.At.Before(found.At) {
			found = row
		}
	}
	for _, row := range rows {
		if !row.At.After(at) && !row.At.Before(found.At) {
			found = row
		}
	}
	return json.Unmarshal(found.Body, value)
}

// DecodeSchedulerLine decodes one stored scheduler line and fills every
// reference from the catalog.
func DecodeSchedulerLine(raw []byte, catalog *ObservationCatalog) (SchedulerObservation, error) {
	var line schedulerLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return SchedulerObservation{}, err
	}
	event := SchedulerObservation{At: line.At, Control: line.Control, Decision: line.Decision, Snapshot: line.Snapshot, Settings: line.Settings, Action: line.Action}
	if d := event.Decision; d != nil && d.Target != nil && d.Target.ID == d.Keep.ID {
		target := d.Keep
		d.Target = &target
	}
	if s := event.Snapshot; s != nil {
		s.Workloads, s.Costs, s.Baselines = nil, nil, nil
		if len(s.WorkloadIDs) > 0 {
			s.Workloads = make([]*serving.Definition, len(s.WorkloadIDs))
		}
		for i, id := range s.WorkloadIDs {
			if err := catalog.body(observationDefinitionCatalog, id, line.At, &s.Workloads[i]); err != nil {
				return event, err
			}
		}
		if len(s.CostRefs) > 0 {
			s.Costs = make([]serving.OperationCost, len(s.CostRefs))
		}
		for i, id := range s.CostRefs {
			if err := catalog.body("cost", id, line.At, &s.Costs[i]); err != nil {
				return event, err
			}
		}
		if len(s.BaselineRefs) > 0 {
			s.Baselines = make([]serving.NodeBaseline, len(s.BaselineRefs))
		}
		for i, id := range s.BaselineRefs {
			if err := catalog.body("baseline", id, line.At, &s.Baselines[i]); err != nil {
				return event, err
			}
		}
		s.WorkloadIDs, s.CostRefs, s.BaselineRefs = nil, nil, nil
	}
	return event, resolvePower(&event, &line, catalog)
}

func resolvePower(event *SchedulerObservation, line *schedulerLine, catalog *ObservationCatalog) error {
	if line.Meter == "" {
		return nil
	}
	var meter ObservationMeter
	if err := catalog.body("meter", line.Meter, line.At, &meter); err != nil {
		return err
	}
	power := &metricobserver.Result{Source: meter.Source, EndedAt: line.At, Samples: line.Samples, Failures: line.Failures}
	if line.StartedAt != nil {
		power.StartedAt = *line.StartedAt
	}
	power.Summary = metricobserver.Summarize(*power)
	event.Power, event.Node, event.Meter, event.PowerScope = power, line.Node, line.Meter, meter.Scope
	event.PowerSampling, event.PowerCoverage = meter.Sampling, meter.Coverage
	event.PowerJoules, event.PowerError = line.PowerJoules, line.PowerError
	return nil
}

type WorkloadLoad struct {
	UID            string         `json:"uid"`
	Received       int            `json:"received"`
	Accepted       int            `json:"accepted"`
	Assigned       int            `json:"assigned"`
	Completed      int            `json:"completed"`
	ReceivedRate   float64        `json:"receivedInputsPerSecond"`
	AcceptedRate   float64        `json:"acceptedInputsPerSecond"`
	AssignedRate   float64        `json:"assignedInputsPerSecond"`
	CompletionRate float64        `json:"completionEventsPerSecond"`
	Instances      map[string]int `json:"instanceAssignments"`
}

type LoadWindow struct {
	From               time.Time                   `json:"from"`
	To                 time.Time                   `json:"to"`
	Duration           time.Duration               `json:"durationNanos"`
	Population         string                      `json:"population"`
	Workloads          []WorkloadLoad              `json:"workloads"`
	Arrivals           []serving.SchedulerArrival  `json:"arrivals"`
	GapUntil           *time.Time                  `json:"gapUntil,omitempty"`
	Coverage           serving.ObservationCoverage `json:"coverage"`
	PathSamples        []serving.PathTimingSample  `json:"pathSamples"`
	ClientObservations []serving.ClientObservation `json:"clientObservations"`
}

// LoadWindow counts each stage whose time, receipt plus stage offset, lies in
// [from,to), independently of result cohort filters.
func (l *ObservationLog) LoadWindow(ctx context.Context, from, to time.Time) (LoadWindow, error) {
	return l.LoadWindowTimed(ctx, from, to, nil)
}

// LoadWindowTimed records disjoint window phases in the control observation.
func (l *ObservationLog) LoadWindowTimed(ctx context.Context, from, to time.Time, timing *SchedulerControlObservation) (LoadWindow, error) {
	result := LoadWindow{From: from, To: to, Duration: to.Sub(from), Population: "all ingress receipts; computable demand separately marked after authorization and input validation; stage time [from,to); one request = one input"}
	if from.IsZero() || !from.Before(to) {
		return result, &serving.Error{Code: "InvalidQuery", Message: "positive explicit load window is required"}
	}
	// Both streams share one capture. Persisted first-report eligibility keeps
	// client reads at observation-window scale, without a retained-history scan.
	started := time.Now()
	var barrierBefore time.Duration
	if timing != nil {
		barrierBefore = timing.WindowBarrier
	}
	ctx = context.WithValue(ctx, windowTimingKey{}, timing)
	read, err := l.prepare(ctx, map[string][2]time.Time{
		observationRequestStream: {from, {}},
		observationClientStream:  {from, to},
	})
	if timing != nil {
		timing.WindowScan += time.Since(started) - (timing.WindowBarrier - barrierBefore)
	}
	if err != nil {
		return result, err
	}
	defer read.close()
	read.timing = timing
	read.cacheWindow = true
	joined := time.Now()
	defer func() {
		if timing != nil {
			timing.WindowJoin += time.Since(joined) - read.scanTime - read.decodeTime - read.sortTime - read.cacheTime
			timing.WindowScan += read.scanTime
			timing.WindowDecode += read.decodeTime
			timing.WindowSort += read.sortTime
			timing.WindowCache += read.cacheTime
		}
	}()
	result.Coverage = read.coverage(map[string][2]time.Time{observationRequestStream: {from, to}, observationClientStream: {from, to}})
	result.GapUntil = result.Coverage.GapUntil(from)
	nativeRows := map[string]serving.RequestRow{}
	l.retainWindow(from, to, false, read.now)
	rows, err := l.windowRequests(read, nativeRows, from, to)
	if err != nil {
		return result, err
	}
	result.PathSamples, result.ClientObservations, err = l.clientWindow(read, rows, nativeRows, from, to)
	if err != nil {
		return result, err
	}
	loads := map[string]*WorkloadLoad{}
	in := func(row *serving.RequestRow, offset *int64) bool {
		return offset != nil && serving.ObservationInInterval(row.ReceivedAt.Add(time.Duration(*offset)), from, to)
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		received := serving.ObservationInInterval(row.ReceivedAt, from, to)
		accepted := row.Accepted() && (row.AcceptedNanos == nil && received || in(row, row.AcceptedNanos))
		assigned := row.DispatchedAt != nil && serving.ObservationInInterval(*row.DispatchedAt, from, to) || in(row, row.DispatchedNanos)
		completed := row.Outcome == outcomeCompleted && in(row, row.ElapsedNanos)
		if received {
			result.Arrivals = append(result.Arrivals, requestArrival(row, read.definitions))
		}
		if !received && !accepted && !assigned && !completed {
			continue
		}
		load := loads[row.WorkloadUID]
		if load == nil {
			load = &WorkloadLoad{UID: row.WorkloadUID, Instances: map[string]int{}}
			loads[row.WorkloadUID] = load
		}
		if received {
			load.Received++
		}
		if accepted {
			load.Accepted++
		}
		if assigned {
			load.Assigned++
			load.Instances[row.Instance]++
		}
		if completed {
			load.Completed++
		}
	}
	seconds := result.Duration.Seconds()
	for _, load := range loads {
		load.ReceivedRate = float64(load.Received) / seconds
		load.AcceptedRate = float64(load.Accepted) / seconds
		load.AssignedRate = float64(load.Assigned) / seconds
		load.CompletionRate = float64(load.Completed) / seconds
		result.Workloads = append(result.Workloads, *load)
	}
	sorted := time.Now()
	sort.Slice(result.Workloads, func(a, b int) bool { return result.Workloads[a].UID < result.Workloads[b].UID })
	sort.Slice(result.Arrivals, func(a, b int) bool {
		if result.Arrivals[a].At.Equal(result.Arrivals[b].At) {
			return result.Arrivals[a].ID < result.Arrivals[b].ID
		}
		return result.Arrivals[a].At.Before(result.Arrivals[b].At)
	})
	read.sortTime += time.Since(sorted)
	return result, nil
}

// requestArrival is a received request as the scheduler counts it.
func requestArrival(row *serving.RequestRow, definitions map[string]*serving.Definition) serving.SchedulerArrival {
	return serving.SchedulerArrival{
		ID: row.RequestID, At: row.ReceivedAt, WorkloadUID: row.WorkloadUID, Definition: definitions[row.DefinitionID],
		Lifetime: time.Duration(row.LifetimeNanos), Accepted: row.Accepted(), Compute: row.ValidatedNanos != nil || row.Accepted(), Outcome: row.Outcome,
	}
}

// SchedulerObservationSelection is validated once by the HTTP query boundary.
// Its zero value preserves the bounded append-order read.
type SchedulerObservationSelection struct {
	Order   string
	Filter  string
	UID     string
	Summary bool
}

// SchedulerObservationPage decodes full payloads only for page entries.
// Cursors count selected records; Offset retains the unfiltered append position.
func (l *ObservationLog) SchedulerObservationPage(ctx context.Context, from, to time.Time, limit, offset int, selection SchedulerObservationSelection) ([]SchedulerObservation, int, *time.Time, serving.ObservationCoverage, error) {
	if from.IsZero() || !from.Before(to) || limit <= 0 || offset < 0 {
		return nil, -1, nil, serving.ObservationCoverage{}, &serving.Error{Code: "InvalidQuery", Message: "positive window, bounded limit and nonnegative cursor are required"}
	}
	read, err := l.prepare(ctx, map[string][2]time.Time{observationSchedulerStream: {from, to}})
	if err != nil {
		return nil, -1, nil, serving.ObservationCoverage{}, err
	}
	defer read.close()
	if selection.Order == "desc" {
		return l.schedulerSelectedPage(read, from, to, limit, offset, selection)
	}
	records := make([]SchedulerObservation, 0, limit)
	position, matched, next := 0, 0, -1
	err = l.scanStream(read, observationSchedulerStream, func(raw []byte) error {
		if selection.Summary {
			at, err := observationTimestamp(raw)
			if err != nil || !serving.ObservationInInterval(at, from, to) {
				return err
			}
		}
		var envelope schedulerObservationEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		if !serving.ObservationInInterval(envelope.At, from, to) {
			return nil
		}
		original := position
		position++
		selected, err := schedulerObservationMatches(raw, envelope, selection)
		if err != nil || !selected {
			return err
		}
		matched++
		if matched <= offset {
			return nil
		}
		if len(records) == limit {
			next = offset + len(records)
			return errObservationStop
		}
		record, err := l.decodeSchedulerPageRecord(read, raw, selection.Summary)
		if err != nil {
			return err
		}
		record.Offset = &original
		records = append(records, record)
		return nil
	})
	if err != nil {
		return nil, -1, nil, serving.ObservationCoverage{}, err
	}
	if offset > matched {
		return nil, -1, nil, serving.ObservationCoverage{}, &serving.Error{Code: "InvalidQuery", Message: "cursor exceeds selected observation count"}
	}
	coverage := read.coverage(map[string][2]time.Time{observationSchedulerStream: {from, to}})
	return records, next, coverage.GapUntil(from), coverage, nil
}

// Summary reads never hydrate catalogs or decode candidate, profile, or sample
// bodies. Exact-record reads retain the full validation/restoration path.
func (l *ObservationLog) decodeSchedulerPageRecord(read *observationRead, raw []byte, summary bool) (SchedulerObservation, error) {
	if !summary {
		if err := l.readCatalog(read, read.scanningHour); err != nil {
			return SchedulerObservation{}, err
		}
		return DecodeSchedulerLine(raw, read.catalog())
	}
	var line struct {
		At       time.Time
		Control  *SchedulerControlObservation
		Decision *struct {
			At        time.Time
			Status    string
			Reasons   []string
			Target    *struct{ Keep bool }
			Workloads []serving.SchedulerWorkloadState `json:"workloads"`
		}
		Snapshot *struct{ At time.Time }
		Settings *struct {
			ActorUsername string
			ActorUID      string
			AppliedAt     *time.Time
			Error         string
		}
		Action *struct {
			ID        string
			Kind      string
			Node      serving.NodeReference
			Phase     string
			StartedAt time.Time
		}
		Node        *serving.NodeReference
		Meter       string
		StartedAt   *time.Time
		PowerJoules *float64
	}
	if err := json.Unmarshal(raw, &line); err != nil {
		return SchedulerObservation{}, err
	}
	record := SchedulerObservation{At: line.At, Control: line.Control, Node: line.Node, PowerJoules: line.PowerJoules}
	if d := line.Decision; d != nil {
		record.Decision = &serving.SchedulerDecision{At: d.At, Status: d.Status, Reasons: d.Reasons, Workloads: d.Workloads}
		if d.Target != nil {
			record.Decision.Target = &serving.SchedulerCandidate{Keep: d.Target.Keep}
		}
	}
	if s := line.Snapshot; s != nil {
		record.Snapshot = &serving.SchedulerSnapshot{At: s.At}
	}
	if s := line.Settings; s != nil {
		record.Settings = &SchedulerSettingsChange{ActorUsername: s.ActorUsername, ActorUID: s.ActorUID, AppliedAt: s.AppliedAt, Error: s.Error}
	}
	if a := line.Action; a != nil {
		record.Action = &SchedulerActionObservation{ID: a.ID, Kind: a.Kind, Node: a.Node, Phase: a.Phase, StartedAt: a.StartedAt}
	}
	if line.Meter != "" {
		record.Power = &metricobserver.Result{}
		if line.StartedAt != nil {
			record.Power.StartedAt = *line.StartedAt
		}
	}
	return record, nil
}

const (
	schedulerHistoryDecision = "decision"
	schedulerHistoryAction   = "action"
	schedulerHistoryOther    = "other"
)

type schedulerObservationEnvelope struct {
	At time.Time
	// Decision decodes only the target's keep marker, the decision-change key.
	Decision *struct {
		Target *struct{ Keep bool }
	}
	Snapshot *struct{}
	Settings *struct{}
	Action   *struct{}
	Meter    string
}

func (e schedulerObservationEnvelope) kind() string {
	switch {
	case e.Decision != nil:
		return schedulerHistoryDecision
	case e.Snapshot != nil:
		return "snapshot"
	case e.Settings != nil:
		return "settings"
	case e.Action != nil:
		return schedulerHistoryAction
	case e.Meter != "":
		return "power"
	default:
		return schedulerHistoryOther
	}
}

// Descending selection scans only sort/filter keys, then projects the selected
// page. Ascending selection uses the bounded append-order path above.
func (l *ObservationLog) schedulerSelectedPage(read *observationRead, from, to time.Time, limit, offset int, selection SchedulerObservationSelection) ([]SchedulerObservation, int, *time.Time, serving.ObservationCoverage, error) {
	type entry struct {
		at     time.Time
		offset int
	}
	var entries []entry
	position := 0
	err := l.scanStream(read, observationSchedulerStream, func(raw []byte) error {
		if selection.Summary {
			at, err := observationTimestamp(raw)
			if err != nil || !serving.ObservationInInterval(at, from, to) {
				return err
			}
		}
		var envelope schedulerObservationEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		if !serving.ObservationInInterval(envelope.At, from, to) {
			return nil
		}
		original := position
		position++
		selected, err := schedulerObservationMatches(raw, envelope, selection)
		if err != nil || !selected {
			return err
		}
		entries = append(entries, entry{envelope.At, original})
		return nil
	})
	if err != nil {
		return nil, -1, nil, serving.ObservationCoverage{}, err
	}
	if offset > len(entries) {
		return nil, -1, nil, serving.ObservationCoverage{}, &serving.Error{Code: "InvalidQuery", Message: "cursor exceeds selected observation count"}
	}
	if selection.Order == "desc" {
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].at.Equal(entries[j].at) {
				return entries[i].offset > entries[j].offset
			}
			return entries[i].at.After(entries[j].at)
		})
	}
	end := offset + min(limit, len(entries)-offset)
	next := -1
	if end < len(entries) {
		next = end
	}
	records := make([]SchedulerObservation, end-offset)
	selected := make(map[int]int, len(records))
	for i, entry := range entries[offset:end] {
		selected[entry.offset] = i
	}
	for _, file := range read.files[observationSchedulerStream] {
		if _, err := file.file.Seek(0, io.SeekStart); err != nil {
			return nil, -1, nil, serving.ObservationCoverage{}, err
		}
	}
	position = 0
	if len(selected) != 0 {
		err = l.scanStream(read, observationSchedulerStream, func(raw []byte) error {
			var header struct{ At time.Time }
			if selection.Summary {
				var err error
				header.At, err = observationTimestamp(raw)
				if err != nil {
					return err
				}
			} else if err := json.Unmarshal(raw, &header); err != nil {
				return err
			}
			if !serving.ObservationInInterval(header.At, from, to) {
				return nil
			}
			original := position
			position++
			index, ok := selected[original]
			if !ok {
				return nil
			}
			record, err := l.decodeSchedulerPageRecord(read, raw, selection.Summary)
			if err != nil {
				return err
			}
			record.Offset = &original
			records[index] = record
			delete(selected, original)
			if len(selected) == 0 {
				return errObservationStop
			}
			return nil
		})
	}
	if err != nil {
		return nil, -1, nil, serving.ObservationCoverage{}, err
	}
	coverage := read.coverage(map[string][2]time.Time{observationSchedulerStream: {from, to}})
	return records, next, coverage.GapUntil(from), coverage, nil
}

func schedulerObservationMatches(raw []byte, envelope schedulerObservationEnvelope, selection SchedulerObservationSelection) (bool, error) {
	kind := envelope.kind()
	switch selection.Filter {
	case "", "all", kind:
	case "decision-action":
		if kind != schedulerHistoryDecision && kind != schedulerHistoryAction {
			return false, nil
		}
	case "decision-change":
		// A changed configuration is a selected target other than the keep plan.
		if kind != schedulerHistoryDecision || envelope.Decision.Target == nil || envelope.Decision.Target.Keep {
			return false, nil
		}
	default:
		return false, nil
	}
	if selection.UID != "" {
		return schedulerObservationHasWorkload(raw, selection.UID)
	}
	return true, nil
}

// Project only workload relationships, never arbitrary UID-looking strings in
// Node identities, error text or evidence payloads.
func schedulerObservationHasWorkload(raw []byte, uid string) (bool, error) {
	type workload struct {
		UID              string
		WorkloadUID      string
		OwnerWorkloadUID string
	}
	type candidate struct {
		Instances []workload
		Routing   map[string]json.RawMessage
	}
	var record struct {
		Decision *struct {
			Workloads []workload
			Keep      candidate
			Target    *candidate
		}
		Snapshot *struct {
			WorkloadIDs []string
			Workloads   []workload
			Instances   []workload
			Routing     map[string]json.RawMessage
		}
		Settings *struct {
			Before, After                           serving.SchedulerSettings
			BeforeStaticRouting, AfterStaticRouting map[string]json.RawMessage
		}
		Action *struct {
			Expected struct {
				WorkloadUID  string
				WorkloadUIDs []string
				Prepare      *struct {
					Owner    *workload
					Instance workload
				}
				Target *candidate
			}
		}
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return false, err
	}
	has := func(w workload) bool { return w.UID == uid || w.WorkloadUID == uid || w.OwnerWorkloadUID == uid }
	inCandidate := func(c *candidate) bool {
		return c != nil && (c.Routing[uid] != nil || slices.ContainsFunc(c.Instances, has))
	}
	if d := record.Decision; d != nil && (slices.ContainsFunc(d.Workloads, has) || inCandidate(&d.Keep) || inCandidate(d.Target)) {
		return true, nil
	}
	if s := record.Snapshot; s != nil {
		if slices.ContainsFunc(s.WorkloadIDs, func(id string) bool { return strings.HasPrefix(id, uid+"/") }) ||
			slices.ContainsFunc(s.Workloads, has) || slices.ContainsFunc(s.Instances, has) || s.Routing[uid] != nil {
			return true, nil
		}
	}
	if s := record.Settings; s != nil && (slices.Contains(s.Before.AutonomousWorkloads, uid) || slices.Contains(s.After.AutonomousWorkloads, uid) || s.BeforeStaticRouting[uid] != nil || s.AfterStaticRouting[uid] != nil) {
		return true, nil
	}
	if a := record.Action; a != nil {
		e := a.Expected
		if e.WorkloadUID != "" {
			return e.WorkloadUID == uid, nil
		}
		if e.WorkloadUIDs != nil {
			return slices.Contains(e.WorkloadUIDs, uid), nil
		}
		if inCandidate(e.Target) || e.Prepare != nil && (e.Prepare.Owner != nil && has(*e.Prepare.Owner) || has(e.Prepare.Instance)) {
			return true, nil
		}
	}
	return false, nil
}
