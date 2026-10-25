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

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
)

const nonAutonomousResetScope = "all-non-autonomous"

type manualResetPlan struct {
	nodes     []serving.NodeReference
	instances []serving.SchedulerInstance
}

func (c *SchedulerController) manualResetOperationLocked(request ManualOperation, config SchedulerControllerConfig, snapshot serving.SchedulerSnapshot) (Operation, error) {
	if strings.TrimSpace(request.Reason) == "" || request.WorkloadUID != "" || request.Node != (serving.NodeReference{}) || request.ProfileID != "" || request.Instance != "" || request.FixedBatchSize != 0 || request.Weights != nil {
		return Operation{}, refused("InvalidInput", "reset requires a reason and only workloadUIDs or scope")
	}
	if (request.WorkloadUIDs != nil) == (request.Scope != "") || request.WorkloadUIDs != nil && len(request.WorkloadUIDs) == 0 ||
		request.Scope != "" && request.Scope != nonAutonomousResetScope {
		return Operation{}, refused("InvalidInput", "reset requires workloadUIDs or scope all-non-autonomous, exclusively")
	}
	uids := make([]string, len(request.WorkloadUIDs))
	copy(uids, request.WorkloadUIDs)
	if request.Scope != "" {
		for _, d := range snapshot.Workloads {
			if d != nil && !slices.Contains(config.Settings.AutonomousWorkloads, d.UID) {
				uids = append(uids, d.UID)
			}
		}
	}
	slices.Sort(uids)
	for index, uid := range uids {
		if uid == "" || index > 0 && uid == uids[index-1] || !slices.ContainsFunc(snapshot.Workloads, func(d *serving.Definition) bool { return d != nil && d.UID == uid }) {
			return Operation{}, refused("InvalidInput", "reset UIDs must be unique current Workload UIDs")
		}
	}
	return Operation{ID: "manual-reset:" + string(uuid.NewUUID()), Kind: operationReset, WorkloadUIDs: uids, Reason: request.Reason, ActorUsername: request.ActorUsername, ActorUID: request.ActorUID}, nil
}

// Admission freezes the affected Nodes and refuses in-flight ownership instead
// of overlapping a reset with an already executing route or Node operation.
func (c *SchedulerController) planManualResetLocked(ctx context.Context, operation Operation) (manualResetPlan, error) {
	plan := manualResetPlan{}
	owned, err := c.resetOwnedTargets(ctx, operation.WorkloadUIDs)
	if err != nil {
		return plan, err
	}
	plan.instances = owned
	for _, instance := range owned {
		if !slices.Contains(plan.nodes, instance.Node) {
			plan.nodes = append(plan.nodes, instance.Node)
		}
	}
	pending, err := c.resetPendingPowerNodes(ctx, operation.WorkloadUIDs)
	if err != nil {
		return plan, err
	}
	for _, node := range pending {
		if !slices.Contains(plan.nodes, node) {
			plan.nodes = append(plan.nodes, node)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, run := range c.runs {
		if run.action.Status == nativeOperationRunning && (isRoutingOperation(run.action.Kind) || run.action.Kind == operationReset || slices.Contains(plan.nodes, run.action.Node)) {
			return plan, refused("OperationConflict", "a running route or affected Node operation owns reset resources")
		}
	}
	if c.goal != nil && slices.ContainsFunc(c.goal.Instances, func(i serving.SchedulerInstance) bool { return slices.Contains(operation.WorkloadUIDs, i.WorkloadUID) }) {
		for _, action := range c.goal.Actions {
			run := c.runs[action.ID]
			if !action.Fixed && action.From != readySubsetRoute && (run == nil || run.action.Status != nativeOperationComplete) {
				return plan, refused("OperationConflict", "the autonomous transition must complete before reset")
			}
		}
	}
	return plan, nil
}

// Removal can succeed before release or power return fails. Rediscover empty
// candidate Nodes from current state rather than losing them with their Pods.
func (c *SchedulerController) resetPendingPowerNodes(ctx context.Context, uids []string) ([]serving.NodeReference, error) {
	c.mu.Lock()
	config := c.config
	c.mu.Unlock()
	snapshot, err := c.capture(ctx, time.Now(), config)
	if err != nil {
		return nil, err
	}
	pods, err := c.kube.CoreV1().Pods("").List(ctx, meta.ListOptions{})
	if err != nil {
		return nil, err
	}
	var nodes []serving.NodeReference
	for _, node := range snapshot.Nodes {
		lowest := serving.SchedulerLowestMode(config.Baselines, node.Node)
		if node.Measurement || slices.Contains(node.Reasons, serving.NodeExcluded) ||
			lowest == "" || node.PowerMode == lowest && node.Ready {
			continue
		}
		if slices.ContainsFunc(pods.Items, func(p core.Pod) bool {
			return p.Spec.NodeName == node.Node.Name && p.Labels["app.kubernetes.io/component"] == servingInstanceComponent
		}) {
			continue
		}
		if slices.ContainsFunc(snapshot.Options, func(option serving.SchedulerOption) bool {
			return option.Instance.Node == node.Node && option.Instance.Profile != nil && option.Instance.Profile.Condition.NodeUID == node.Node.UID &&
				slices.ContainsFunc(option.CompatibleUIDs, func(uid string) bool { return slices.Contains(uids, uid) })
		}) {
			nodes = append(nodes, node.Node)
		}
	}
	return nodes, nil
}

func (c *SchedulerController) resetOwnedTargets(ctx context.Context, uids []string) ([]serving.SchedulerInstance, error) {
	pods, err := c.kube.CoreV1().Pods(c.operations.config.Namespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return nil, err
	}
	owned := []serving.SchedulerInstance{}
	physical := c.runtime.Instances()
	for _, pod := range pods.Items {
		if pod.Labels["app.kubernetes.io/component"] != servingInstanceComponent {
			continue
		}
		owner := workloadOwner(pod.OwnerReferences)
		seeds := map[string]int32{}
		if raw := pod.Annotations[ServingRoutingAnnotation]; raw != "" {
			if err := json.Unmarshal([]byte(raw), &seeds); err != nil {
				return nil, err
			}
		}
		for uid, weight := range seeds {
			if weight > 0 && owner != "" && uid != owner && (slices.Contains(uids, uid) || slices.Contains(uids, owner)) {
				return nil, refused("OperationConflict", "reset Pod routing conflicts with Workload ownership")
			}
		}
		if !slices.Contains(uids, owner) {
			continue
		}
		status := firstfloorStatus(&pod)
		if status == nil || status.ContainerID == "" {
			return nil, refused("OperationConflict", "owned Pod lacks exact native container identity")
		}
		id := physicalID(pod.UID, status.ContainerID)
		for _, instance := range physical {
			if instance.ID != id {
				continue
			}
			if instance.OwnerWorkloadUID != "" && instance.OwnerWorkloadUID != owner ||
				slices.ContainsFunc(instance.Workloads, func(workload serving.WorkloadReference) bool { return workload.UID != owner }) {
				return nil, refused("OperationConflict", "owned reset instance is bound to another Workload")
			}
		}
		node, err := c.kube.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, meta.GetOptions{})
		if err != nil {
			return nil, err
		}
		owned = append(owned, serving.SchedulerInstance{ID: id, Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, OwnerWorkloadUID: owner})
	}
	slices.SortFunc(owned, func(a, b serving.SchedulerInstance) int { return strings.Compare(a.ID, b.ID) })
	return owned, nil
}

func (c *SchedulerController) reserveResetNodes(operation Operation, plan manualResetPlan) []string {
	ids := make([]string, 0, len(plan.nodes))
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, node := range plan.nodes {
		id := operation.ID + "/node/" + node.UID
		ids = append(ids, id)
		c.runs[id] = &actionRun{action: serving.Action{ID: id, Kind: operationReset, Node: node, From: externalActionSource, Status: nativeOperationRunning}, started: time.Now()}
	}
	return ids
}

func (c *SchedulerController) releaseResetNodes(ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		delete(c.runs, id)
	}
}

func (c *SchedulerController) resetState(ctx context.Context, plan manualResetPlan) (OperationState, error) {
	state := OperationState{At: time.Now().UTC(), Instances: c.runtime.Instances(), Native: map[string]native.State{}}
	state.Routing, _ = c.runtime.SchedulerRouting()
	for _, node := range plan.nodes {
		actual, err := c.operations.Capture(ctx, node)
		state.Pods = append(state.Pods, actual.Pods...)
		state.States = append(state.States, actual.States...)
		maps.Copy(state.Native, actual.Native)
		if len(plan.nodes) == 1 {
			state.Node, state.PowerMode = node, actual.PowerMode
		}
		if err != nil {
			return state, err
		}
		if len(actual.Reasons) != 0 {
			return state, errors.New(strings.Join(actual.Reasons, "; "))
		}
	}
	return state, nil
}

func (c *SchedulerController) executeManualReset(ctx context.Context, operation Operation, config SchedulerControllerConfig, plan manualResetPlan, result *OperationResult) error {
	var err error
	phases := []struct {
		name string
		run  func() error
	}{
		{"disable-autonomy", func() error {
			if err := c.persistManualConfiguration(ctx, &result.Operation); err != nil {
				return err
			}
			c.mu.Lock()
			if c.goal != nil && slices.ContainsFunc(c.goal.Instances, func(i serving.SchedulerInstance) bool { return slices.Contains(operation.WorkloadUIDs, i.WorkloadUID) }) {
				c.goal = nil
			}
			c.mu.Unlock()
			return nil
		}},
		{"unroute", func() error { return c.resetRouting(ctx, operation, operationUnroute, result) }},
		{"drain", func() error {
			return c.operations.wait(ctx, func() (bool, error) {
				for _, request := range c.runtime.SchedulerInputs(time.Now).Outstanding {
					if request.Definition != nil && slices.Contains(operation.WorkloadUIDs, request.Definition.UID) {
						return false, nil
					}
				}
				state, err := c.resetState(ctx, plan)
				for id, native := range state.Native {
					if !slices.ContainsFunc(plan.instances, func(instance serving.SchedulerInstance) bool { return instance.ID == id }) {
						continue
					}
					if len(native.Requests) > 0 {
						return false, err
					}
				}
				return err == nil, err
			})
		}},
		{"remove", func() error {
			owned, err := c.resetOwnedTargets(ctx, operation.WorkloadUIDs)
			if err != nil {
				return err
			}
			for _, instance := range owned {
				if !slices.Contains(plan.nodes, instance.Node) {
					return refused("OperationConflict", "reset ownership moved outside the admitted Nodes")
				}
				outcome, err := c.operations.Execute(ctx, Operation{ID: operation.ID + "/remove/" + instance.ID, Kind: operationRemove, Node: instance.Node, Instance: instance.ID})
				if result.DrainNative == nil {
					result.DrainNative = map[string]native.State{}
				}
				maps.Copy(result.DrainNative, outcome.DrainNative)
				result.Progress = append(result.Progress, outcome.Progress...)
				if err != nil {
					return err
				}
			}
			return nil
		}},
		{"release", func() error { return c.resetRouting(ctx, operation, operationRelease, result) }},
		{"lower-power", func() error { return c.resetLowerNodes(ctx, plan.nodes, config, result) }},
	}
	for index, phase := range phases {
		step := OperationStep{Name: phase.name, StartedAt: time.Now().UTC(), Status: nativeOperationRunning}
		step.Before, err = c.resetState(ctx, plan)
		if index == 0 {
			result.Before = step.Before
		}
		stepIndex := len(result.Steps)
		result.Steps = append(result.Steps, step)
		if err == nil && config.OnAction != nil {
			err = config.OnAction(*result)
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			err = phase.run()
		}
		step.EndedAt = time.Now().UTC()
		var observeErr error
		step.Actual, observeErr = c.resetState(ctx, plan)
		err = errors.Join(err, observeErr)
		step.Status = nativeOperationComplete
		if err != nil {
			step.Status, step.Error = nativeOperationFailed, err.Error()
		}
		result.Steps[stepIndex] = step
		result.Actual = step.Actual
		if config.OnAction != nil {
			err = errors.Join(err, config.OnAction(*result))
		}
		if err != nil {
			result.Status = nativeOperationFailed
			if len(result.Steps) > 1 {
				result.Status = nativeOperationPartial
			}
			return err
		}
	}
	return nil
}

func (c *SchedulerController) resetRouting(ctx context.Context, root Operation, kind string, result *OperationResult) error {
	for _, uid := range root.WorkloadUIDs {
		c.mu.Lock()
		config := c.config
		c.mu.Unlock()
		if kind == operationUnroute {
			declared, active, err := c.resetRoutingPresent(ctx, uid, config)
			if err != nil {
				return err
			}
			if !declared && !active {
				continue
			}
		}
		snapshot, err := c.capture(ctx, time.Now(), config)
		if err != nil {
			return err
		}
		request := ManualOperation{Kind: kind, WorkloadUID: uid, Reason: root.Reason, ActorUsername: root.ActorUsername, ActorUID: root.ActorUID}
		// Admission already reserved these resources; do not conflict with this
		// reset's own route/Node reservations when composing its internal steps.
		target, err := serving.StaticRoutingTarget(snapshot, map[string]map[string]int{uid: {}}, config.Settings.AutonomousWorkloads, root.ID+"/"+kind+"/"+uid)
		if err != nil {
			return err
		}
		operation := Operation{ID: target.ID, Kind: request.Kind, WorkloadUID: uid, Reason: request.Reason, ActorUsername: request.ActorUsername, ActorUID: request.ActorUID, Target: &target}
		outcome, err := c.executeManualRouting(ctx, operation)
		result.Operation.SourceConfigRV, result.Operation.StaticRouting = outcome.Operation.SourceConfigRV, outcome.Operation.StaticRouting
		result.Progress = append(result.Progress, outcome.Progress...)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *SchedulerController) resetRoutingPresent(ctx context.Context, uid string, config SchedulerControllerConfig) (bool, bool, error) {
	cm, err := c.kube.CoreV1().ConfigMaps(c.operations.config.Namespace).Get(ctx, config.ConfigurationName, meta.GetOptions{})
	if err != nil {
		return false, false, err
	}
	var document struct {
		StaticRouting map[string]map[string]int `json:"staticRouting"`
	}
	if err := json.Unmarshal([]byte(cm.Data[config.ConfigurationKey]), &document); err != nil {
		return false, false, err
	}
	_, declared := document.StaticRouting[uid]
	owned, err := c.resetOwnedTargets(ctx, []string{uid})
	if err != nil {
		return false, false, err
	}
	if len(owned) != 0 {
		return declared, true, nil
	}
	for _, instance := range c.runtime.Instances() {
		if instance.Routable[uid] {
			return declared, true, nil
		}
	}
	return declared, false, nil
}

func (c *SchedulerController) resetLowerNodes(ctx context.Context, nodes []serving.NodeReference, config SchedulerControllerConfig, result *OperationResult) error {
	var classes edge.DeviceClassList
	if err := c.store.client.List(ctx, &classes); err != nil {
		return err
	}
	for _, ref := range nodes {
		node, err := c.kube.CoreV1().Nodes().Get(ctx, ref.Name, meta.GetOptions{})
		if err != nil {
			return err
		}
		if string(node.UID) != ref.UID || profilingrun.InMeasurement(node) {
			return errors.New("reset Node identity or measurement ownership changed")
		}
		pods, err := c.kube.CoreV1().Pods("").List(ctx, meta.ListOptions{})
		if err != nil {
			return err
		}
		if slices.ContainsFunc(pods.Items, func(p core.Pod) bool {
			return p.Spec.NodeName == node.Name && p.Labels["app.kubernetes.io/component"] == servingInstanceComponent
		}) {
			result.Progress = append(result.Progress, node.Name+": power skipped; another serving Pod remains")
			continue
		}
		class := nodeClass(node, classes.Items)
		mode := readBackMode(node, class)
		if class == nil || mode == "" || !slices.ContainsFunc(node.Status.Conditions, func(condition core.NodeCondition) bool {
			return condition.Type == core.NodeReady && condition.Status == core.ConditionTrue
		}) {
			return errors.New("reset requires actual Node mode and Ready readback")
		}
		if !slices.ContainsFunc(class.Spec.PowerModes, func(mode edge.PowerMode) bool { return mode.Actuator.Kind != edge.PowerModeActuatorFixed }) {
			result.Progress = append(result.Progress, node.Name+": power skipped; fixed mode "+mode)
			continue
		}
		lowest := serving.SchedulerLowestMode(config.Baselines, ref)
		if lowest == "" {
			return fmt.Errorf("Node %s lacks the measured lowest power mode", node.Name)
		}
		if lowest == mode {
			result.Progress = append(result.Progress, node.Name+": lowest mode already Ready")
			continue
		}
		duration, supported := nodePowerTargets(node, classes.Items, config.PowerPolicies)[lowest]
		if !supported {
			return fmt.Errorf("Node %s has no actuation policy for lowest mode %s", node.Name, lowest)
		}
		outcome, err := c.operations.Execute(ctx, Operation{ID: result.Operation.ID + "/power/" + ref.UID, Kind: operationPower, Node: ref, PowerMode: lowest, MaxActuationDuration: duration})
		result.Progress = append(result.Progress, outcome.Progress...)
		if err != nil {
			return err
		}
		if err := c.operations.wait(ctx, func() (bool, error) {
			actual, err := c.kube.CoreV1().Nodes().Get(ctx, ref.Name, meta.GetOptions{})
			if err != nil {
				return false, err
			}
			if string(actual.UID) != ref.UID {
				return false, errors.New("reset Node identity changed during power readback")
			}
			ready := slices.ContainsFunc(actual.Status.Conditions, func(condition core.NodeCondition) bool {
				return condition.Type == core.NodeReady && condition.Status == core.ConditionTrue
			})
			return ready && readBackMode(actual, class) == lowest, nil
		}); err != nil {
			return err
		}
	}
	return nil
}
