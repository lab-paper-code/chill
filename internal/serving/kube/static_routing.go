package kube

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

const manualStaticRoutingOperation = "manual-static-routing"

// ApplyStaticRouting records a manual, configuration-sourced route application
// for disabled Workloads; every other Workload keeps its current distribution.
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
	if sourceRV == "" {
		return errors.New("manual staticRouting requires a source ConfigMap resourceVersion")
	}
	if config.OnAction == nil {
		return errors.New("manual staticRouting requires the existing action observation log")
	}
	result := OperationResult{
		Operation: Operation{
			ID: manualStaticRoutingOperation + ":" + sourceRV, Kind: manualStaticRoutingOperation,
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
	target, err := serving.StaticRoutingTarget(snapshot, routing, config.Settings.AutonomousWorkloads, manualStaticRoutingOperation+":"+sourceRV)
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
	progress := len(result.Progress)
	err = c.operations.ApplyTarget(ctx, target, &result)
	if err != nil && len(result.Progress) > progress {
		result.Status = nativeOperationPartial
	}
	return err
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
		if result.Status != nativeOperationPartial {
			result.Status = nativeOperationFailed
		}
		if len(result.Actual.Native) > 0 {
			result.Status = nativeOperationPartial
		}
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
