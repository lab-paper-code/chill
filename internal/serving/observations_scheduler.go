package serving

import (
	"encoding/json"
	"sort"
	"time"

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
	return j.append("scheduler", observationRecord{Scheduler: &event})
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
// ponytail: scan retained JSONL; add a window index if campaign history becomes
// a measured bottleneck. Request fact allocation is limited to this receipt window.
func (j *Journal) LoadWindow(from, to time.Time) (LoadWindow, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := LoadWindow{From: from, To: to, Duration: to.Sub(from), Population: "all ingress receipt events; computable demand separately marked after authorization and input validation; event-time [from,to); one request = one input"}
	if from.IsZero() || !from.Before(to) {
		return result, failure("InvalidQuery", "positive explicit load window is required")
	}
	definitions := map[string]*Definition{}
	if err := j.scan("definitions", func(record observationRecord) {
		if record.Definition != nil {
			definitions[record.Definition.Identity] = record.Definition.Definition
		}
	}); err != nil {
		j.lastError = err
		return result, err
	}
	loads := map[string]*WorkloadLoad{}
	facts := map[string]*windowLoadFact{}
	if err := j.scan("requests", func(record observationRecord) {
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
		j.lastError = err
		return result, err
	}
	for id, fact := range facts {
		if observationInInterval(fact.received, from, to) {
			arrival := SchedulerArrival{ID: id, At: fact.received, WorkloadUID: fact.uid, Definition: definitions[fact.definition], InputPath: fact.inputPath, Lifetime: fact.lifetime, Accepted: fact.accepted, Compute: fact.compute, Outcome: fact.outcome}
			result.Arrivals = append(result.Arrivals, arrival)
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
	for _, window := range j.windows {
		if window.Incomplete && window.StartedAt.Before(to) && window.EndsAt.After(from) {
			result.IncompleteWindows = append(result.IncompleteWindows, window.ID)
		}
	}
	return result, nil
}

func (j *Journal) SchedulerArrivals(from, to time.Time) ([]SchedulerArrival, error) {
	window, err := j.LoadWindow(from, to)
	return window.Arrivals, err
}

// SchedulerObservationPage uses retained append order and bounds decoded output.
// Cursor offsets apply to the selected event window, independently of archives.
func (j *Journal) SchedulerObservationPage(from, to time.Time, limit, offset int) ([]SchedulerObservation, int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if from.IsZero() || !from.Before(to) || limit <= 0 || offset < 0 {
		return nil, -1, failure("InvalidQuery", "positive window, bounded limit and nonnegative cursor are required")
	}
	records := make([]SchedulerObservation, 0, limit)
	matched := 0
	err := j.scan("scheduler", func(record observationRecord) {
		if record.Scheduler == nil || !observationInInterval(record.Scheduler.At, from, to) {
			return
		}
		if matched >= offset && len(records) < limit {
			records = append(records, *record.Scheduler)
		}
		matched++
	})
	if err != nil {
		j.lastError = err
		return nil, -1, err
	}
	if offset > matched {
		return nil, -1, failure("InvalidQuery", "cursor exceeds selected observation count")
	}
	next := -1
	if matched-offset > len(records) {
		next = offset + len(records)
	}
	return records, next, nil
}
