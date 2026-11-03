package main

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	ctrl "sigs.k8s.io/controller-runtime"
)

// Inputs and published slices are immutable after acceptance. The sole worker
// owns scanning/reduction; scheduler capture reads one atomic snapshot only.
type rollingOccupancyInput struct {
	configuration *schedulerConfiguration
	catalog       *httpapi.ProfilesView
	profiles      map[string]*serving.Profile
	legacy        []serving.DispatchOccupancy
}

type rollingOccupancySnapshot struct {
	input     *rollingOccupancyInput
	selected  []serving.DispatchOccupancy
	estimates []serving.OccupancyEstimate
}

type rollingOccupancyCache struct {
	mu        sync.Mutex
	published atomic.Pointer[rollingOccupancySnapshot]
	changed   chan struct{}
}

func (c *rollingOccupancyCache) configure(input *rollingOccupancyInput) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.changed == nil {
		c.changed = make(chan struct{}, 1)
	}
	// The first decision never waits for an observation scan.
	c.published.Store(&rollingOccupancySnapshot{input: input, selected: resolveOccupancy(input, nil)})
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

func (c *rollingOccupancyCache) selected() []serving.DispatchOccupancy {
	if snapshot := c.published.Load(); snapshot != nil {
		return snapshot.selected
	}
	return nil
}

func resolveOccupancy(input *rollingOccupancyInput, measured []serving.DispatchOccupancy) []serving.DispatchOccupancy {
	candidates := input.legacy
	if len(measured) != 0 {
		candidates = make([]serving.DispatchOccupancy, 0, len(measured)+len(input.legacy))
		candidates = append(candidates, measured...)
		candidates = append(candidates, input.legacy...)
	}
	batches := 0
	for _, profile := range input.profiles {
		batches += len(profile.Batches)
	}
	selected := make([]serving.DispatchOccupancy, 0, batches)
	// Catalog order, then profile batch order, makes publication deterministic.
	for _, profile := range input.catalog.Profiles {
		p := input.profiles[profile.ID]
		if p == nil {
			continue
		}
		for _, batch := range p.Batches {
			instance := serving.SchedulerInstance{
				Node:    serving.NodeReference{Name: p.NodeName, UID: p.Condition.NodeUID},
				Profile: p, MaxBatch: batch.Batch,
			}
			selected = append(selected, *serving.ResolveDispatchOccupancy(input.configuration.Settings, candidates, instance))
		}
	}
	return selected
}

func qualifiedOccupancy(
	estimates []serving.OccupancyEstimate, minimumPairs int32, updatedAt time.Time,
) []serving.DispatchOccupancy {
	measured := make([]serving.DispatchOccupancy, 0, len(estimates))
	for _, estimate := range estimates {
		if estimate.Pairs < int(minimumPairs) || estimate.Statistics == nil {
			continue
		}
		median := estimate.Statistics.Median
		if math.IsNaN(median) || math.IsInf(median, 0) || median >= float64(math.MaxInt64) {
			continue
		}
		value := int64(math.Round(max(median, 0)))
		window := estimate.Window
		measured = append(measured, serving.DispatchOccupancy{
			Node: estimate.Node, PowerMode: estimate.PowerMode, Model: estimate.Model,
			RuntimeImage: estimate.RuntimeImage, ProfileID: estimate.Profile.ProfileID,
			ExecutionPath: estimate.ExecutionPath, Batch: estimate.Batch, PerRequest: time.Duration(value),
			Provenance: &serving.DispatchOccupancyProvenance{
				Source: "measured", Pairs: estimate.Pairs, Window: &window, UpdatedAt: updatedAt, Value: value,
			},
		})
	}
	return measured
}

type occupancyAggregation func(
	context.Context, *rollingOccupancyInput, time.Time, time.Time,
) ([]serving.OccupancyEstimate, error)

func (c *rollingOccupancyCache) refresh(
	ctx context.Context, input *rollingOccupancyInput, now time.Time, aggregate occupancyAggregation,
) error {
	policy := input.configuration.DispatchOccupancy
	scanCtx, cancel := context.WithTimeout(ctx, time.Duration(policy.ScanTimeoutSeconds)*time.Second)
	defer cancel()
	estimates, err := aggregate(scanCtx, input, now.Add(-time.Duration(policy.AggregationWindowSeconds)*time.Second), now)
	if err == nil {
		err = scanCtx.Err()
	}
	if err != nil {
		// Neither partial facts nor a stale rolling measurement may mask DOM/default.
		estimates = nil
	}
	measured := qualifiedOccupancy(estimates, policy.MinimumPairs, time.Now().UTC())
	snapshot := &rollingOccupancySnapshot{input: input, selected: resolveOccupancy(input, measured), estimates: estimates}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.published.Load().input == input {
		c.published.Store(snapshot)
	}
	return err
}

func occupancyRefreshDelay(refresh, elapsed time.Duration) time.Duration {
	// At least as much idle wall time as total scan+reduction time. This also
	// covers non-preemptible exact sorting after the scanner's context deadline.
	return max(refresh-elapsed, elapsed)
}

func (c *rollingOccupancyCache) run(ctx context.Context, aggregate occupancyAggregation) {
	for ctx.Err() == nil {
		snapshot := c.published.Load()
		if snapshot == nil || snapshot.input.configuration.DispatchOccupancy == nil {
			c.mu.Lock()
			if c.changed == nil {
				c.changed = make(chan struct{}, 1)
			}
			changed := c.changed
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-changed:
				continue
			}
		}
		started := time.Now()
		if err := c.refresh(ctx, snapshot.input, started.UTC(), aggregate); err != nil && ctx.Err() == nil {
			ctrl.Log.Error(err, "refresh rolling dispatch occupancy")
		}
		refresh := time.Duration(snapshot.input.configuration.DispatchOccupancy.RefreshIntervalSeconds) * time.Second
		timer := time.NewTimer(occupancyRefreshDelay(refresh, time.Since(started)))
		// Configuration changes do not bypass the duty cooldown or spawn workers.
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (o *schedulerService) aggregateOccupancy(
	ctx context.Context, input *rollingOccupancyInput, from, to time.Time,
) ([]serving.OccupancyEstimate, error) {
	o.lock.Lock()
	if o.configuration.Load() != input.configuration {
		o.lock.Unlock()
		return nil, context.Canceled
	}
	profiles := input.profiles // exact accepted canonical profileByID references
	policies := o.execution.OccupancyPolicies()
	o.lock.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	estimator := serving.NewOccupancyEstimator(profiles, policies, from, to)
	if _, err := o.observationLog.ScanOccupancyRowsBounded(ctx, "", "", from, to,
		input.configuration.DispatchOccupancy.MaximumRequests, estimator.Add); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return estimator.ResultsByCell(), nil
}
