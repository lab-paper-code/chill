package state

import (
	serving "github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	slices "slices"
	time "time"
)

// NodeBusy reports a running run on the Node; Tick, Admit and hand-back then
// leave the Node to that run.
func (c *ReservationState) NodeBusy(node *core.Node) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.runs {
		if r.reserved && slices.Contains(r.scope.Nodes, serving.NodeReference{Name: node.Name, UID: string(node.UID)}) {
			return true
		}
	}
	return false
}

func (c *ReservationState) ReserveMeasurementLease(operation Operation) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if run := c.runs[operation.ID]; run != nil && run.reserved {
		return true
	}
	now := time.Now()
	run := &ActionRun{owner: operation.Holder, action: serving.Action{ID: operation.ID, Kind: MeasurementLeaseKind, Node: operation.Node, Status: NativeOperationRunning}, result: &OperationResult{Operation: operation, StartedAt: now, Status: NativeOperationRunning}, started: now}
	return c.ReserveRun(operation.ID, run, operation)
}

func (c *ReservationState) FailMeasurementLease(holder string, err error) {
	c.mu.Lock()
	lease := c.runs[MeasurementLeaseKind+"/"+holder]
	lease.action.Status = NativeOperationFailed
	lease.result.EndedAt, lease.result.Error = time.Now(), err.Error()
	c.mu.Unlock()
}
