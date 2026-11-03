package kube

import (
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	"slices"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// annotateHistory sets each captured instance's UnroutedSince and HeldUntil
// from the controller's records and drops records of instances that left.
// A restart starts the records again, so an instance waits one more window.
func (c *SchedulerController) annotateHistory(s *serving.SchedulerSnapshot, window time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unrouted == nil {
		c.unrouted, c.overloaded = map[string]time.Time{}, map[string]time.Time{}
	}
	for key, until := range c.overloaded {
		if !until.IsZero() && !s.At.Before(until) {
			delete(c.overloaded, key)
		}
	}
	present := map[string]bool{}
	for k := range s.Instances {
		i := &s.Instances[k]
		present[i.ID] = true
		routed := false
		for _, weights := range s.Routing {
			routed = routed || weights[i.ID] > 0
		}
		if _, seen := c.unrouted[i.ID]; routed {
			delete(c.unrouted, i.ID)
		} else if !seen {
			c.unrouted[i.ID] = s.At
		}
		if since, ok := c.unrouted[i.ID]; ok {
			i.UnroutedSince = &since
		}
		for _, key := range []string{i.ID, i.TargetID} {
			until, ok := c.overloaded[key]
			if key == "" || !ok {
				continue
			}
			if until.IsZero() && i.Ready {
				until = s.At.Add(window)
				c.overloaded[key] = until
			}
			if !until.IsZero() {
				i.HeldUntil = &until
			}
		}
	}
	for id := range c.unrouted {
		if !present[id] {
			delete(c.unrouted, id)
		}
	}
	// Marks remain while any owner still holds the corresponding preparation.
	for key, until := range c.overloaded {
		preparing := false
		for _, r := range c.runs {
			preparing = preparing || r.action.Kind == servingstate.OperationPrepare && r.action.Instance == key
		}
		if until.IsZero() && !preparing {
			delete(c.overloaded, key)
		}
	}
}

// markOverloadPrepares records the slots a decision prepares while keep is in
// Overload; once Ready they are held for one observation window.
func (c *SchedulerController) markOverloadPrepares(decision serving.SchedulerDecision) {
	if decision.Target == nil || decision.Keep.Status != serving.SchedulerStatusOverload {
		return
	}
	if c.overloaded == nil {
		c.unrouted, c.overloaded = map[string]time.Time{}, map[string]time.Time{}
	}
	for _, a := range decision.Target.Actions {
		if _, marked := c.overloaded[a.Instance]; a.Kind == servingstate.OperationPrepare && !marked {
			c.overloaded[a.Instance] = time.Time{}
		}
	}
}

// reconcileSnapshot preserves each owner's frozen DAG and reservation independently.
func (c *SchedulerController) reconcileSnapshot(snapshot serving.SchedulerSnapshot, now time.Time, observationWindow time.Duration) (serving.SchedulerSnapshot, []servingstate.OperationResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var lost []servingstate.OperationResult
	settings := c.config.Settings
	gap := !c.replacementObservedAt.IsZero() && (now.Before(c.replacementObservedAt) || now.Sub(c.replacementObservedAt) > settings.MaxObservationAge+settings.ControlInterval)
	if gap {
		c.replacements = nil
	}
	c.replacementObservedAt = now
	// Forget replaced UIDs/profiles and missing or unhealthy observations.
	c.replacements = slices.DeleteFunc(c.replacements, func(r serving.SchedulerReplacement) bool {
		return !slices.ContainsFunc(snapshot.Instances, func(i serving.SchedulerInstance) bool {
			return i.Node.UID == r.NodeUID && i.WorkloadUID == r.WorkloadUID && i.Profile != nil &&
				i.Profile.ID == r.ProfileID && i.Ready && !i.Stopped
		})
	})
	c.freezePreparingExecutorsLocked(snapshot)
	lost = c.reconcileOwnersLocked(snapshot, now, gap)
	cutoff := now.Add(-observationWindow)
	c.state.Results = slices.DeleteFunc(c.state.Results, func(r servingstate.OperationResult) bool { return r.EndedAt.Before(cutoff) })
	for id, run := range c.runs {
		if !run.reserved && run.action.Status != servingstate.NativeOperationRunning && run.action.Status != servingstate.NativeOperationCancel && run.result != nil && run.result.EndedAt.Before(cutoff) && !c.runReferencedLocked(id) {
			delete(c.runs, id)
		}
	}
	snapshot.Actions = c.actionsLocked(now)
	snapshot.Reservations = c.reservationsLocked()
	snapshot.Replacements = slices.Clone(c.replacements)
	return snapshot, lost
}

func (c *SchedulerController) freezePreparingExecutorsLocked(snapshot serving.SchedulerSnapshot) {
	// API-confirmed RestartPolicyNever Pod identity permits one late container
	// binding even after failure. Slot/name discovery is not creation evidence.
	for _, run := range c.runs {
		if (run.action.Kind != servingstate.OperationPrepare && run.action.Kind != servingstate.OperationResize) || run.prepareIdentity != "" || run.result == nil || run.result.Operation.Prepare == nil {
			continue
		}
		wanted := run.result.Operation.Prepare.Instance
		evidence := run.result.Preparation
		if evidence == nil || run.result.Operation.Prepare.Owner == nil ||
			run.result.Operation.Prepare.Owner.UID != wanted.WorkloadUID {
			continue
		}
		for _, actual := range snapshot.Instances {
			podUID, containerID, exact := strings.Cut(actual.ID, "/")
			if wanted.Profile != nil && actual.Profile != nil && actual.Profile.ID == wanted.Profile.ID &&
				actual.TargetID == wanted.ID && actual.Node == wanted.Node && actual.OwnerWorkloadUID == wanted.WorkloadUID &&
				actual.PodUID != "" && exact && actual.PodUID == podUID && containerID != "" {
				if evidence.PodUID != actual.PodUID || evidence.NodeUID != actual.Node.UID ||
					evidence.DecisionID != run.result.Operation.Prepare.Config.Policy.DecisionID {
					continue
				}
				run.prepareIdentity = actual.ID
				run.scope.Pods = append(run.scope.Pods, actual.PodUID)
				run.scope.Instances = append(run.scope.Instances, actual.ID)
				if parent := c.returnProofParent(run); parent != nil {
					parent.returnObserved = false
					parent.scope.Pods = append(parent.scope.Pods, actual.PodUID)
					parent.scope.Instances = append(parent.scope.Instances, actual.ID)
				}
				break
			}
		}
	}
}

func (c *SchedulerController) reconcileOwnersLocked(snapshot serving.SchedulerSnapshot, now time.Time, gap bool) []servingstate.OperationResult {
	var lost []servingstate.OperationResult
	for _, parent := range c.runs {
		if parent.action.Kind != transitionRunKind || parent.result == nil {
			continue
		}
		if parent.action.Status != servingstate.NativeOperationRunning {
			// Adopt only the fresh physical-return/known-effect proof. Keep the
			// failure receipt and each child's still-unconfirmed responsibility.
			if parent.action.Status != servingstate.NativeOperationCancel && parent.returnObserved {
				parent.reserved = false
			}
			continue
		}
		target := parent.result.Operation.Target
		failedEffects, failed := c.failTargetLocked(snapshot, target, now)
		lost = append(lost, failedEffects...)
		if failed {
			parent.action.Status = servingstate.NativeOperationFailed
			parent.result.Status, parent.result.EndedAt = servingstate.NativeOperationFailed, now
			continue
		}
		if c.ownerDAGCompleteLocked(parent) {
			parent.action.Status = servingstate.NativeOperationComplete
			parent.result.Status, parent.result.EndedAt = servingstate.NativeOperationComplete, now
			if !gap {
				for _, r := range parent.replacements {
					if slices.ContainsFunc(snapshot.Instances, func(i serving.SchedulerInstance) bool {
						return i.Node.UID == r.NodeUID && i.WorkloadUID == r.WorkloadUID && i.Profile != nil &&
							i.Profile.ID == r.ProfileID && i.Ready && !i.Stopped
					}) {
						r.CompletedAt = now
						c.replacements = slices.DeleteFunc(c.replacements, func(old serving.SchedulerReplacement) bool {
							return old.NodeUID == r.NodeUID && old.WorkloadUID == r.WorkloadUID
						})
						c.replacements = append(c.replacements, r)
					}
				}
			}
			c.releaseOwnerLocked(parent.owner)
		}
	}
	return lost
}

// failTargetLocked checks only this owner's DAG; terminal receipts beat stale plans.
func (c *SchedulerController) failTargetLocked(snapshot serving.SchedulerSnapshot, goal *serving.SchedulerCandidate, now time.Time) ([]servingstate.OperationResult, bool) {
	var lost []servingstate.OperationResult
	failed := false
	for _, desired := range goal.Actions {
		if desired.From == readySubsetRoute || desired.From == externalActionSource {
			continue
		}
		run := c.runs[desired.ID]
		if run == nil || run.action.Status == servingstate.NativeOperationRunning || run.action.Status == servingstate.NativeOperationCancel || run.result == nil {
			continue
		}
		// Fixed lowering is undone by the goal's own raise of the same Node.
		if desired.RouteTarget == nil && !desired.Fixed && run.result.Status == servingstate.NativeOperationComplete && !c.actionEffectPresent(snapshot, run) && !c.actionEffectUnknown(snapshot, run) {
			result := *run.result
			result.Status, result.Error, result.EndedAt = servingstate.NativeOperationFailed, "completed effect lost", now
			run.result, run.action.Status, run.action.Reason = &result, servingstate.NativeOperationFailed, result.Error
			lost = append(lost, result)
		}
		failed = failed || run.result.Status != servingstate.NativeOperationComplete
	}
	return lost, failed
}
