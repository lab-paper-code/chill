package httpapi

import (
	"net/http"
	"reflect"
	"strconv"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

// Compact reads share only the large repeated profile evidence. Everything
// else, including policy, candidate order and action acknowledgements, is intact.
// These references belong to this response, not to the stored observation contract.
type schedulerEvidenceRef struct {
	Ref string `json:"$ref"`
}

type schedulerReadEvidence struct {
	Profiles map[string]*serving.Profile `json:"profiles"`
}

func newSchedulerReadEvidence() schedulerReadEvidence {
	return schedulerReadEvidence{Profiles: make(map[string]*serving.Profile)}
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
	if len(views) == 1 && views[0] == "summary" && r.URL.Path == "/api/v1/scheduler/state" {
		return false, nil
	}
	if len(views) == 1 && views[0] == "compact" {
		return true, nil
	}
	return false, &serving.Error{Code: "InvalidQuery", Message: "scheduler view must be compact, summary (state only), or omitted"}
}
