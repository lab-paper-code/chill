package httpapi

import (
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

const schedulerSummaryView = "summary"

// Compact history shares repeated profile evidence without changing it.
// Current-state reads additionally project raw latency populations to counts.
// These response-local references never change the stored observation contract.
type schedulerEvidenceRef struct {
	Ref string `json:"$ref"`
}

type schedulerReadEvidence struct {
	Profiles map[string]any `json:"profiles"`
}

func newSchedulerReadEvidence() schedulerReadEvidence {
	return schedulerReadEvidence{Profiles: make(map[string]any)}
}

func (e *schedulerReadEvidence) profile(p *serving.Profile) *schedulerEvidenceRef {
	if p == nil {
		return nil
	}
	key := p.ID
	for variant := 0; ; variant++ {
		if previous, exists := e.Profiles[key]; !exists {
			e.Profiles[key] = p
			return &schedulerEvidenceRef{Ref: key}
		} else if previous == p || reflect.DeepEqual(previous, p) {
			return &schedulerEvidenceRef{Ref: key}
		}
		// Same logical identity with different retained quantities is not merged.
		key = p.ID + "#" + strconv.Itoa(variant+1)
	}
}

type schedulerReadInstance struct {
	*serving.SchedulerInstance
	Profile *schedulerEvidenceRef `json:"profile"`
}

func (e *schedulerReadEvidence) instance(instance *serving.SchedulerInstance) schedulerReadInstance {
	return schedulerReadInstance{instance, e.profile(instance.Profile)}
}

func (e *schedulerReadEvidence) instances(instances []serving.SchedulerInstance) []schedulerReadInstance {
	if instances == nil {
		return nil
	}
	result := make([]schedulerReadInstance, len(instances))
	for i := range instances {
		result[i] = e.instance(&instances[i])
	}
	return result
}

type schedulerReadCandidate struct {
	*serving.SchedulerCandidate
	Instances []schedulerReadInstance `json:"instances"`
}

func (e *schedulerReadEvidence) candidate(candidate *serving.SchedulerCandidate) *schedulerReadCandidate {
	if candidate == nil {
		return nil
	}
	return &schedulerReadCandidate{candidate, e.instances(candidate.Instances)}
}

type schedulerReadDecision struct {
	*serving.SchedulerDecision
	Keep   *schedulerReadCandidate `json:"keep"`
	Target *schedulerReadCandidate `json:"target"`
}

func (e *schedulerReadEvidence) decision(decision *serving.SchedulerDecision) *schedulerReadDecision {
	if decision == nil {
		return nil
	}
	return &schedulerReadDecision{SchedulerDecision: decision, Keep: e.candidate(&decision.Keep), Target: e.candidate(decision.Target)}
}

type schedulerReadOption struct {
	*serving.SchedulerOption
	Instance schedulerReadInstance `json:"instance"`
}

type schedulerReadSnapshot struct {
	*serving.SchedulerSnapshot
	Instances    []schedulerReadInstance    `json:"instances"`
	Options      []schedulerReadOption      `json:"options"`
	Reservations []schedulerReadReservation `json:"reservations,omitempty"`
	ArrivalCount *int                       `json:"arrivalCount,omitempty"`
	OptionCount  *int                       `json:"optionCount,omitempty"`
}

type schedulerReadReservation struct {
	*serving.SchedulerReservation
	Target *schedulerReadCandidate `json:"target,omitempty"`
}

func (e *schedulerReadEvidence) snapshot(snapshot *serving.SchedulerSnapshot) *schedulerReadSnapshot {
	if snapshot == nil {
		return nil
	}
	result := &schedulerReadSnapshot{SchedulerSnapshot: snapshot, Instances: e.instances(snapshot.Instances)}
	if snapshot.Reservations != nil {
		result.Reservations = make([]schedulerReadReservation, len(snapshot.Reservations))
		for i := range snapshot.Reservations {
			reservation := &snapshot.Reservations[i]
			result.Reservations[i] = schedulerReadReservation{reservation, e.candidate(reservation.Target)}
		}
	}
	if snapshot.Options != nil {
		result.Options = make([]schedulerReadOption, len(snapshot.Options))
		for i := range snapshot.Options {
			result.Options[i] = schedulerReadOption{&snapshot.Options[i], e.instance(&snapshot.Options[i].Instance)}
		}
	}
	return result
}

type schedulerReadState struct {
	*servingkube.SchedulerControllerState
	Snapshot *schedulerReadSnapshot `json:"snapshot"`
	Decision *schedulerReadDecision `json:"decision"`
}

// Current operational reads need the measurement identity, conditions, counts
// and scalar resource/energy facts, not every empirical latency sample. Exact
// historical evidence and the unprojected REST read retain the raw population.
type schedulerReadProfile struct {
	*serving.Profile
	Batches []schedulerReadBatch `json:"batches"`
}

type schedulerReadBatch struct {
	*serving.BatchProfile
	DurationNanos       []float64 `json:"durationNanos,omitempty"`
	DurationSampleCount int       `json:"durationSampleCount"`
}

func (e *schedulerReadEvidence) state(state *servingkube.SchedulerControllerState) schedulerReadState {
	// The current dashboard reads neither the arrival log nor catalog options.
	// Keep their counts, without registering profiles reachable only from options.
	snapshot := state.Snapshot
	snapshot.Arrivals = nil
	snapshot.Options = nil
	projected := schedulerReadState{state, e.snapshot(&snapshot), e.decision(&state.Decision)}
	arrivals, options := len(state.Snapshot.Arrivals), len(state.Snapshot.Options)
	projected.Snapshot.ArrivalCount = &arrivals
	projected.Snapshot.OptionCount = &options
	for key, value := range e.Profiles {
		profile := value.(*serving.Profile)
		batches := make([]schedulerReadBatch, len(profile.Batches))
		for i := range profile.Batches {
			batch := &profile.Batches[i]
			batches[i] = schedulerReadBatch{BatchProfile: batch, DurationSampleCount: len(batch.DurationNanos)}
		}
		e.Profiles[key] = schedulerReadProfile{profile, batches}
	}
	return projected
}

type schedulerReadObservation struct {
	*observation.SchedulerObservation
	Decision *schedulerReadDecision `json:"decision,omitempty"`
	Snapshot *schedulerReadSnapshot `json:"snapshot,omitempty"`
}

func (e *schedulerReadEvidence) observations(records []observation.SchedulerObservation) []schedulerReadObservation {
	result := make([]schedulerReadObservation, len(records))
	for i := range records {
		result[i] = schedulerReadObservation{&records[i], e.decision(records[i].Decision), e.snapshot(records[i].Snapshot)}
	}
	return result
}

// History lists carry identity and row labels only; exact-record reads retain
// the complete payload. This projection never changes the journal/catalog.
type schedulerHistoryDecision struct {
	At     time.Time `json:"at"`
	Status string    `json:"status"`
}

type schedulerHistorySnapshot struct {
	At time.Time `json:"at"`
}

type schedulerHistorySettings struct {
	ActorUsername string     `json:"actorUsername"`
	ActorUID      string     `json:"actorUID"`
	AppliedAt     *time.Time `json:"appliedAt,omitempty"`
	Error         string     `json:"error,omitempty"`
}

type schedulerHistoryAction struct {
	ID        string                `json:"id"`
	Kind      string                `json:"kind"`
	Node      serving.NodeReference `json:"node"`
	Phase     string                `json:"phase"`
	StartedAt time.Time             `json:"startedAt"`
}

type schedulerHistoryPower struct {
	StartedAt time.Time `json:"startedAt"`
}

type schedulerHistoryObservation struct {
	At          time.Time                 `json:"at"`
	Offset      *int                      `json:"offset,omitempty"`
	Decision    *schedulerHistoryDecision `json:"decision,omitempty"`
	Snapshot    *schedulerHistorySnapshot `json:"snapshot,omitempty"`
	Settings    *schedulerHistorySettings `json:"settings,omitempty"`
	Action      *schedulerHistoryAction   `json:"action,omitempty"`
	Power       *schedulerHistoryPower    `json:"power,omitempty"`
	Node        *serving.NodeReference    `json:"node,omitempty"`
	PowerJoules *float64                  `json:"powerJoules,omitempty"`
}

func schedulerHistoryObservations(records []observation.SchedulerObservation) []schedulerHistoryObservation {
	result := make([]schedulerHistoryObservation, len(records))
	for i, record := range records {
		row := schedulerHistoryObservation{At: record.At, Offset: record.Offset, Node: record.Node, PowerJoules: record.PowerJoules}
		if d := record.Decision; d != nil {
			row.Decision = &schedulerHistoryDecision{d.At, d.Status}
		}
		if s := record.Snapshot; s != nil {
			row.Snapshot = &schedulerHistorySnapshot{s.At}
		}
		if s := record.Settings; s != nil {
			row.Settings = &schedulerHistorySettings{s.ActorUsername, s.ActorUID, s.AppliedAt, s.Error}
		}
		if a := record.Action; a != nil {
			row.Action = &schedulerHistoryAction{a.ID, a.Kind, a.Node, a.Phase, a.StartedAt}
		}
		if p := record.Power; p != nil {
			row.Power = &schedulerHistoryPower{p.StartedAt}
		}
		result[i] = row
	}
	return result
}

func schedulerCompactQuery(r *http.Request) (bool, error) {
	views := r.URL.Query()["view"]
	if len(views) == 0 {
		return false, nil
	}
	if len(views) == 1 && views[0] == schedulerSummaryView && (r.URL.Path == "/api/v1/scheduler/state" || r.URL.Path == "/api/v1/observations/scheduler") {
		return false, nil
	}
	if len(views) == 1 && views[0] == "compact" {
		return true, nil
	}
	return false, &serving.Error{Code: "InvalidQuery", Message: "scheduler view must be compact, summary (state/history only), or omitted"}
}
