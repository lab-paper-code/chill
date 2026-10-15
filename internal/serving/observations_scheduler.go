package serving

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	jsoniter "github.com/json-iterator/go"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
)

type SchedulerSettingsChange struct {
	ActorUsername string            `json:"actorUsername"`
	ActorUID      string            `json:"actorUID"`
	Before        SchedulerSettings `json:"before"`
	After         SchedulerSettings `json:"after"`
	ValidatedAt   time.Time         `json:"validatedAt"`
	AppliedAt     *time.Time        `json:"appliedAt,omitempty"`
	Affected      []NodeReference   `json:"affected"`
	Error         string            `json:"error,omitempty"`
}

type SchedulerActionObservation struct {
	ID          string                         `json:"id"`
	Kind        string                         `json:"kind"`
	Node        NodeReference                  `json:"node"`
	Instance    string                         `json:"instance,omitempty"`
	Phase       string                         `json:"phase"`
	StartedAt   time.Time                      `json:"startedAt"`
	CompletedAt *time.Time                     `json:"completedAt,omitempty"`
	Expected    json.RawMessage                `json:"expected,omitempty"`
	Actual      json.RawMessage                `json:"actual,omitempty"`
	Error       string                         `json:"error,omitempty"`
	Evidence    []evidencestore.ObjectIdentity `json:"evidence,omitempty"`
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

func (j *Journal) SchedulerRecord(event SchedulerObservation) error {
	if event.At.IsZero() {
		return failure("InvalidObservation", "scheduler observation time is required")
	}
	return j.append(observationSchedulerStream, observationRecord{Scheduler: &event})
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
	From              time.Time          `json:"from"`
	To                time.Time          `json:"to"`
	Duration          time.Duration      `json:"durationNanos"`
	Population        string             `json:"population"`
	Workloads         []WorkloadLoad     `json:"workloads"`
	Arrivals          []SchedulerArrival `json:"arrivals"`
	IncompleteWindows []string           `json:"incompleteWindows"`
}

type windowLoadFact struct {
	received                 time.Time
	uid, definition, outcome string
	inputPath                int
	lifetime                 time.Duration
	accepted, compute        bool
}

func (fact *windowLoadFact) add(event *RequestEvent) {
	if event.WorkloadUID != "" {
		fact.uid = event.WorkloadUID
	}
	if event.Definition != "" {
		fact.definition = event.Definition
	}
	if event.InputPath != nil {
		fact.inputPath = *event.InputPath
	}
	if event.LifetimeNanos > 0 {
		fact.lifetime = time.Duration(event.LifetimeNanos)
	}
	if event.Kind == phaseAccepted {
		fact.accepted = true
	}
	if event.Kind == "validated" {
		fact.compute = true
	}
	if event.Kind == eventResponseFinished {
		fact.outcome = event.Outcome
	}
}

// LoadWindow uses event-time [from,to), independently of archive rotation and
// result cohort filters. Completed counts completion events, not received cohort.
func (j *Journal) LoadWindow(from, to time.Time) (LoadWindow, error) {
	return j.loadWindow(from, to, false)
}

// SchedulerHistoryWindow requires complete evidence for one consumed receipt
// interval and its selected request/definition dependencies, not unrelated history.
func (j *Journal) SchedulerHistoryWindow(from, to time.Time) (LoadWindow, error) {
	return j.loadWindow(from, to, true)
}

func (j *Journal) loadWindow(from, to time.Time, history bool) (LoadWindow, error) {
	result := LoadWindow{From: from, To: to, Duration: to.Sub(from), Population: "all ingress receipt events; computable demand separately marked after authorization and input validation; event-time [from,to); one request = one input"}
	if from.IsZero() || !from.Before(to) {
		return result, failure("InvalidQuery", "positive explicit load window is required")
	}
	snapshot, err := j.snapshot(observationDefinitionStream, observationRequestStream)
	if err != nil {
		return result, err
	}
	snapshot.selectLoadWindow(from, to)
	definitions := map[string]*Definition{}
	var definitionWindows map[string]string
	var requiredWindows map[string]bool
	if history {
		definitionWindows = map[string]string{}
		requiredWindows = map[string]bool{}
	}
	if err := snapshot.scan(observationDefinitionStream, func(record observationRecord) {
		if record.Definition != nil {
			definitions[record.Definition.Identity] = record.Definition.Definition
			if history {
				definitionWindows[record.Definition.Identity] = record.Window
			}
		}
	}); err != nil {
		return result, err
	}
	loads := map[string]*WorkloadLoad{}
	facts := map[string]*windowLoadFact{}
	if err := snapshot.scan(observationRequestStream, func(record observationRecord) {
		event := record.Request
		if event == nil {
			return
		}
		at := event.At
		if event.Kind == phaseReceived && !event.ReceivedAt.IsZero() {
			at = event.ReceivedAt
		}
		fact := facts[event.RequestID]
		if event.Kind == phaseReceived && observationInInterval(at, from, to) {
			if fact == nil {
				fact = &windowLoadFact{}
				facts[event.RequestID] = fact
			}
			fact.received = at
		}
		if fact != nil {
			fact.add(event)
			if history {
				requiredWindows[record.Window] = true
			}
		}
		if !observationInInterval(at, from, to) {
			return
		}
		load := loads[event.WorkloadUID]
		if load == nil {
			load = &WorkloadLoad{UID: event.WorkloadUID, Instances: map[string]int{}}
			loads[event.WorkloadUID] = load
		}
		switch event.Kind {
		case phaseReceived:
			load.Received++
		case phaseAccepted:
			load.Accepted++
		case phaseAssigned:
			load.Assigned++
			if event.Assignment != nil {
				load.Instances[event.Assignment.Instance]++
			}
		case eventResponseFinished:
			if event.Outcome == outcomeCompleted {
				load.Completed++
			}
		}
	}); err != nil {
		return result, err
	}
	for id, fact := range facts {
		result.Arrivals = append(result.Arrivals, SchedulerArrival{ID: id, At: fact.received, WorkloadUID: fact.uid, Definition: definitions[fact.definition], InputPath: fact.inputPath, Lifetime: fact.lifetime, Accepted: fact.accepted, Compute: fact.compute, Outcome: fact.outcome})
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
	result.IncompleteWindows, err = snapshot.loadWindowCompleteness(from, to, history, facts, definitions, definitionWindows, requiredWindows)
	if err != nil {
		return result, err
	}
	return result, nil
}

// loadWindowCompleteness only reads the selected facts and captured dependency
// metadata; arrival construction stays in the ordinary load reduction.
func (s *observationSnapshot) loadWindowCompleteness(from, to time.Time, history bool, facts map[string]*windowLoadFact, definitions map[string]*Definition, definitionWindows map[string]string, requiredWindows map[string]bool) ([]string, error) {
	var overlapping []string
	var incomplete map[string]bool
	if history {
		incomplete = map[string]bool{}
	}
	for _, window := range s.windows {
		if !window.Incomplete {
			continue
		}
		if window.overlaps(from, to) {
			overlapping = append(overlapping, window.ID)
		}
		if history {
			incomplete[window.ID] = true
		}
	}
	if !history {
		return overlapping, nil
	}
	if len(overlapping) > 0 {
		return overlapping, failure("IncompleteObservation", "history interval overlaps incomplete observation window %s", overlapping[0])
	}
	for _, prefix := range s.streams[observationRequestStream] {
		window := s.windows[prefix.window]
		if incomplete[window.ID] && !prefix.requestCensus.trusted {
			return overlapping, failure("IncompleteObservation", "history interval has an unknown incomplete request prefix in %s", window.ID)
		}
	}
	for window := range requiredWindows {
		if incomplete[window] {
			return overlapping, failure("IncompleteObservation", "history receipt facts depend on incomplete observation window %s", window)
		}
	}
	for id, fact := range facts {
		if !fact.compute && !fact.accepted && fact.outcome == "" {
			return overlapping, failure("IncompleteObservation", "history receipt %s has no durable demand classification or terminal outcome", id)
		}
		if (fact.compute || fact.accepted) && definitions[fact.definition] == nil {
			return overlapping, failure("IncompleteObservation", "history receipt %s has no immutable definition", id)
		}
		if window := definitionWindows[fact.definition]; incomplete[window] {
			return overlapping, failure("IncompleteObservation", "history receipt %s depends on incomplete definition window %s", id, window)
		}
	}
	return overlapping, nil
}

// ObservedOverhead is the joint completion overhead the Serving entry point
// measured on its own monotonic clock for completed requests received in
// [from,to), keyed by Serving instance ID. Each request contributes (receipt to
// exchange finish - native elapsed) + (response delivery - exchange finish):
// all non-native receipt-to-response time, including ingress and dispatch.
// The one-way delivery and return legs are not observable without cross-host
// timestamps, so no one-way value is derived. Completion is the observed maximum,
// not a future bound. The frozen assignment and definitions identify the measured
// execution condition independently of the physical instance's continued presence.
type ObservedOverhead struct {
	Completion    time.Duration                  `json:"completionNanos"`
	Node          NodeReference                  `json:"node"`
	Profile       string                         `json:"profile"`
	Artifact      string                         `json:"artifact"`
	ExecutionPath string                         `json:"executionPath"`
	Definitions   map[string]*Definition         `json:"definitions"`
	Requests      int                            `json:"requests"`
	TrialIDs      []string                       `json:"trialIDs"`
	Source        string                         `json:"source"`
	References    []evidencestore.ObjectIdentity `json:"references,omitempty"`
	From          time.Time                      `json:"from"`
	To            time.Time                      `json:"to"`
	validated     bool
}

// Valid reports boundary construction, never a caller-supplied scalar flag.
func (o *ObservedOverhead) Valid() bool { return o != nil && o.validated }

type overheadFact struct {
	assignment                   *Assignment
	workload, definition         string
	exchange, response, residual int64
	haveExchange, haveResponse   bool
}

func (fact *overheadFact) add(event *RequestEvent) {
	switch event.Kind {
	case eventBackendExchangeFinished:
		p, a := event.NativeProgress, event.Assignment
		if a == nil || p == nil || p.ProfileID != a.Profile || p.Phase != phaseFinished || !p.ExecutionComplete || event.ClientSpanNanos <= 0 || event.ClientSpanNanos > event.ElapsedNanos || !schedulerFinite(p.NativeElapsedMS) || p.NativeElapsedMS < 0 || p.NativeElapsedMS*float64(time.Millisecond) > float64(event.ClientSpanNanos) {
			return
		}
		fact.assignment = a
		fact.workload, fact.definition = event.WorkloadUID, event.Definition
		fact.exchange = event.ElapsedNanos
		fact.residual = event.ElapsedNanos - int64(p.NativeElapsedMS*float64(time.Millisecond))
		fact.haveExchange = true
	case eventResponseFinished:
		fact.response = event.ElapsedNanos
		fact.haveResponse = true
	}
}

func overheadProjection(facts map[string]*overheadFact, definitions map[string]*Definition, from, to time.Time, references []evidencestore.ObjectIdentity) map[string]ObservedOverhead {
	result := completedOverheads(facts, definitions)
	source := fmt.Sprintf("serving-observed window=%s/%s completion overhead=max(receipt to exchange finish - native elapsed + response delivery - exchange finish) applied once at completion; observed maximum, not a future guarantee; one-way delivery not observed", from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano))
	for id, o := range result {
		o.Source = fmt.Sprintf("%s requests=%d", source, o.Requests)
		o.From, o.To, o.References, o.validated = from, to, references, true
		result[id] = o
	}
	return result
}

func (j *Journal) ObservedOverhead(from, to time.Time) (map[string]ObservedOverhead, error) {
	if from.IsZero() || !from.Before(to) {
		return nil, failure("InvalidQuery", "positive explicit overhead window is required")
	}
	snapshot, err := j.snapshot(observationDefinitionStream, observationRequestStream)
	if err != nil {
		return nil, err
	}
	prefixes := snapshot.streams[observationRequestStream][:0]
	for _, prefix := range snapshot.streams[observationRequestStream] {
		if !prefix.requestCensus.trusted || prefix.requestCensus.receivedAt.intersects(from, to) {
			prefixes = append(prefixes, prefix)
		}
	}
	snapshot.streams[observationRequestStream] = prefixes
	definitions := map[string]*Definition{}
	if err := snapshot.scan(observationDefinitionStream, func(record observationRecord) {
		if record.Definition != nil {
			definitions[record.Definition.Identity] = record.Definition.Definition
		}
	}); err != nil {
		return nil, err
	}
	facts := map[string]*overheadFact{}
	if err := snapshot.scan(observationRequestStream, func(record observationRecord) {
		event := record.Request
		if event == nil || event.Outcome != outcomeCompleted || !observationInInterval(event.ReceivedAt, from, to) {
			return
		}
		fact := facts[event.RequestID]
		if fact == nil {
			fact = &overheadFact{}
			facts[event.RequestID] = fact
		}
		fact.add(event)
	}); err != nil {
		return nil, err
	}
	// The scan marks a newly detected torn tail; judge windows only afterwards.
	for _, window := range snapshot.windows {
		if window.Incomplete && window.overlaps(from, to) {
			return nil, failure("IncompleteObservation", "overhead window overlaps incomplete observation window %s", window.ID)
		}
	}
	return overheadProjection(facts, definitions, from, to, nil), nil
}

// completedOverheads joins completed spans with their frozen model/input identity
// and takes the measured maximum for each immutable physical executor.
func completedOverheads(facts map[string]*overheadFact, definitions map[string]*Definition) map[string]ObservedOverhead {
	result := map[string]ObservedOverhead{}
	invalid := map[string]bool{}
	for requestID, fact := range facts {
		returned := fact.response - fact.exchange
		a, d := fact.assignment, definitions[fact.definition]
		if !fact.haveExchange || !fact.haveResponse || a == nil || a.Instance == "" || a.Node.Name == "" || a.Node.UID == "" || a.Profile == "" || a.Artifact == "" || a.ExecutionPath == "" || fact.workload == "" || d == nil || d.UID != fact.workload || fact.residual < 0 || returned < 0 || invalid[a.Instance] {
			continue
		}
		if !slices.ContainsFunc(d.Model.Paths, func(path ModelPath) bool { return path.Name == a.ExecutionPath && path.Artifact == a.Artifact }) {
			continue
		}
		o := result[a.Instance]
		if o.Definitions == nil {
			o = ObservedOverhead{Node: a.Node, Profile: a.Profile, Artifact: a.Artifact, ExecutionPath: a.ExecutionPath, Definitions: map[string]*Definition{}}
		} else if o.Node != a.Node || o.Profile != a.Profile || o.Artifact != a.Artifact || o.ExecutionPath != a.ExecutionPath {
			// A physical executor cannot change its immutable execution identity.
			delete(result, a.Instance)
			invalid[a.Instance] = true
			continue
		}
		o.Completion = max(o.Completion, time.Duration(fact.residual+returned))
		o.Definitions[fact.definition] = d
		o.Requests++
		o.TrialIDs = append(o.TrialIDs, requestID)
		result[a.Instance] = o
	}
	for id, o := range result {
		slices.Sort(o.TrialIDs)
		result[id] = o
	}
	return result
}

func (j *Journal) SchedulerArrivals(from, to time.Time) ([]SchedulerArrival, error) {
	window, err := j.LoadWindow(from, to)
	return window.Arrivals, err
}

// SchedulerObservationPage uses retained append order, decoding full payloads
// only for page entries and stopping at the next matching event.
// Cursor offsets apply to the selected event window, independently of archives.
func (j *Journal) SchedulerObservationPage(from, to time.Time, limit, offset int) ([]SchedulerObservation, int, error) {
	if from.IsZero() || !from.Before(to) || limit <= 0 || offset < 0 {
		return nil, -1, failure("InvalidQuery", "positive window, bounded limit and nonnegative cursor are required")
	}
	snapshot, err := j.snapshot(observationSchedulerStream)
	if err != nil {
		return nil, -1, err
	}
	prefixes := snapshot.streams[observationSchedulerStream][:0]
	for _, prefix := range snapshot.streams[observationSchedulerStream] {
		if !prefix.census.trusted || prefix.census.schedulerAt.intersects(from, to) {
			prefixes = append(prefixes, prefix)
		}
	}
	snapshot.streams[observationSchedulerStream] = prefixes
	records := make([]SchedulerObservation, 0, limit)
	matched, next := 0, -1
	err = snapshot.scanRaw(observationSchedulerStream, func(window string, raw []byte) (bool, error) {
		var envelope struct {
			SchemaVersion string `json:"schemaVersion"`
			Window        string `json:"window"`
			Scheduler     *struct {
				At time.Time `json:"at"`
			} `json:"scheduler"`
		}
		if err := jsoniter.ConfigCompatibleWithStandardLibrary.Unmarshal(raw, &envelope); err != nil {
			return false, err
		}
		if envelope.SchemaVersion != observationSchema || envelope.Window != window {
			return false, errObservationRecordIdentity
		}
		if envelope.Scheduler == nil || !observationInInterval(envelope.Scheduler.At, from, to) {
			return false, observationJSONSyntaxError(raw)
		}
		matched++
		if matched <= offset {
			return false, observationJSONSyntaxError(raw)
		}
		if len(records) == limit {
			next = offset + len(records)
			return true, observationJSONSyntaxError(raw)
		}
		var record observationRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return false, err
		}
		records = append(records, *record.Scheduler)
		return false, nil
	})
	if err != nil {
		return nil, -1, err
	}
	if offset > matched {
		return nil, -1, failure("InvalidQuery", "cursor exceeds selected observation count")
	}
	return records, next, nil
}
