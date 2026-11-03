package state

import (
	serving "github.com/lab-paper-code/chill/internal/serving"
	slices "slices"
	strings "strings"
)

func (c *ReservationState) ContinueTransitionsLocked(
	settings serving.SchedulerSettings, continuation bool, dispatch func(serving.SchedulerCandidate),
) {
	parents := []*ActionRun{}
	for _, run := range c.runs {
		if run.action.Kind == TransitionRunKind && run.action.Status == NativeOperationRunning {
			parents = append(parents, run)
		}
	}
	slices.SortFunc(parents, func(a, b *ActionRun) int { return strings.Compare(a.owner, b.owner) })
	for _, parent := range parents {
		if continuation {
			if parent.admittedControls != settings.Controls {
				continue
			}
			if slices.ContainsFunc(parent.result.Operation.WorkloadUIDs, func(uid string) bool {
				return !slices.Contains(settings.AutonomousWorkloads, uid)
			}) {
				continue
			}
		}
		dispatch(*parent.result.Operation.Target)
	}
}
