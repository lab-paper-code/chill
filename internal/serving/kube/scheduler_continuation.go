package kube

import (
	"context"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// Completions coalesces action outcomes; receiving is not proof of success.
func (c *SchedulerController) Completions() <-chan struct{} { return c.completions }

// Continue advances only already admitted owners. It shares Tick's serialization
// with configuration changes, but never evaluates or admits another candidate.
func (c *SchedulerController) Continue(ctx context.Context, now time.Time) error {
	c.LockTick()
	defer c.UnlockTick()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.LockState()
	config := c.config
	c.UnlockState()
	snapshot, err := c.captureReconciled(ctx, now, config, true)
	if err != nil {
		return err
	}
	c.LockState()
	defer c.UnlockState()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.PublishSnapshotLocked(&snapshot)
	c.ContinueTransitionsLocked(config.Settings, true, func(target serving.SchedulerCandidate) {
		c.launchTargetLocked(target, config)
	})
	return nil
}

