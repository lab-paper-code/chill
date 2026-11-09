package kube

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

// Completions coalesces action outcomes; receiving is not proof of success.
func (c *SchedulerController) Completions() <-chan struct{} { return c.completions }

// Continue advances only already admitted owners. It shares Tick's serialization
// with configuration changes, but never evaluates or admits another candidate.
func (c *SchedulerController) Continue(ctx context.Context, now time.Time) error {
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	config := c.config
	c.mu.Unlock()
	snapshot, err := c.captureReconciled(ctx, now, config, true)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Continuation observes paths but cannot activate another capacity group.
	snapshot.PathBudgetObservations, snapshot.PathBudgets = snapshot.PathBudgets, c.appliedPathBudgets
	c.publishSnapshotLocked(&snapshot)
	c.continueTransitionsLocked(config.Settings, true, func(target serving.SchedulerCandidate) {
		c.launchTargetLocked(target, config)
	})
	return nil
}

func (c *reservationState) continueTransitionsLocked(
	settings serving.SchedulerSettings, continuation bool, dispatch func(serving.SchedulerCandidate),
) {
	parents := []*actionRun{}
	for _, run := range c.runs {
		if run.action.Kind == transitionRunKind && (run.action.Status == servingstate.NativeOperationRunning ||
			run.action.Status == servingstate.NativeOperationFailed && run.reserved) {
			parents = append(parents, run)
		}
	}
	slices.SortFunc(parents, func(a, b *actionRun) int { return strings.Compare(a.owner, b.owner) })
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
		target := *parent.result.Operation.Target
		if parent.action.Status == servingstate.NativeOperationFailed {
			var completed []serving.Action
			for _, action := range target.Actions {
				run := c.runs[action.ID]
				if run != nil && run.owner == parent.owner && run.result != nil && run.result.Status == servingstate.NativeOperationComplete {
					completed = append(completed, run.action)
				}
			}
			target = servingstate.FailedStopCleanup(target, completed)
		}
		if len(target.Actions) > 0 {
			dispatch(target)
		}
	}
}
