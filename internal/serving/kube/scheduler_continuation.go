package kube

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// Completions coalesces action outcomes; receiving is not proof of success.
func (c *SchedulerController) Completions() <-chan struct{} { return c.completions }

// Continue advances only already admitted owners. It shares Tick's serialization
// with configuration changes, but never evaluates or admits another candidate.
func (c *SchedulerController) Continue(ctx context.Context, now time.Time) error {
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	if err := ctx.Err(); err != nil { return err }
	c.mu.Lock()
	config := c.config
	c.mu.Unlock()
	snapshot, err := c.capture(ctx, now, config)
	if err != nil { return err }
	if err := ctx.Err(); err != nil { return err }
	if reasons := serving.SchedulerContinuationReasons(config.Settings, snapshot); len(reasons) != 0 {
		return fmt.Errorf("continuation observation unavailable: %s", strings.Join(reasons, ", "))
	}
	snapshot, lost := c.reconcileSnapshot(snapshot, snapshot.At, config.Settings.ObservationWindow)
	for _, result := range lost {
		if config.OnAction != nil {
			if err := config.OnAction(result); err != nil { return fmt.Errorf("record lost action effect: %w", err) }
		}
	}
	c.annotateHistory(&snapshot, config.Settings.ObservationWindow)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil { return err }
	c.state.Snapshot = snapshot
	c.launchRunningParentsLocked(config, true)
	return nil
}

func (c *SchedulerController) launchRunningParentsLocked(config SchedulerControllerConfig, continuation bool) []*actionRun {
	parents := []*actionRun{}
	for _, run := range c.runs {
		if run.action.Kind == transitionRunKind && run.action.Status == nativeOperationRunning {
			parents = append(parents, run)
		}
	}
	slices.SortFunc(parents, func(a, b *actionRun) int { return strings.Compare(a.owner, b.owner) })
	for _, parent := range parents {
		if continuation {
			if parent.admittedControls != config.Settings.Controls { continue }
			if slices.ContainsFunc(parent.result.Operation.WorkloadUIDs, func(uid string) bool {
				return !slices.Contains(config.Settings.AutonomousWorkloads, uid)
			}) { continue }
		}
		c.launchTargetLocked(*parent.result.Operation.Target, config)
	}
	return parents
}
