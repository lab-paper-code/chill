package kube

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// ApplyStaticRouting records a manual, configuration-sourced route application.
// Ordinary autonomous actions subsequently operate on this actual distribution.
func (c *SchedulerController) ApplyStaticRouting(ctx context.Context, routing map[string]map[string]int, sourceRV string) (err error) {
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	c.mu.Lock()
	config := c.config
	pendingRoute := ""
	for id, run := range c.runs {
		if run.action.Kind == operationRoute && run.action.Status == nativeOperationRunning {
			pendingRoute = id
			break
		}
	}
	c.mu.Unlock()
	if config.Settings.Controls.Enabled || sourceRV == "" {
		return errors.New("manual staticRouting requires controls OFF and a source ConfigMap resourceVersion")
	}
	if config.OnAction == nil {
		return errors.New("manual staticRouting requires the existing action journal")
	}
	result := OperationResult{
		Operation: Operation{
			ID: "manual-static-routing:" + sourceRV, Kind: "manual-static-routing",
			SourceConfigRV: sourceRV, StaticRouting: routing,
		},
		StartedAt: time.Now().UTC(), Status: nativeOperationRunning,
		Progress: []string{"manual staticRouting configuration awaiting actual readiness"},
	}
	defer func() {
		actual, _ := c.runtime.SchedulerRouting()
		err = c.finishStaticRouting(result, actual, err, config.OnAction)
	}()
	if pendingRoute != "" {
		result.Progress = append(result.Progress, "manual staticRouting waiting for existing global route "+pendingRoute)
		return fmt.Errorf("manual staticRouting waits for running global route %s native acknowledgement", pendingRoute)
	}
	snapshot, err := c.capture(ctx, time.Now().UTC(), config)
	if err != nil {
		return err
	}
	result.Before = OperationState{At: snapshot.At, Routing: snapshot.Routing}
	target, err := serving.StaticRoutingTarget(snapshot, routing, "manual-static-routing:"+sourceRV)
	if err != nil {
		return err
	}
	result.Operation.Target = &target
	if err = c.runtime.ValidateSchedulerTarget(target); err != nil {
		return err
	}
	result.Progress = append(result.Progress, "manual staticRouting configuration accepted")
	if err = config.OnAction(result); err != nil {
		return err
	}
	return c.operations.ApplyTarget(ctx, target)
}

func (c *SchedulerController) finishStaticRouting(result OperationResult, actual map[string]map[string]int32, applyErr error, record func(OperationResult) error) error {
	result.EndedAt = time.Now().UTC()
	result.Actual.At = result.EndedAt
	result.Actual.Routing = actual
	if applyErr == nil && !maps.EqualFunc(result.Operation.Target.Routing, actual, func(wanted, observed map[string]int32) bool {
		return maps.Equal(wanted, observed)
	}) {
		applyErr = errors.New("manual staticRouting complete physical readback differs from declared routing; accepted requests may still be draining")
	}
	if applyErr != nil {
		result.Status = nativeOperationFailed
		result.Error = applyErr.Error()
	} else {
		result.Status = nativeOperationComplete
		result.Progress = append(result.Progress, "physical routing applied and read back")
	}
	if recordErr := record(result); recordErr != nil {
		applyErr = errors.Join(applyErr, fmt.Errorf("record physical operation outcome: %w", recordErr))
		result.Error = applyErr.Error()
		if result.Status == nativeOperationComplete {
			result.Status = nativeOperationPartial
		}
	}
	c.mu.Lock()
	c.state.Results = append(c.state.Results, result)
	c.mu.Unlock()
	return applyErr
}
