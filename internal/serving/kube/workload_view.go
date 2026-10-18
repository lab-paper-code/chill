package kube

import (
	"context"
	"sort"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
)

// SupportedInput is measured support of one execution path for the actual
// applied definition, not a claim that an executor is ready. The raw input and
// output contract is the applied definition's preprocessor input and output.
type SupportedInput struct {
	ExecutionPath string             `json:"executionPath"`
	Artifact      string             `json:"artifact"`
	Profiles      []SupportedProfile `json:"profiles"`
}

type SupportedProfile struct {
	ID        string                         `json:"id"`
	Reference evidencestore.ObjectIdentity   `json:"reference"`
	Evidence  []evidencestore.ObjectIdentity `json:"evidence"`
	Condition derivation.CurvePlanSpec       `json:"condition"`
}

type WorkloadView struct {
	Desired         *api.Workload          `json:"desired"`
	Applied         *serving.Definition    `json:"appliedDefinition,omitempty"`
	SupportedInputs []SupportedInput       `json:"supportedInputs"`
	Admission       bool                   `json:"admission"`
	Readiness       string                 `json:"readiness"`
	Reason          string                 `json:"reason"`
	Pending         int                    `json:"pending"`
	Assigned        int                    `json:"assigned"`
	Operation       *api.WorkloadOperation `json:"operation,omitempty"`
	ObservedAt      time.Time              `json:"observedAt"`
}

func (m *Manager) view(w *api.Workload) WorkloadView {
	status := m.status(w)
	view := WorkloadView{Desired: w.DeepCopy(), SupportedInputs: []SupportedInput{}, Operation: status.Operation,
		Readiness: "Waiting", Reason: "AppliedDefinitionUnavailable", ObservedAt: time.Now().UTC()}
	state, err := m.runtime.State(w.Name)
	if err == nil && state.Applied != nil && state.Applied.UID == string(w.UID) {
		view.Applied, view.Admission, view.Reason = state.Applied, state.Admission, state.Reason
		view.Pending, view.Assigned, view.ObservedAt = state.Pending, state.Assigned, state.ObservedAt
		switch state.Reason {
		case "Ready":
			view.Readiness = "Ready"
		case "Preparing":
			view.Readiness = "Preparing"
		}
		profiles := m.observer.Profiles()
		for _, path := range state.Applied.Model.Paths {
			input := SupportedInput{ExecutionPath: path.Name, Artifact: path.Artifact, Profiles: []SupportedProfile{}}
			for i := range profiles {
				profile := &profiles[i]
				if profile.Condition.Artifact.ExecutionPath == path.Name && serving.MatchesProfile(state.Applied, profile) {
					input.Profiles = append(input.Profiles, SupportedProfile{ID: profile.ID, Reference: profile.Reference, Evidence: profile.Evidence, Condition: profile.Condition})
				}
			}
			if len(input.Profiles) != 0 {
				view.SupportedInputs = append(view.SupportedInputs, input)
			}
		}
	}
	if active(status.Operation) {
		view.Readiness = "Changing"
		if status.Operation.Kind == "Delete" {
			view.Readiness = "Deleting"
		}
	} else if status.Operation != nil && status.Operation.Phase == "Failed" {
		view.Readiness, view.Reason, view.Admission = "Failed", status.Operation.Reason, false
	}
	return view
}

func (m *Manager) ListWorkloads(ctx context.Context) ([]WorkloadView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list, err := m.store.ListWorkloads(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]WorkloadView, len(list.Items))
	for i := range list.Items {
		views[i] = m.view(&list.Items[i])
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Desired.Name < views[j].Desired.Name })
	return views, nil
}

func (m *Manager) GetWorkload(ctx context.Context, name string) (WorkloadView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, err := m.store.GetWorkload(ctx, name)
	if err != nil {
		return WorkloadView{}, err
	}
	return m.view(w), nil
}
