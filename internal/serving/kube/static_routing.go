package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
)

// manualRoutingOperationLocked admits one explicit declaration while tickMu is held.
func (c *SchedulerController) manualRoutingOperationLocked(ctx context.Context, request ManualOperation, config SchedulerControllerConfig, snapshot serving.SchedulerSnapshot) (Operation, error) {
	if strings.TrimSpace(request.Reason) == "" || request.ProfileID != "" || request.Instance != "" || request.Node != (serving.NodeReference{}) || request.FixedBatchSize != 0 || request.MaxBatch != nil || len(request.WorkloadUIDs) != 0 || request.Scope != "" {
		return Operation{}, refused("InvalidInput", "routing requires a reason and only workloadUID and route weights")
	}
	if !slices.ContainsFunc(snapshot.Workloads, func(d *serving.Definition) bool { return d.UID == request.WorkloadUID }) {
		return Operation{}, refused("InvalidInput", "workloadUID must name a current Workload")
	}
	if slices.Contains(config.Settings.AutonomousWorkloads, request.WorkloadUID) {
		return Operation{}, refused("OperationConflict", "the Workload is under autonomous control")
	}
	if err := c.authorize(Operation{WorkloadUID: request.WorkloadUID}); err != nil {
		return Operation{}, err
	}
	weights := map[string]int{}
	positive := false
	for id, weight := range request.Weights {
		if weight < 0 {
			return Operation{}, refused("InvalidInput", "route weights must be nonnegative int32")
		}
		weights[id] = int(weight)
		positive = positive || weight > 0
	}
	switch request.Kind {
	case operationRoute:
		if !positive {
			return Operation{}, refused("InvalidInput", "route requires a positive weight")
		}
	case operationUnroute, operationRelease:
		if request.Weights != nil {
			return Operation{}, refused("InvalidInput", "unroute and release do not take weights")
		}
	default:
		return Operation{}, refused("InvalidInput", "kind must be route, unroute or release")
	}
	operation := Operation{ID: "manual-" + request.Kind + ":" + string(uuid.NewUUID()), Kind: request.Kind, WorkloadUID: request.WorkloadUID, Reason: request.Reason, ActorUsername: request.ActorUsername, ActorUID: request.ActorUID}
	target, err := serving.StaticRoutingTarget(snapshot, map[string]map[string]int{request.WorkloadUID: weights}, config.Settings.AutonomousWorkloads, operation.ID)
	if err != nil {
		return Operation{}, refused("OperationConflict", err.Error())
	}
	if err = c.validateManualRoutingNodes(ctx, request, snapshot, weights); err != nil {
		return Operation{}, err
	}
	if err = c.runtime.ValidateSchedulerWorkloadsTarget(target, []string{request.WorkloadUID}); err != nil {
		return Operation{}, refused("OperationConflict", err.Error())
	}
	if request.Kind != operationRelease {
		if err = c.operations.validateRoutingPolicies(ctx, target, request.WorkloadUID); err != nil {
			return Operation{}, refused("OperationConflict", err.Error())
		}
		operation.Target = controlledTarget(Operation{Target: &target, WorkloadUID: request.WorkloadUID})
	}
	return operation, nil
}

// Validate withdrawn routes and destinations before persisting any declaration.
func (c *SchedulerController) validateManualRoutingNodes(ctx context.Context, request ManualOperation, snapshot serving.SchedulerSnapshot, weights map[string]int) error {
	nodes := map[serving.NodeReference]bool{}
	if request.Kind == operationRelease {
		pods, err := c.kube.CoreV1().Pods(c.operations.config.Namespace).List(ctx, meta.ListOptions{})
		if err != nil {
			return err
		}
		for _, pod := range pods.Items {
			var seeds map[string]int32
			if raw := pod.Annotations[ServingRoutingAnnotation]; raw != "" {
				if err = json.Unmarshal([]byte(raw), &seeds); err != nil {
					return err
				}
			}
			status := firstfloorStatus(&pod)
			if status == nil || seeds[request.WorkloadUID] <= 0 {
				continue
			}
			for _, instance := range snapshot.Instances {
				if instance.ID == physicalID(pod.UID, status.ContainerID) {
					nodes[instance.Node] = true
					if manualWorkloadConflict(instance, request.WorkloadUID) {
						return refused("OperationConflict", "release default target serves another Workload")
					}
				}
			}
		}
	}
	for _, instance := range snapshot.Instances {
		if snapshot.Routing[request.WorkloadUID][instance.ID] > 0 || weights[instance.ID] > 0 || instance.OwnerWorkloadUID == request.WorkloadUID || instance.WorkloadUID == request.WorkloadUID {
			if weights[instance.ID] > 0 && manualWorkloadConflict(instance, request.WorkloadUID) {
				return refused("OperationConflict", "route target serves another Workload")
			}
			nodes[instance.Node] = true
		}
	}
	for node := range nodes {
		if _, err := c.admitNodeLocked(ctx, node); err != nil {
			return err
		}
	}
	return nil
}

func manualWorkloadConflict(instance serving.SchedulerInstance, uid string) bool {
	return instance.OwnerWorkloadUID != "" && instance.OwnerWorkloadUID != uid ||
		instance.WorkloadUID != "" && instance.WorkloadUID != uid
}

// configurationNotApplied distinguishes a confirmed rejection from an unknown
// Update outcome. It is only safe to release a run before any earlier effect.
type configurationNotApplied struct{ error }

func (e *configurationNotApplied) Unwrap() error { return e.error }

func configurationUpdateRejected(err error) bool {
	return apierrors.IsConflict(err) || apierrors.IsInvalid(err) || apierrors.IsForbidden(err) ||
		apierrors.IsUnauthorized(err) || apierrors.IsNotFound(err)
}

// persistManualConfiguration updates only the selected declaration/settings field
// using the ConfigMap's resourceVersion. A conflict is returned, never retried.
func (c *SchedulerController) persistManualConfiguration(ctx context.Context, operation *Operation) (err error) {
	updateAttempted := false
	defer func() {
		if err != nil && (!updateAttempted || configurationUpdateRejected(err)) {
			err = &configurationNotApplied{err}
		}
	}()
	c.mu.Lock()
	config := c.config
	c.mu.Unlock()
	if config.ConfigurationName == "" || config.ConfigurationKey == "" {
		return errors.New("manual operation requires the scheduler ConfigMap name and key")
	}
	cm, err := c.kube.CoreV1().ConfigMaps(c.operations.config.Namespace).Get(ctx, config.ConfigurationName, meta.GetOptions{})
	if err != nil {
		return err
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal([]byte(cm.Data[config.ConfigurationKey]), &document); err != nil {
		return err
	}
	if document == nil {
		return errors.New("scheduler configuration must be an object")
	}
	var input serving.SchedulerSettingsInput
	if err = json.Unmarshal(document["settings"], &input); err != nil {
		return err
	}
	settings, err := input.Settings()
	if err != nil {
		return err
	}
	var routing map[string]map[string]int
	if raw := document["staticRouting"]; len(raw) != 0 {
		if err = json.Unmarshal(raw, &routing); err != nil {
			return err
		}
	}
	operation.BeforeStaticRouting = maps.Clone(routing)
	changed := false
	switch operation.Kind {
	case operationRoute, operationUnroute, operationRelease:
		if slices.Contains(settings.AutonomousWorkloads, operation.WorkloadUID) {
			return refused("OperationConflict", "the Workload is under autonomous control")
		}
		if routing == nil {
			routing = map[string]map[string]int{}
		}
		if operation.Kind == operationRelease {
			_, changed = routing[operation.WorkloadUID]
			delete(routing, operation.WorkloadUID)
		} else {
			weights := map[string]int{}
			if operation.Target == nil {
				return errors.New("routing target required")
			}
			for id, weight := range operation.Target.Routing[operation.WorkloadUID] {
				weights[id] = int(weight)
			}
			previous, exists := routing[operation.WorkloadUID]
			changed = !exists || !maps.Equal(previous, weights)
			routing[operation.WorkloadUID] = weights
		}
		if changed {
			if document["staticRouting"], err = json.Marshal(routing); err != nil {
				return err
			}
			now := time.Now().UTC()
			operation.SettingsValidatedAt = &now
			document["staticRoutingValidatedAt"], _ = json.Marshal(now)
		}
	case operationReset:
		before := len(settings.AutonomousWorkloads)
		settings.AutonomousWorkloads = slices.DeleteFunc(slices.Clone(settings.AutonomousWorkloads), func(uid string) bool { return slices.Contains(operation.WorkloadUIDs, uid) })
		changed = before != len(settings.AutonomousWorkloads)
		if changed {
			if document["settings"], err = json.Marshal(settings); err != nil {
				return err
			}
		}
	default:
		return errors.New("operation does not change manual configuration")
	}
	committed := cm
	if changed {
		raw, marshalErr := json.Marshal(document)
		if marshalErr != nil {
			return marshalErr
		}
		cm.Data[config.ConfigurationKey] = string(raw)
		updateAttempted = true
		committed, err = c.kube.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, meta.UpdateOptions{})
		if err != nil {
			return err
		}
	}
	operation.SourceConfigRV = committed.ResourceVersion
	operation.StaticRouting = routing
	c.mu.Lock()
	if operation.Kind == operationReset {
		c.config.Settings.AutonomousWorkloads = slices.DeleteFunc(slices.Clone(c.config.Settings.AutonomousWorkloads),
			func(uid string) bool { return slices.Contains(operation.WorkloadUIDs, uid) })
	}
	c.config.StaticRouting = len(routing) > 0
	c.mu.Unlock()
	return nil
}

// executeManualRouting uses the caller's run and observation-log ownership.
func (c *SchedulerController) executeManualRouting(ctx context.Context, operation Operation) (OperationResult, error) {
	result := OperationResult{Operation: operation, StartedAt: time.Now().UTC(), Status: nativeOperationRunning}
	result.Before.At = result.StartedAt
	result.Before.Routing, _ = c.runtime.SchedulerRouting()
	beforeDeclared := c.runtime.InstanceRouting()
	err := c.persistManualConfiguration(ctx, &result.Operation)
	var rejected *configurationNotApplied
	result.effectsNotStarted = errors.As(err, &rejected)
	if err == nil {
		result.Progress = append(result.Progress, "manual declaration persisted; physical acknowledgement pending")
		physical, executeErr := c.operations.Execute(ctx, result.Operation)
		result.Before, result.Actual = physical.Before, physical.Actual
		result.Progress = append(result.Progress, physical.Progress...)
		result.DrainNative = physical.DrainNative
		err = executeErr
	}
	result.EndedAt = time.Now().UTC()
	result.Actual.At = result.EndedAt
	result.Actual.Routing, _ = c.runtime.SchedulerRouting()
	if err == nil {
		// Complete declaration readback excludes routes retained for accepted calls:
		// unroute ACK must precede their separate drain step.
		expected := beforeDeclared
		for _, weights := range expected {
			delete(weights, operation.WorkloadUID)
		}
		if operation.Kind != operationRelease {
			for id, weight := range operation.Target.Routing[operation.WorkloadUID] {
				if weight <= 0 {
					continue
				}
				if expected[id] == nil {
					expected[id] = map[string]int32{}
				}
				expected[id][operation.WorkloadUID] = weight
			}
		}
		if operation.Kind != operationRelease {
			want, got := map[string]int32{}, map[string]int32{}
			for id, weights := range expected {
				if weights[operation.WorkloadUID] > 0 {
					want[id] = weights[operation.WorkloadUID]
				}
			}
			for id, weights := range c.runtime.InstanceRouting() {
				if weights[operation.WorkloadUID] > 0 {
					got[id] = weights[operation.WorkloadUID]
				}
			}
			if !maps.Equal(want, got) {
				err = errors.New("selected UID physical declaration readback differs from requested routing")
			}
		}
	}
	if err != nil {
		result.Status = nativeOperationFailed
		if len(result.Progress) > 0 || len(result.Actual.Native) > 0 {
			result.Status = nativeOperationPartial
		}
		result.Error = err.Error()
	} else {
		result.Status = nativeOperationComplete
		result.Progress = append(result.Progress, "physical routing acknowledged and read back")
	}
	return result, err
}

// validateRoutingPolicies checks immutable native policy compatibility before a
// manual declaration or physical policy can be changed.
func (o *Operations) validateRoutingPolicies(ctx context.Context, target serving.SchedulerCandidate, workloadUID string) error {
	pods, err := o.kube.CoreV1().Pods(o.config.Namespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	for _, instance := range target.Instances {
		if instance.WorkloadUID != workloadUID {
			continue
		}
		found := false
		for _, pod := range pods.Items {
			status := firstfloorStatus(&pod)
			if status == nil || physicalID(pod.UID, status.ContainerID) != instance.ID {
				continue
			}
			found = true
			cm, err := o.kube.CoreV1().ConfigMaps(pod.Namespace).Get(ctx, pod.Name, meta.GetOptions{})
			if err != nil {
				return err
			}
			var config native.Config
			if err = json.Unmarshal([]byte(cm.Data["config.json"]), &config); err != nil {
				return err
			}
			if instance.Profile == nil || instance.Profile.Condition.Firstfloor == nil || config.Policy.ProfileID != instance.Profile.ID || native.EffectiveDeadlinePolicy(config.Policy.DeadlinePolicy) != native.EffectiveDeadlinePolicy(instance.Profile.Condition.Firstfloor.DeadlinePolicy) || config.Policy.FixedBatchSize != instance.FixedBatchSize {
				return fmt.Errorf("instance %s has incompatible immutable native policy", instance.ID)
			}
		}
		if !found {
			return fmt.Errorf("actual policy target %s unavailable", instance.ID)
		}
	}
	return nil
}
