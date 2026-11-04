package state

import "github.com/lab-paper-code/chill/internal/serving"

// UnconfirmedFrontier classifies a dispatch sweep, not elapsed time. Unknown
// dependencies, runnable work and terminal failures are not confirmation loss.
// completed includes only exact receipts and existing omitted-route proofs;
// blocked contains only preconditions actually refused by that sweep.
func UnconfirmedFrontier(actions []serving.Action, completed map[string]bool, blocked map[string]string) string {
	byID := make(map[string]serving.Action, len(actions))
	for _, action := range actions {
		byID[action.ID] = action
	}
	visiting := map[string]bool{}
	frontiers := map[string]string{}
	var frontier func(string) (string, bool)
	frontier = func(id string) (string, bool) {
		if completed[id] {
			return "", true
		}
		if reason := frontiers[id]; reason != "" {
			return reason, true
		}
		action, exists := byID[id]
		if !exists || visiting[id] {
			return "", false
		}
		visiting[id] = true
		defer delete(visiting, id)
		reason := ""
		for _, dependency := range action.DependsOn {
			why, known := frontier(dependency)
			if !known {
				return "", false
			}
			if why != "" {
				reason = why
			}
		}
		if reason != "" {
			frontiers[id] = reason
			return reason, true
		}
		why := blocked[id]
		if why != "" {
			frontiers[id] = why
		}
		return why, why != ""
	}
	reason := ""
	for _, action := range actions {
		if completed[action.ID] {
			continue
		}
		why, known := frontier(action.ID)
		if !known || why == "" {
			return ""
		}
		reason = why
	}
	return reason
}
