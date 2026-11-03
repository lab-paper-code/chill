package state

import (
	serving "github.com/lab-paper-code/chill/internal/serving"
	slices "slices"
)

// Called both while planning and under mu at acquisition: no ownership changes
// until every conflicting owner's runs have passed the same containment check.
func (c *ReservationState) ResetReservationOwnersLocked(scope ResourceScope, admittedNodes []serving.NodeReference) ([]string, error) {
	var owners []string
	for _, run := range c.runs {
		if run.reserved && scope.Overlaps(run.scope) && !slices.Contains(owners, run.owner) {
			owners = append(owners, run.owner)
		}
	}
	for _, run := range c.runs {
		if !slices.Contains(owners, run.owner) {
			continue
		}
		if run.action.Status == NativeOperationRunning || run.action.Status == NativeOperationCancel {
			return nil, Refused("OperationConflict", "resources are reserved by active owner "+run.owner)
		}
		if !run.reserved {
			continue
		}
		if run.action.Kind == MeasurementLeaseKind || run.result == nil || run.result.EndedAt.IsZero() || run.result.Operation.Holder != "" ||
			len(run.scope.Workloads) == 0 ||
			slices.ContainsFunc(run.scope.Workloads, func(uid string) bool { return !slices.Contains(scope.Workloads, uid) }) ||
			slices.ContainsFunc(run.scope.Nodes, func(node serving.NodeReference) bool { return !slices.Contains(admittedNodes, node) }) {
			return nil, Refused("OperationConflict", "reset cannot inherit reservation outside its cleanup scope: "+run.owner)
		}
	}
	return owners, nil
}
