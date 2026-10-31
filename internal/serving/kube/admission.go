package kube

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// handBackKind is the run that returns a measured Node to the available Nodes.
	handBackKind = "measurement-handback"

	refusedNodeMissing  = "NodeMissing"
	refusedNodeServing  = "NodeServing"
	powerChangedReason  = "PowerChangedDuringMeasurement"
	campaignFailedPhase = "Failed"
	// measurementInvalidCode reports a measurement the evidence readers count and do not use.
	measurementInvalidCode = "MeasurementInvalid"
)

// measurementRoot is one measurement object that asks ChillServer for a Node.
// Children a granted root creates are not roots.
type measurementRoot struct {
	object   client.Object
	node     string
	terminal bool
}

// measurementRoots lists the measurement objects of the namespaces.
func (c *SchedulerController) measurementRoots(ctx context.Context, namespaces []string) ([]measurementRoot, error) {
	var roots []measurementRoot
	for _, namespace := range namespaces {
		var beos edge.BatchEnergyOptimizationList
		if err := c.store.client.List(ctx, &beos, client.InNamespace(namespace)); err != nil {
			return nil, err
		}
		for i := range beos.Items {
			o := &beos.Items[i]
			// A curve plan resolution failure is retried by its controller.
			terminal := o.Status.Phase == catalogCompletePhase || o.Status.Phase == campaignFailedPhase && o.Status.Outcome != "CurvePlanResolutionFailed"
			roots = append(roots, measurementRoot{object: o, node: o.Spec.State.TargetNode, terminal: terminal})
		}
		var baselines edge.ModelLoadedIdleBaselineList
		if err := c.store.client.List(ctx, &baselines, client.InNamespace(namespace)); err != nil {
			return nil, err
		}
		for i := range baselines.Items {
			o := &baselines.Items[i]
			terminal := o.Status.Phase == edge.ModelLoadedIdleBaselinePhaseComplete || o.Status.Phase == edge.ModelLoadedIdleBaselinePhaseInsufficient
			roots = append(roots, measurementRoot{object: o, node: o.Spec.State.TargetNode, terminal: terminal})
		}
		var runs edge.ProfilingRunList
		if err := c.store.client.List(ctx, &runs, client.InNamespace(namespace)); err != nil {
			return nil, err
		}
		for i := range runs.Items {
			o := &runs.Items[i]
			if owner := meta.GetControllerOf(o); owner != nil && (owner.Kind == "BatchEnergyOptimization" || owner.Kind == "ModelLoadedIdleBaseline") {
				continue
			}
			done := false
			for _, condition := range o.Status.Conditions {
				done = done || condition.Type == edge.ProfilingRunConditionExecutionComplete && condition.Status != meta.ConditionUnknown
			}
			roots = append(roots, measurementRoot{object: o, node: o.Spec.TargetNode, terminal: done})
		}
		var transitions edge.PowerModeTransitionCostList
		if err := c.store.client.List(ctx, &transitions, client.InNamespace(namespace)); err != nil {
			return nil, err
		}
		for i := range transitions.Items {
			o := &transitions.Items[i]
			terminal := o.Status.Phase == edge.PowerModeTransitionCostPhaseComplete || o.Status.Phase == edge.PowerModeTransitionCostPhaseFailed
			roots = append(roots, measurementRoot{object: o, node: o.Spec.TargetNode, terminal: terminal})
		}
		var costs edge.OperationCostMeasurementList
		if err := c.store.client.List(ctx, &costs, client.InNamespace(namespace)); err != nil {
			return nil, err
		}
		for i := range costs.Items {
			o := &costs.Items[i]
			roots = append(roots, measurementRoot{object: o, node: o.Spec.Node, terminal: measurementTerminal(o.Status.Phase)})
		}
		var occupancies edge.DispatchOccupancyMeasurementList
		if err := c.store.client.List(ctx, &occupancies, client.InNamespace(namespace)); err != nil {
			return nil, err
		}
		for i := range occupancies.Items {
			o := &occupancies.Items[i]
			roots = append(roots, measurementRoot{object: o, node: o.Spec.Node, terminal: measurementTerminal(o.Status.Phase)})
		}
	}
	return roots, nil
}

// Admit is the single writer of the measurement taint. It grants a waiting
// measurement an empty Node, refuses a Node that runs a CHILL serving Pod, and
// starts the hand-back of a Node whose measurement ended or vanished. Holding
// tickMu orders it against Tick: neither acts on a Node the other holds.
func (c *SchedulerController) Admit(ctx context.Context) error {
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	c.mu.Lock()
	config := c.config
	c.mu.Unlock()
	observedAt := time.Now()
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	pods, err := c.kube.CoreV1().Pods(c.observer.config.Namespace).List(ctx, meta.ListOptions{
		LabelSelector: "app.kubernetes.io/component=" + servingInstanceComponent,
	})
	if err != nil {
		return err
	}
	c.releaseObservedScopes(nodes.Items, pods.Items, nil, observedAt)
	c.mu.Lock()
	busy := map[string]bool{}
	for _, run := range c.runs {
		if run.reserved {
			for _, node := range run.scope.Nodes {
				busy[node.UID] = true
			}
		}
	}
	c.mu.Unlock()
	roots, err := c.measurementRoots(ctx, config.MeasurementNamespaces)
	if err != nil {
		return err
	}
	var classes edge.DeviceClassList
	if err = c.store.client.List(ctx, &classes); err != nil {
		return err
	}
	now := time.Now()
	in := admissionInputs{config: config, pods: pods.Items, classes: classes.Items, roots: roots, profiles: c.observer.Profiles(), now: now}
	byName := map[string]*core.Node{}
	byUID := map[string]*measurementRoot{}
	for i := range nodes.Items {
		byName[nodes.Items[i].Name] = &nodes.Items[i]
	}
	for i := range roots {
		byUID[string(roots[i].object.GetUID())] = &roots[i]
	}
	errs := c.continueHeldNodes(ctx, nodes.Items, byUID, config, now)
	waiting := slices.DeleteFunc(slices.Clone(roots), func(r measurementRoot) bool {
		_, admitted := r.object.GetAnnotations()[profilingrun.AdmissionAnnotation]
		return admitted || r.terminal
	})
	slices.SortFunc(waiting, func(a, b measurementRoot) int {
		return cmp.Or(a.object.GetCreationTimestamp().Compare(b.object.GetCreationTimestamp().Time),
			strings.Compare(a.object.GetNamespace()+"/"+a.object.GetName(), b.object.GetNamespace()+"/"+b.object.GetName()))
	})
	// A root that waits holds its Node's queue: a later root on that Node is not
	// granted before it, so creation order is execution order on every Node. A
	// refusal is terminal and lets the next root go.
	claimed := map[string]bool{}
	for _, root := range waiting {
		node := byName[root.node]
		if node == nil {
			errs = append(errs, c.annotate(ctx, root.object, profilingrun.AdmissionAnnotation, profilingrun.RefusedAdmission(refusedNodeMissing, root.node)))
			continue
		}
		if c.authorize(c.measurementRootOperation(root, node, byName, config)) != nil {
			claimed[node.Name] = true
			continue
		}
		if claimed[node.Name] || busy[string(node.UID)] || held(node, in) {
			claimed[node.Name] = true
			continue
		}
		reason, detail, wait, start := c.rootRefusal(ctx, root, node, in)
		if wait {
			claimed[node.Name] = true
			continue
		}
		if reason != "" {
			errs = append(errs, c.annotate(ctx, root.object, profilingrun.AdmissionAnnotation, profilingrun.RefusedAdmission(reason, detail)),
				c.failMeasurement(ctx, root.object, strings.TrimSpace(reason+" "+detail)))
			continue
		}
		transition, _ := root.object.(*edge.PowerModeTransitionCost)
		var runner PrepareSpec
		if transition != nil {
			if runner, reason = c.runnerPreparation(transition, node, classes.Items, config); reason != "" {
				errs = append(errs, c.annotate(ctx, root.object, profilingrun.AdmissionAnnotation, profilingrun.RefusedAdmission(reason, "")))
				continue
			}
		}
		claimed[node.Name] = true
		if !c.reserveMeasurementRoot(root, node, byName, config) {
			continue
		}
		if err := c.setMeasurementTaint(ctx, node, string(root.object.GetUID())); err != nil {
			errs = append(errs, err)
			c.mu.Lock()
			lease := c.runs[measurementLeaseKind+"/"+string(root.object.GetUID())]
			lease.action.Status = nativeOperationFailed
			lease.result.EndedAt, lease.result.Error = time.Now(), err.Error()
			c.mu.Unlock()
			continue
		}
		if transition != nil {
			c.startRunner(node, transition.DeepCopy(), runner, config)
			continue
		}
		if err := c.annotate(ctx, root.object, profilingrun.AdmissionAnnotation, profilingrun.GrantedAdmission(now)); err != nil || start == nil {
			errs = append(errs, err)
			continue
		}
		start()
	}
	return errors.Join(errs...)
}

// held reports physical taint and power proof; resource conflicts use reservations.
func held(node *core.Node, in admissionInputs) bool {
	return profilingrun.InMeasurement(node) || powerModePending(node, in.classes, in.config, in.now)
}

// admissionInputs is what one Admit cycle read.
type admissionInputs struct {
	config   SchedulerControllerConfig
	pods     []core.Pod
	classes  []edge.DeviceClass
	roots    []measurementRoot
	profiles []serving.Profile
	now      time.Time
}

// rootRefusal judges a waiting root on a Node nothing holds: a refusal reason
// and detail, none to grant it, or wait while its catalog evidence lags.
// start, when set, runs a granted measurement ChillServer executes itself.
func (c *SchedulerController) rootRefusal(ctx context.Context, root measurementRoot, node *core.Node, in admissionInputs) (reason, detail string, wait bool, start func()) {
	if serving := servingPodsOn(in.pods, node.Name); len(serving) > 0 {
		return refusedNodeServing, strings.Join(serving, ","), false, nil
	}
	switch o := root.object.(type) {
	case *edge.OperationCostMeasurement:
		profiles, reason, detail, wait := c.operationCostRefusal(o, node, in)
		return reason, detail, wait, func() { c.startOperationCost(node, o.DeepCopy(), profiles, in) }
	case *edge.DispatchOccupancyMeasurement:
		plan, reason, detail, wait := c.occupancyRefusal(ctx, o, node, in)
		return reason, detail, wait, func() { c.startOccupancy(node, o.DeepCopy(), plan, in.config) }
	}
	reason, detail = cpuRefusal(root.object, node, in.classes, in.config.Reserved[node.Name].CPUs)
	return reason, detail, false, nil
}

// chillServerRun reports a measurement kind ChillServer runs itself.
func chillServerRun(object client.Object) bool {
	switch object.(type) {
	case *edge.OperationCostMeasurement, *edge.DispatchOccupancyMeasurement:
		return true
	}
	return false
}

// continueHeldNodes finishes the grant a restart cut off and starts the
// hand-back of every tainted Node whose measurement ended or vanished.
func (c *SchedulerController) continueHeldNodes(ctx context.Context, nodes []core.Node, byUID map[string]*measurementRoot, config SchedulerControllerConfig, now time.Time) []error {
	errs := make([]error, 0, len(nodes))
	for i := range nodes {
		node := &nodes[i]
		holder, tainted := profilingrun.MeasurementHolder(node)
		if !tainted || c.nodeExecuting(node) {
			continue
		}
		root := byUID[holder]
		if root != nil && !root.terminal {
			granted, _, _ := profilingrun.Admission(root.object.GetAnnotations())
			if granted && chillServerRun(root.object) {
				// No run of this process measures it: a restart cut it off.
				c.restartedMeasurement(node, root.object.DeepCopyObject().(client.Object), config)
				continue
			}
			if granted {
				byName := map[string]*core.Node{}
				for i := range nodes {
					byName[nodes[i].Name] = &nodes[i]
				}
				c.reserveMeasurementRoot(*root, node, byName, config)
				continue
			}
			if _, transition := root.object.(*edge.PowerModeTransitionCost); !transition && !chillServerRun(root.object) {
				// The taint was written and the grant was not: finish the grant.
				errs = append(errs, c.annotate(ctx, root.object, profilingrun.AdmissionAnnotation, profilingrun.GrantedAdmission(now)))
				continue
			}
			// A runner preparation or a measurement run a restart cut off
			// before its grant is handed back; the measurement then waits again.
		}
		if root != nil {
			// The hand-back patches its own copy; this cycle keeps reading the listed one.
			root = &measurementRoot{object: root.object.DeepCopyObject().(client.Object), node: root.node, terminal: root.terminal}
		}
		c.startHandBack(node, holder, root, config)
	}
	return errs
}

// nodeBusy reports a running run on the Node; Tick, Admit and hand-back then
// leave the Node to that run.
func (c *SchedulerController) nodeBusy(node *core.Node) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.runs {
		if r.reserved && slices.Contains(r.scope.Nodes, serving.NodeReference{Name: node.Name, UID: string(node.UID)}) {
			return true
		}
	}
	return false
}

// powerModePending reports a Node whose actuation agent applies a requested
// mode before its deadline (request time plus the mode's
// maxActuationDurationSeconds). A pending mode without a request time or a
// policy has no deadline.
func powerModePending(node *core.Node, classes []edge.DeviceClass, config SchedulerControllerConfig, now time.Time) bool {
	if node.Labels[chillmeta.PowerModeState] != chillmeta.PowerModePending {
		return false
	}
	maxDuration, ok := nodePowerTargets(node, classes, config.PowerPolicies)[node.Labels[chillmeta.PowerMode]]
	deadline, requested := chillmeta.PowerModeDeadline(node, maxDuration)
	return !ok || !requested || now.Before(deadline)
}

// servingPodsOn names the non-terminal CHILL serving Pods on the Node.
func servingPodsOn(pods []core.Pod, node string) []string {
	var names []string
	for i := range pods {
		p := &pods[i]
		if p.Spec.NodeName == node && p.Status.Phase != core.PodSucceeded && p.Status.Phase != core.PodFailed &&
			p.Labels["app.kubernetes.io/component"] == servingInstanceComponent {
			names = append(names, p.Namespace+"/"+p.Name)
		}
	}
	slices.Sort(names)
	return names
}

func (c *SchedulerController) annotate(ctx context.Context, object client.Object, keyValues ...string) error {
	annotations := map[string]string{}
	for i := 0; i+1 < len(keyValues); i += 2 {
		annotations[keyValues[i]] = keyValues[i+1]
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": annotations}})
	if err != nil {
		return err
	}
	return c.store.client.Patch(ctx, object, client.RawPatch(types.MergePatchType, patch))
}

// setMeasurementTaint adds the taint naming holder, or removes it when holder
// is empty. The patch carries the read resourceVersion: spec.taints is one
// atomic list other writers share, so a conflict retries next cycle.
func (c *SchedulerController) setMeasurementTaint(ctx context.Context, node *core.Node, holder string) error {
	taints := slices.DeleteFunc(slices.Clone(node.Spec.Taints), func(t core.Taint) bool { return t.Key == profilingrun.MeasurementTaintKey })
	if holder != "" {
		taints = append(taints, core.Taint{Key: profilingrun.MeasurementTaintKey, Value: holder, Effect: core.TaintEffectNoSchedule})
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"resourceVersion": node.ResourceVersion},
		"spec":     map[string]any{"taints": taints},
	})
	if err != nil {
		return err
	}
	_, err = c.kube.CoreV1().Nodes().Patch(ctx, node.Name, types.MergePatchType, patch, meta.PatchOptions{})
	return err
}

// startHandBack registers the hand-back of the Node as its run.
func (c *SchedulerController) startHandBack(node *core.Node, holder string, root *measurementRoot, config SchedulerControllerConfig) {
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	operation := Operation{ID: handBackKind + ":" + node.Name + ":" + holder, Kind: handBackKind, Node: ref, Holder: holder}
	c.startNodeRun(operation, config, func(ctx context.Context, result *OperationResult) error {
		return c.handBack(ctx, ref, holder, root, root == nil, result)
	})
}

// startNodeRun registers a run on the Node and executes it outside the locks.
// Callers hold tickMu, so Tick and Admit see the run before they look again.
// The returned result is final once done is closed.
func (c *SchedulerController) startNodeRun(operation Operation, config SchedulerControllerConfig, body func(context.Context, *OperationResult) error) (<-chan struct{}, *OperationResult) {
	done := make(chan struct{})
	started := time.Now()
	result := OperationResult{Operation: operation, StartedAt: started.UTC(), Status: nativeOperationRunning}
	runCtx, cancel := context.WithCancel(c.ctx)
	initial := result
	kind := operation.Kind
	switch kind {
	case operationUnroute, operationRelease, operationReset:
		kind = operationRoute
	}
	run := &actionRun{action: serving.Action{ID: operation.ID, Kind: kind, Node: operation.Node, From: externalActionSource, Status: nativeOperationRunning}, result: &initial, cancel: cancel, started: started}
	if operation.Holder != "" {
		run.owner = operation.Holder
	}
	c.mu.Lock()
	if !c.reserveRun(operation.ID, run, operation) {
		c.mu.Unlock()
		cancel()
		result.Status, result.Error = nativeOperationFailed, "OperationConflict: resources are reserved"
		result.EndedAt = time.Now().UTC()
		close(done)
		return done, &result
	}
	c.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(done)
		defer cancel()
		err := body(runCtx, &result)
		if result.EndedAt.IsZero() {
			result.EndedAt = time.Now().UTC()
		}
		if result.Status == nativeOperationRunning {
			result.Status = nativeOperationComplete
		}
		if err != nil {
			if result.Status == nativeOperationComplete {
				result.Status = nativeOperationFailed
			}
			result.Error = err.Error()
		}
		c.mu.Lock()
		c.recordPreparationLocked(run, result.Preparation)
		c.mu.Unlock()
		if config.OnAction != nil {
			if recordErr := config.OnAction(result); recordErr != nil {
				result.Error = errors.Join(err, fmt.Errorf("record operation outcome: %w", recordErr)).Error()
				if result.Status == nativeOperationComplete {
					result.Status = nativeOperationPartial
				}
			}
		}
		c.mu.Lock()
		run.result = &result
		run.action.Status, run.action.Reason = result.Status, result.Error
		if err == nil && result.Status == nativeOperationComplete || result.effectsNotStarted && !run.inheritedReservation {
			run.reserved = false
			if operation.Kind != runnerKind {
				c.releaseOwnerLocked(run.owner)
			}
		}
		run.action.Elapsed = result.EndedAt.Sub(result.StartedAt)
		c.state.Results = append(c.state.Results, result)
		c.mu.Unlock()
	}()
	return done, &result
}

// handBack returns a measured Node to the available Nodes: it waits for a
// power change in flight, removes the CHILL serving Pods the measurement left,
// lowers the Node and removes the taint. Each measurement record judges its
// own power-mode generation, so the hand-back judges none. A failed step keeps
// the taint; the next cycle starts the hand-back again.
//
// interrupted is set when the measurement object vanished: it is recorded as
// Interrupted.
func (c *SchedulerController) handBack(ctx context.Context, node serving.NodeReference, holder string, root *measurementRoot, interrupted bool, result *OperationResult) error {
	c.mu.Lock()
	config := c.config
	c.mu.Unlock()
	if err := c.operations.wait(ctx, func() (bool, error) {
		current, err := c.kube.CoreV1().Nodes().Get(ctx, node.Name, meta.GetOptions{})
		if err != nil {
			return false, err
		}
		var classes edge.DeviceClassList
		if err = c.store.client.List(ctx, &classes); err != nil {
			return false, err
		}
		return !powerModePending(current, classes.Items, config, time.Now()), nil
	}); err != nil {
		return err
	}
	if root == nil && interrupted {
		result.Progress = append(result.Progress, "Interrupted "+holder)
	}
	if err := errors.Join(c.deleteLoadJobs(ctx, node, config.LoadGenerator), c.operations.clearNode(ctx, node)); err != nil {
		return err
	}
	runners, err := c.kube.CoreV1().Services(c.observer.config.Namespace).List(ctx, meta.ListOptions{LabelSelector: runnerNodeLabel + "=" + node.UID})
	if err != nil {
		return err
	}
	for _, service := range runners.Items {
		if err = eIfNotFound(c.kube.CoreV1().Services(service.Namespace).Delete(ctx, service.Name, meta.DeleteOptions{})); err != nil {
			return err
		}
	}
	result.Progress = append(result.Progress, "serving Pods removed")
	current, err := c.kube.CoreV1().Nodes().Get(ctx, node.Name, meta.GetOptions{})
	if err != nil {
		return err
	}
	if string(current.UID) != node.UID {
		return errors.New("physical Node identity changed")
	}
	if value, tainted := profilingrun.MeasurementHolder(current); !tainted || value != holder {
		c.mu.Lock()
		c.releaseOwnerLocked(holder)
		c.mu.Unlock()
		return nil
	}
	if err = c.lowerMeasuredNode(ctx, current, holder, result); err != nil {
		return err
	}
	// The power change read the Node again; patch the current version.
	if current, err = c.kube.CoreV1().Nodes().Get(ctx, node.Name, meta.GetOptions{}); err != nil {
		return err
	}
	if err = c.setMeasurementTaint(ctx, current, ""); err != nil {
		return err
	}
	result.Progress = append(result.Progress, "taint removed")
	c.mu.Lock()
	c.releaseOwnerLocked(holder)
	c.mu.Unlock()
	return nil
}

// lowerMeasuredNode sets the Node to its lowest power mode and confirms the
// independent read-back, so a measurement that rebooted or failed hands back a
// known mode. A Node without a requestable lowest mode (a Fixed actuator, no
// unloaded baseline) skips the step.
func (c *SchedulerController) lowerMeasuredNode(ctx context.Context, node *core.Node, holder string, result *OperationResult) error {
	c.mu.Lock()
	config := c.config
	c.mu.Unlock()
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	var classes edge.DeviceClassList
	if err := c.store.client.List(ctx, &classes); err != nil {
		return err
	}
	lowest := serving.SchedulerLowestMode(config.Baselines, ref)
	maxDuration, ok := nodePowerTargets(node, classes.Items, config.PowerPolicies)[lowest]
	if lowest == "" || !ok {
		result.Progress = append(result.Progress, "no requestable lowest mode")
		return nil
	}
	if err := c.measurement.ChangePower(ctx, ref, holder, lowest, maxDuration); err != nil {
		return err
	}
	result.Progress = append(result.Progress, "lowest mode "+lowest+" read back")
	return nil
}

// clearNode removes every CHILL serving Pod on the Node: through the native
// removal when the runtime observes the instance, otherwise by deleting the Pod.
func (o *Operations) clearNode(ctx context.Context, node serving.NodeReference) error {
	pods, err := o.kube.CoreV1().Pods(o.config.Namespace).List(ctx, meta.ListOptions{
		LabelSelector: "app.kubernetes.io/component=" + servingInstanceComponent,
	})
	if err != nil {
		return err
	}
	observed := map[string]bool{}
	for _, i := range o.runtime.Instances() {
		observed[i.ID] = true
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Spec.NodeName != node.Name {
			continue
		}
		if status := firstfloorStatus(pod); status != nil && observed[physicalID(pod.UID, status.ContainerID)] {
			if err = o.RemoveInstance(ctx, node, physicalID(pod.UID, status.ContainerID)); err != nil {
				return err
			}
			continue
		}
		// Unobserved executors still need the same retained termination proof.
		// A preparation without an executor may request DELETE, but still waits
		// for terminal Pod status before releasing its source finalizer.
		if err = o.removeInstance(ctx, node, string(pod.UID), "", nil); err != nil {
			return err
		}
	}
	return nil
}

func (c *SchedulerController) measurementRootOperation(root measurementRoot, node *core.Node, nodes map[string]*core.Node, config SchedulerControllerConfig) Operation {
	holder := string(root.object.GetUID())
	operation := Operation{ID: measurementLeaseKind + "/" + holder, Kind: measurementLeaseKind, Holder: holder, Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}}
	if occupancy, ok := root.object.(*edge.DispatchOccupancyMeasurement); ok {
		for _, definition := range c.runtime.SchedulerInputs(time.Now).Workloads {
			if definition.Name == occupancy.Spec.Workload {
				operation.WorkloadUID = definition.UID
			}
		}
		if load := nodes[config.LoadGenerator.NodeName]; load != nil {
			operation.Nodes = append(operation.Nodes, serving.NodeReference{Name: load.Name, UID: string(load.UID)})
		}
	}
	return operation
}

func (c *SchedulerController) reserveMeasurementRoot(root measurementRoot, node *core.Node, nodes map[string]*core.Node, config SchedulerControllerConfig) bool {
	operation := c.measurementRootOperation(root, node, nodes, config)
	c.mu.Lock()
	defer c.mu.Unlock()
	if run := c.runs[operation.ID]; run != nil && run.reserved {
		return true
	}
	now := time.Now()
	run := &actionRun{owner: operation.Holder, action: serving.Action{ID: operation.ID, Kind: measurementLeaseKind, Node: operation.Node, Status: nativeOperationRunning}, result: &OperationResult{Operation: operation, StartedAt: now, Status: nativeOperationRunning}, started: now}
	return c.reserveRun(operation.ID, run, operation)
}
