package serving

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"time"

	jsoniter "github.com/json-iterator/go"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
)

// SchedulerSettingsChange is one settings PUT; its validatedAt identifies it.
// The staticRouting pair is the ConfigMap declaration around the change, and
// Reason is the operator's selection rationale for a staticRouting change.
type SchedulerSettingsChange struct {
	ActorUsername       string                    `json:"actorUsername"`
	ActorUID            string                    `json:"actorUID"`
	Before              SchedulerSettings         `json:"before"`
	After               SchedulerSettings         `json:"after"`
	BeforeStaticRouting map[string]map[string]int `json:"beforeStaticRouting,omitempty"`
	AfterStaticRouting  map[string]map[string]int `json:"afterStaticRouting,omitempty"`
	Reason              string                    `json:"reason,omitempty"`
	ValidatedAt         time.Time                 `json:"validatedAt"`
	AppliedAt           *time.Time                `json:"appliedAt,omitempty"`
	Affected            []NodeReference           `json:"affected"`
	Error               string                    `json:"error,omitempty"`
}

type SchedulerActionObservation struct {
	ID              string                         `json:"id"`
	Kind            string                         `json:"kind"`
	Node            NodeReference                  `json:"node"`
	Instance        string                         `json:"instance,omitempty"`
	Phase           string                         `json:"phase"`
	StartedAt       time.Time                      `json:"startedAt"`
	CompletedAt     *time.Time                     `json:"completedAt,omitempty"`
	Expected        json.RawMessage                `json:"expected,omitempty"`
	Actual          json.RawMessage                `json:"actual,omitempty"`
	Error           string                         `json:"error,omitempty"`
	Evidence        []evidencestore.ObjectIdentity `json:"evidence,omitempty"`
	ExpectedSeconds *float64                       `json:"expectedSeconds"`
	ExpectedSource  *ActionExpectedSource          `json:"expectedSource"`
}

type SchedulerObservation struct {
	At            time.Time                   `json:"at"`
	Decision      *SchedulerDecision          `json:"decision,omitempty"`
	Snapshot      *SchedulerSnapshot          `json:"snapshot,omitempty"`
	Settings      *SchedulerSettingsChange    `json:"settings,omitempty"`
	Action        *SchedulerActionObservation `json:"action,omitempty"`
	Power         *metricobserver.Result      `json:"power,omitempty"`
	Node          *NodeReference              `json:"node,omitempty"`
	Meter         string                      `json:"meter,omitempty"`
	PowerScope    string                      `json:"powerScope,omitempty"`
	PowerSampling *metricobserver.Request     `json:"powerSampling,omitempty"`
	PowerCoverage *powerwindow.Policy         `json:"powerCoverage,omitempty"`
	PowerJoules   *float64                    `json:"powerJoules,omitempty"`
	PowerError    string                      `json:"powerError,omitempty"`
}

// schedulerLine is the stored scheduler record. Immutable bodies live in the
// catalog: a power row names its meter, and a snapshot names definitions,
// costs and baselines by reference.
type schedulerLine struct {
	At          time.Time                   `json:"at"`
	Decision    *SchedulerDecision          `json:"decision,omitempty"`
	Snapshot    *SchedulerSnapshot          `json:"snapshot,omitempty"`
	Settings    *SchedulerSettingsChange    `json:"settings,omitempty"`
	Action      *SchedulerActionObservation `json:"action,omitempty"`
	Node        *NodeReference              `json:"node,omitempty"`
	Meter       string                      `json:"meter,omitempty"`
	StartedAt   *time.Time                  `json:"startedAt,omitempty"`
	Samples     []metricobserver.Sample     `json:"samples,omitempty"`
	Failures    []metricobserver.Failure    `json:"failures,omitempty"`
	PowerJoules *float64                    `json:"powerJoules,omitempty"`
	PowerError  string                      `json:"powerError,omitempty"`
}

// ObservationMeter is the catalog body of a meter.
type ObservationMeter struct {
	Node     NodeReference                 `json:"node"`
	Scope    string                        `json:"scope"`
	Source   metricobserver.SourceIdentity `json:"source"`
	Sampling *metricobserver.Request       `json:"sampling,omitempty"`
	Coverage *powerwindow.Policy           `json:"coverage,omitempty"`
}

func costRef(c OperationCost) string    { return c.ID + "@" + c.Reference.Key }
func baselineRef(b NodeBaseline) string { return b.ID + "@" + b.Reference.Key }
func (o *observationReferences) add(kind, key string, body any) {
	o.rows = append(o.rows, observationReference{kind: kind, key: key, body: body})
}

type observationReference struct {
	kind, key string
	body      any
}

type observationReferences struct{ rows []observationReference }

func (l *ObservationLog) SchedulerRecord(event SchedulerObservation) error {
	if event.At.IsZero() {
		return failure("InvalidObservation", "scheduler observation time is required")
	}
	refs := &observationReferences{}
	line := schedulerLine{At: event.At, Settings: event.Settings, Action: event.Action}
	if d := event.Decision; d != nil {
		recorded := *d
		recorded.Keep = refs.candidate(d.Keep)
		if d.Target != nil && d.Target.ID == d.Keep.ID {
			recorded.Target = &SchedulerCandidate{ID: d.Target.ID, Keep: true}
		} else if d.Target != nil {
			target := refs.candidate(*d.Target)
			recorded.Target = &target
		}
		line.Decision = &recorded
	}
	if s := event.Snapshot; s != nil {
		recorded := *s
		recorded.Instances = refs.instances(s.Instances)
		// Window counts stay in Decision.Search; the request stream holds arrivals.
		recorded.Arrivals = nil
		recorded.Workloads, recorded.WorkloadIDs = nil, make([]string, len(s.Workloads))
		for i, d := range s.Workloads {
			recorded.WorkloadIDs[i] = d.Identity()
			refs.add("definition", d.Identity(), d)
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
				refs.add("definition", r.Definition.Identity(), r.Definition)
				recorded.Outstanding[i].DefinitionID, recorded.Outstanding[i].Definition = r.Definition.Identity(), nil
			}
		}
		recorded.Options = slices.Clone(s.Options)
		for i := range recorded.Options {
			recorded.Options[i].Instance = refs.instance(recorded.Options[i].Instance)
		}
		recorded.Reservations = slices.Clone(s.Reservations)
		for i, reservation := range s.Reservations {
			if reservation.Target != nil {
				target := refs.candidate(*reservation.Target)
				recorded.Reservations[i].Target = &target
			}
		}
		line.Snapshot = &recorded
	}
	if power := event.Power; power != nil {
		if event.Meter == "" || event.Node == nil {
			return failure("InvalidObservation", "power observation requires its meter and Node")
		}
		started := power.StartedAt
		line.Node, line.Meter, line.StartedAt = event.Node, event.Meter, &started
		line.Samples, line.Failures, line.PowerJoules, line.PowerError = power.Samples, power.Failures, event.PowerJoules, event.PowerError
		refs.add("meter", event.Meter, ObservationMeter{
			Node: *event.Node, Scope: event.PowerScope, Source: power.Source,
			Sampling: event.PowerSampling, Coverage: event.PowerCoverage,
		})
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, ref := range refs.rows {
		if err := l.referenceLocked(ref.kind, ref.key, event.At, now, ref.body); err != nil {
			return err
		}
	}
	return l.appendLocked(observationSchedulerStream, now, line)
}

// Stored records keep profile IDs; the catalog holds profile bodies.
func (o *observationReferences) candidate(c SchedulerCandidate) SchedulerCandidate {
	c.Instances = o.instances(c.Instances)
	return c
}

func (o *observationReferences) instances(instances []SchedulerInstance) []SchedulerInstance {
	recorded := slices.Clone(instances)
	for i := range recorded {
		recorded[i] = o.instance(recorded[i])
	}
	return recorded
}

func (o *observationReferences) instance(instance SchedulerInstance) SchedulerInstance {
	if instance.Profile != nil {
		o.add("profile", instance.Profile.ID, instance.Profile)
		instance.ProfileID, instance.Profile = instance.Profile.ID, nil
	}
	return instance
}

// ObservationCatalog resolves a reference to the last catalog row of its key
// at or before the referencing record; a record older than every row of its
// key uses the earliest row.
type ObservationCatalog struct {
	rows     map[string][]CatalogRow
	routerID string
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
		return failure("IncompleteObservation", "catalog has no %s %s", kind, key)
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

// decodeWindowEvidence accepts only the maximum-demand record shape. Historical
// timer evidence stays in the raw archive, not in current typed observations.
func decodeWindowEvidence(raw json.RawMessage) (*SchedulerDownState, error) {
	state := &SchedulerDownState{}
	shape := struct {
		*SchedulerDownState
		WindowMax   *float64       `json:"windowMax"`
		WindowNanos *time.Duration `json:"windowNanos"`
		Supply      *float64       `json:"supply"`
	}{SchedulerDownState: state}
	if err := json.Unmarshal(raw, &shape); err != nil {
		return nil, err
	}
	if shape.WindowMax == nil || shape.WindowNanos == nil || shape.Supply == nil {
		return nil, nil
	}
	state.WindowMax, state.WindowNanos, state.Supply = *shape.WindowMax, *shape.WindowNanos, *shape.Supply
	return state, nil
}

func (w *SchedulerCapacityWorkload) UnmarshalJSON(raw []byte) error {
	type workload SchedulerCapacityWorkload
	record := struct {
		*workload
		ScaleDown json.RawMessage `json:"scaleDown"`
	}{workload: (*workload)(w)}
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	w.ScaleDown = nil
	if len(record.ScaleDown) == 0 {
		return nil
	}
	var err error
	w.ScaleDown, err = decodeWindowEvidence(record.ScaleDown)
	return err
}

func (s *SchedulerSnapshot) UnmarshalJSON(raw []byte) error {
	type snapshot SchedulerSnapshot
	record := struct {
		*snapshot
		ScaleDown map[string]json.RawMessage `json:"scaleDown"`
	}{snapshot: (*snapshot)(s)}
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	s.ScaleDown = nil
	for uid, evidence := range record.ScaleDown {
		state, err := decodeWindowEvidence(evidence)
		if err != nil {
			return err
		}
		if state != nil {
			if s.ScaleDown == nil {
				s.ScaleDown = map[string]SchedulerDownState{}
			}
			s.ScaleDown[uid] = *state
		}
	}
	if len(record.ScaleDown) != len(s.ScaleDown) {
		s.EmptyNodeScaleDownAllowed = false
	}
	return nil
}

// DecodeSchedulerLine decodes one stored scheduler line and fills every
// reference from the catalog.
func DecodeSchedulerLine(raw []byte, catalog *ObservationCatalog) (SchedulerObservation, error) {
	var line schedulerLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return SchedulerObservation{}, err
	}
	event := SchedulerObservation{At: line.At, Decision: line.Decision, Snapshot: line.Snapshot, Settings: line.Settings, Action: line.Action}
	if d := event.Decision; d != nil && d.Target != nil && d.Target.ID == d.Keep.ID {
		target := d.Keep
		d.Target = &target
	}
	if s := event.Snapshot; s != nil {
		s.Workloads, s.Costs, s.Baselines = nil, nil, nil
		if len(s.WorkloadIDs) > 0 {
			s.Workloads = make([]*Definition, len(s.WorkloadIDs))
		}
		for i, id := range s.WorkloadIDs {
			if err := catalog.body("definition", id, line.At, &s.Workloads[i]); err != nil {
				return event, err
			}
		}
		if len(s.CostRefs) > 0 {
			s.Costs = make([]OperationCost, len(s.CostRefs))
		}
		for i, id := range s.CostRefs {
			if err := catalog.body("cost", id, line.At, &s.Costs[i]); err != nil {
				return event, err
			}
		}
		if len(s.BaselineRefs) > 0 {
			s.Baselines = make([]NodeBaseline, len(s.BaselineRefs))
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
	From       time.Time           `json:"from"`
	To         time.Time           `json:"to"`
	Duration   time.Duration       `json:"durationNanos"`
	Population string              `json:"population"`
	Workloads  []WorkloadLoad      `json:"workloads"`
	Arrivals   []SchedulerArrival  `json:"arrivals"`
	GapUntil   *time.Time          `json:"gapUntil,omitempty"`
	Coverage   ObservationCoverage `json:"coverage"`
}

// LoadWindow counts each stage whose time, receipt plus stage offset, lies in
// [from,to), independently of result cohort filters.
func (l *ObservationLog) LoadWindow(ctx context.Context, from, to time.Time) (LoadWindow, error) {
	result := LoadWindow{From: from, To: to, Duration: to.Sub(from), Population: "all ingress receipts; computable demand separately marked after authorization and input validation; stage time [from,to); one request = one input"}
	if from.IsZero() || !from.Before(to) {
		return result, failure("InvalidQuery", "positive explicit load window is required")
	}
	read, err := l.prepare(ctx, map[string][2]time.Time{observationRequestStream: {from, {}}})
	if err != nil {
		return result, err
	}
	defer read.close()
	result.Coverage = read.coverage(map[string][2]time.Time{observationRequestStream: {from, to}})
	result.GapUntil = result.Coverage.gapUntil(from)
	rows, err := l.requestRows(read, func(*requestEnvelope) bool { return true })
	if err != nil {
		return result, err
	}
	loads := map[string]*WorkloadLoad{}
	in := func(row *RequestRow, offset *int64) bool {
		return offset != nil && observationInInterval(row.ReceivedAt.Add(time.Duration(*offset)), from, to)
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		received := observationInInterval(row.ReceivedAt, from, to)
		accepted := row.accepted() && (row.AcceptedNanos == nil && received || in(row, row.AcceptedNanos))
		assigned := row.DispatchedAt != nil && observationInInterval(*row.DispatchedAt, from, to) || in(row, row.DispatchedNanos)
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
	sort.Slice(result.Workloads, func(a, b int) bool { return result.Workloads[a].UID < result.Workloads[b].UID })
	sort.Slice(result.Arrivals, func(a, b int) bool {
		if result.Arrivals[a].At.Equal(result.Arrivals[b].At) {
			return result.Arrivals[a].ID < result.Arrivals[b].ID
		}
		return result.Arrivals[a].At.Before(result.Arrivals[b].At)
	})
	return result, nil
}

// requestArrival is a received request as the scheduler counts it.
func requestArrival(row *RequestRow, definitions map[string]*Definition) SchedulerArrival {
	definition := row.definition
	if definition == nil {
		definition = definitions[row.DefinitionID]
	}
	return SchedulerArrival{
		ID: row.RequestID, At: row.ReceivedAt, WorkloadUID: row.WorkloadUID, Definition: definition,
		Lifetime: time.Duration(row.LifetimeNanos), Accepted: row.accepted(), Compute: row.ValidatedNanos != nil || row.accepted(), Outcome: row.Outcome,
	}
}

func (l *ObservationLog) SchedulerArrivals(ctx context.Context, from, to time.Time) ([]SchedulerArrival, error) {
	window, err := l.LoadWindow(ctx, from, to)
	if err == nil && l.config.RouterSources != nil {
		for _, gap := range window.Coverage.Gaps {
			if gap.Stream == observationRequestStream && gap.From.Before(to) && gap.To.After(from) {
				return nil, failure("IncompleteObservation", "router %q request stream unavailable in [%s,%s): %s",
					gap.RouterID, gap.From.Format(time.RFC3339Nano), gap.To.Format(time.RFC3339Nano), gap.Reason)
			}
		}
	}
	return window.Arrivals, err
}

// SchedulerObservationPage decodes full payloads only for page entries and
// stops at the next matching event. Cursor offsets apply to the selected
// records in append order.
func (l *ObservationLog) SchedulerObservationPage(ctx context.Context, from, to time.Time, limit, offset int) ([]SchedulerObservation, int, *time.Time, ObservationCoverage, error) {
	if from.IsZero() || !from.Before(to) || limit <= 0 || offset < 0 {
		return nil, -1, nil, ObservationCoverage{}, failure("InvalidQuery", "positive window, bounded limit and nonnegative cursor are required")
	}
	read, err := l.prepare(ctx, map[string][2]time.Time{observationSchedulerStream: {from, to}})
	if err != nil {
		return nil, -1, nil, ObservationCoverage{}, err
	}
	defer read.close()
	records := make([]SchedulerObservation, 0, limit)
	matched, next := 0, -1
	err = l.scanStream(read, observationSchedulerStream, func(raw []byte) error {
		var envelope struct {
			At time.Time `json:"at"`
		}
		if err := jsoniter.ConfigCompatibleWithStandardLibrary.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		if !observationInInterval(envelope.At, from, to) {
			return observationJSONSyntaxError(raw)
		}
		matched++
		if matched <= offset {
			return observationJSONSyntaxError(raw)
		}
		if len(records) == limit {
			next = offset + len(records)
			if err := observationJSONSyntaxError(raw); err != nil {
				return err
			}
			return errObservationStop
		}
		if err := l.readCatalog(read, read.scanningHour); err != nil {
			return err
		}
		record, err := DecodeSchedulerLine(raw, read.catalog())
		if err != nil {
			return err
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		return nil, -1, nil, ObservationCoverage{}, err
	}
	if offset > matched {
		return nil, -1, nil, ObservationCoverage{}, failure("InvalidQuery", "cursor exceeds selected observation count")
	}
	coverage := read.coverage(map[string][2]time.Time{observationSchedulerStream: {from, to}})
	return records, next, coverage.gapUntil(from), coverage, nil
}
