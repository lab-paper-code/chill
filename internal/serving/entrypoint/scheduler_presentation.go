package entrypoint

import (
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

// Compact reads share only the two large repeated evidence objects. Everything
// else, including policy, candidate order and action acknowledgements, is intact.
// These references belong to this response, not to the stored journal contract.
type schedulerEvidenceRef struct {
	Ref string `json:"$ref"`
}

type schedulerReadEvidence struct {
	Profiles               map[string]*serving.Profile          `json:"profiles"`
	CompletionMeasurements map[string]*serving.ObservedOverhead `json:"completionMeasurements"`
}

func newSchedulerReadEvidence() schedulerReadEvidence {
	return schedulerReadEvidence{
		Profiles:               make(map[string]*serving.Profile),
		CompletionMeasurements: make(map[string]*serving.ObservedOverhead),
	}
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

func (e *schedulerReadEvidence) completion(m *serving.ObservedOverhead) *schedulerEvidenceRef {
	if m == nil {
		return nil
	}
	identity := m.Profile + "@" + m.Node.UID + "@" + m.From.Format(time.RFC3339Nano) + "/" + m.To.Format(time.RFC3339Nano)
	key := identity
	for variant := 0; ; variant++ {
		if previous, exists := e.CompletionMeasurements[key]; !exists {
			e.CompletionMeasurements[key] = m
			return &schedulerEvidenceRef{Ref: key}
		} else if previous == m || reflect.DeepEqual(previous, m) {
			return &schedulerEvidenceRef{Ref: key}
		}
		key = identity + "#" + strconv.Itoa(variant+1)
	}
}

type schedulerReadInstance struct {
	*serving.SchedulerInstance
	Profile               *schedulerEvidenceRef `json:"profile"`
	CompletionMeasurement *schedulerEvidenceRef `json:"completionMeasurement,omitempty"`
}

func (e *schedulerReadEvidence) instance(instance *serving.SchedulerInstance) schedulerReadInstance {
	return schedulerReadInstance{instance, e.profile(instance.Profile), e.completion(instance.CompletionMeasurement)}
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
	Keep       *schedulerReadCandidate  `json:"keep"`
	Target     *schedulerReadCandidate  `json:"target"`
	Candidates []schedulerReadCandidate `json:"candidates"`
}

func (e *schedulerReadEvidence) decision(decision *serving.SchedulerDecision) *schedulerReadDecision {
	if decision == nil {
		return nil
	}
	result := &schedulerReadDecision{SchedulerDecision: decision, Keep: e.candidate(&decision.Keep), Target: e.candidate(decision.Target)}
	if decision.Candidates != nil {
		result.Candidates = make([]schedulerReadCandidate, len(decision.Candidates))
		for i := range decision.Candidates {
			result.Candidates[i] = schedulerReadCandidate{&decision.Candidates[i], e.instances(decision.Candidates[i].Instances)}
		}
	}
	return result
}

type schedulerReadOption struct {
	*serving.SchedulerOption
	Instance schedulerReadInstance `json:"instance"`
}

type schedulerReadSnapshot struct {
	*serving.SchedulerSnapshot
	Instances        []schedulerReadInstance `json:"instances"`
	Options          []schedulerReadOption   `json:"options"`
	InProgressTarget *schedulerReadCandidate `json:"inProgressTarget,omitempty"`
}

func (e *schedulerReadEvidence) snapshot(snapshot *serving.SchedulerSnapshot) *schedulerReadSnapshot {
	if snapshot == nil {
		return nil
	}
	result := &schedulerReadSnapshot{SchedulerSnapshot: snapshot, Instances: e.instances(snapshot.Instances), InProgressTarget: e.candidate(snapshot.InProgressTarget)}
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

func (e *schedulerReadEvidence) state(state *servingkube.SchedulerControllerState) schedulerReadState {
	return schedulerReadState{state, e.snapshot(&state.Snapshot), e.decision(&state.Decision)}
}

type schedulerReadObservation struct {
	*serving.SchedulerObservation
	Decision *schedulerReadDecision `json:"decision,omitempty"`
	Snapshot *schedulerReadSnapshot `json:"snapshot,omitempty"`
}

func (e *schedulerReadEvidence) observations(records []serving.SchedulerObservation) []schedulerReadObservation {
	result := make([]schedulerReadObservation, len(records))
	for i := range records {
		result[i] = schedulerReadObservation{&records[i], e.decision(records[i].Decision), e.snapshot(records[i].Snapshot)}
	}
	return result
}

func schedulerCompactQuery(r *http.Request) (bool, error) {
	views := r.URL.Query()["view"]
	if len(views) == 0 {
		return false, nil
	}
	if len(views) == 1 && views[0] == "compact" {
		return true, nil
	}
	return false, &serving.Error{Code: "InvalidQuery", Message: "scheduler view must be compact or omitted"}
}
