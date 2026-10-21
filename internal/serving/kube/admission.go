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
	// handBackLabel marks the power change a hand-back requests to lower the Node.
	handBackLabel    = "chill.dacs.io/measurement-handback"
	holderLabelValue = "true"
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
	// Read the runs before the Pods: a run that ends after this read has its
	// Pods in the list below, and no run starts while tickMu is held.
	c.mu.Lock()
	config := c.config
	busy := map[string]bool{}
	for _, r := range c.runs {
		if r.action.Status == nativeOperationRunning {
			busy[r.action.Node.UID] = true
		}
	}
	c.mu.Unlock()
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
	acts, err := c.nodeActuations(ctx, config.MeasurementNamespaces)
	if err != nil {
		return err
	}
	roots, err := c.measurementRoots(ctx, config.MeasurementNamespaces)
	if err != nil {
		return err
	}
	var classes edge.DeviceClassList
	if err = c.store.client.List(ctx, &classes); err != nil {
		return err
	}
	now := time.Now()
	in := admissionInputs{config: config, pods: pods.Items, acts: acts, classes: classes.Items, roots: roots, profiles: c.observer.Profiles(), now: now}
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
		if claimed[node.Name] || profilingrun.InMeasurement(node) || busy[string(node.UID)] || pendingActuation(acts, string(node.UID), now) {
			claimed[node.Name] = true
			continue
		}
		reason, detail, wait := c.rootRefusal(root, node, in)
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
			if runner, reason = c.runnerPreparation(transition, node, config); reason != "" {
				errs = append(errs, c.annotate(ctx, root.object, profilingrun.AdmissionAnnotation, profilingrun.RefusedAdmission(reason, "")))
				continue
			}
		}
		cost, _ := root.object.(*edge.OperationCostMeasurement)
		var profiles map[string]string
		if cost != nil {
			profiles, _, _, _ = c.operationCostRefusal(cost, node, in)
		}
		claimed[node.Name] = true
		if err := c.setMeasurementTaint(ctx, node, string(root.object.GetUID())); err != nil {
			errs = append(errs, err)
			continue
		}
		if transition != nil {
			c.startRunner(node, transition.DeepCopy(), runner, config)
			continue
		}
		if err := c.annotate(ctx, root.object, profilingrun.AdmissionAnnotation, profilingrun.GrantedAdmission(now)); err != nil || cost == nil {
			errs = append(errs, err)
			continue
		}
		c.startOperationCost(node, cost.DeepCopy(), profiles, in)
	}
	return errors.Join(errs...)
}

// admissionInputs is what one Admit cycle read.
type admissionInputs struct {
	config   SchedulerControllerConfig
	pods     []core.Pod
	acts     []edge.NodeActuation
	classes  []edge.DeviceClass
	roots    []measurementRoot
	profiles []serving.Profile
	now      time.Time
}

// rootRefusal judges a waiting root on a Node nothing holds: a refusal reason
// and detail, none to grant it, or wait while its catalog evidence lags.
func (c *SchedulerController) rootRefusal(root measurementRoot, node *core.Node, in admissionInputs) (reason, detail string, wait bool) {
	if serving := servingPodsOn(in.pods, node.Name); len(serving) > 0 {
		return refusedNodeServing, strings.Join(serving, ","), false
	}
	if cost, ok := root.object.(*edge.OperationCostMeasurement); ok {
		_, reason, detail, wait = c.operationCostRefusal(cost, node, in)
		return reason, detail, wait
	}
	reason, detail = cpuRefusal(root.object, node, in.classes, in.acts, in.config.Reserved[node.Name].CPUs)
	return reason, detail, false
}

// continueHeldNodes finishes the grant a restart cut off and starts the
// hand-back of every tainted Node whose measurement ended or vanished.
func (c *SchedulerController) continueHeldNodes(ctx context.Context, nodes []core.Node, byUID map[string]*measurementRoot, config SchedulerControllerConfig, now time.Time) []error {
	errs := make([]error, 0, len(nodes))
	for i := range nodes {
		node := &nodes[i]
		holder, tainted := profilingrun.MeasurementHolder(node)
		if !tainted || c.nodeBusy(node) {
			continue
		}
		root := byUID[holder]
		if root != nil && !root.terminal {
			granted, _, _ := profilingrun.Admission(root.object.GetAnnotations())
			cost, _ := root.object.(*edge.OperationCostMeasurement)
			if granted && cost != nil {
				// No run of this process measures it: a restart cut it off.
				c.restartedOperationCost(node, cost.DeepCopy(), config)
				continue
			}
			if granted {
				continue
			}
			if _, transition := root.object.(*edge.PowerModeTransitionCost); !transition && cost == nil {
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
		if r.action.Status == nativeOperationRunning && r.action.Node.UID == string(node.UID) {
			return true
		}
	}
	return false
}

// pendingActuation reports a power change of the Node that is neither Ready
// nor Failed. Its deadline bounds the wait: the actuator fails it by then.
func pendingActuation(acts []edge.NodeActuation, nodeUID string, now time.Time) bool {
	for i := range acts {
		a := &acts[i]
		if a.Spec.Node.UID == nodeUID && a.Status.Phase != edge.NodeActuationPhaseReady && a.Status.Phase != edge.NodeActuationPhaseFailed && now.Before(a.Spec.Deadline.Time) {
			return true
		}
	}
	return false
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
	operation := Operation{ID: handBackKind + ":" + node.Name + ":" + holder, Kind: handBackKind, Node: ref}
	c.startNodeRun(operation, config, func(ctx context.Context, result *OperationResult) error {
		return c.handBack(ctx, ref, holder, root, root == nil, result)
	})
}

// nodeActuations lists the power changes of the actuation namespace and of the
// measurement namespaces, where measurements request their own.
func (c *SchedulerController) nodeActuations(ctx context.Context, measurementNamespaces []string) ([]edge.NodeActuation, error) {
	namespaces := append([]string{c.observer.config.ActuationNamespace}, measurementNamespaces...)
	slices.Sort(namespaces)
	var acts []edge.NodeActuation
	for _, namespace := range slices.Compact(namespaces) {
		var list edge.NodeActuationList
		if err := c.store.client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
			return nil, err
		}
		acts = append(acts, list.Items...)
	}
	return acts, nil
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
	run := &actionRun{action: serving.Action{ID: operation.ID, Kind: operation.Kind, Node: operation.Node, Status: nativeOperationRunning}, result: &initial, cancel: cancel, started: started}
	c.mu.Lock()
	c.runs[operation.ID] = run
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
		if config.OnAction != nil {
			if recordErr := config.OnAction(result); recordErr != nil {
				result.Error = errors.Join(err, fmt.Errorf("record operation outcome: %w", recordErr)).Error()
			}
		}
		c.mu.Lock()
		run.result = &result
		run.action.Status, run.action.Reason = result.Status, result.Error
		run.action.Elapsed = result.EndedAt.Sub(result.StartedAt)
		c.state.Results = append(c.state.Results, result)
		c.mu.Unlock()
	}()
	return done, &result
}

// handBack returns a measured Node to the available Nodes: it waits for power
// changes in flight, marks the measurement invalid when another request changed
// the Node's power inside it, removes the CHILL serving Pods the measurement
// left, and removes the taint. A failed step keeps the taint; the next cycle
// starts the hand-back again.
//
// interrupted is set when the measurement object vanished: it is recorded as
// Interrupted.
func (c *SchedulerController) handBack(ctx context.Context, node serving.NodeReference, holder string, root *measurementRoot, interrupted bool, result *OperationResult) error {
	c.mu.Lock()
	namespaces := c.config.MeasurementNamespaces
	c.mu.Unlock()
	var acts []edge.NodeActuation
	if err := c.operations.wait(ctx, func() (bool, error) {
		var err error
		if acts, err = c.nodeActuations(ctx, namespaces); err != nil {
			return false, err
		}
		return !pendingActuation(acts, node.UID, time.Now()), nil
	}); err != nil {
		return err
	}
	if root == nil && interrupted {
		result.Progress = append(result.Progress, "Interrupted "+holder)
	} else if root != nil {
		granted, at, _ := profilingrun.Admission(root.object.GetAnnotations())
		if foreign := foreignActuations(acts, node.UID, root.object.GetUID(), at, result.StartedAt); granted && len(foreign) > 0 {
			if err := c.annotate(ctx, root.object, profilingrun.InvalidAnnotation, powerChangedReason+" "+strings.Join(foreign, ",")); err != nil {
				return err
			}
			result.Progress = append(result.Progress, "invalid: "+powerChangedReason)
		}
	}
	if err := c.operations.clearNode(ctx, node); err != nil {
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
		return nil
	}
	if err = c.lowerMeasuredNode(ctx, current, result); err != nil {
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
	return nil
}

// lowerMeasuredNode sets the Node to its lowest power mode and confirms the
// independent read-back, so a measurement that rebooted or failed hands back a
// known mode. A Node without a requestable lowest mode (a Fixed actuator, no
// unloaded baseline) skips the step.
func (c *SchedulerController) lowerMeasuredNode(ctx context.Context, node *core.Node, result *OperationResult) error {
	c.mu.Lock()
	config := c.config
	c.mu.Unlock()
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	var classes edge.DeviceClassList
	if err := c.store.client.List(ctx, &classes); err != nil {
		return err
	}
	lowest := serving.SchedulerLowestMode(config.Baselines, ref)
	spec, ok := nodePowerTargets(node, classes.Items, config.PowerPolicies)[lowest]
	if lowest == "" || !ok {
		result.Progress = append(result.Progress, "no requestable lowest mode")
		return nil
	}
	request := powerRequest(spec, time.Now())
	request.Labels = map[string]string{handBackLabel: holderLabelValue}
	if err := c.measurement.ChangePower(ctx, ref, request); err != nil {
		return err
	}
	result.Progress = append(result.Progress, "lowest mode "+lowest+" read back")
	return nil
}

// foreignActuations names the power changes of the Node applied between from
// and to that neither the measurement nor a hand-back requested. A retried
// hand-back finds the lowering of the attempt before it inside the window.
func foreignActuations(acts []edge.NodeActuation, nodeUID string, measurement types.UID, from, to time.Time) []string {
	names := make([]string, 0, len(acts))
	for i := range acts {
		a := &acts[i]
		if a.Spec.Node.UID != nodeUID || a.Status.ApplyStartedAt == nil || a.Status.ApplyStartedAt.Time.Before(from) || a.Status.ApplyStartedAt.Time.After(to) {
			continue
		}
		if a.Labels[handBackLabel] != "" || slices.ContainsFunc(a.OwnerReferences, func(o meta.OwnerReference) bool { return o.UID == measurement }) {
			continue
		}
		names = append(names, a.Name)
	}
	slices.Sort(names)
	return names
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
		patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"uid": pod.UID, "$deleteFromPrimitiveList/finalizers": []string{sourceFinalizer}}})
		if err != nil {
			return err
		}
		pods := o.kube.CoreV1().Pods(pod.Namespace)
		if _, err = pods.Patch(ctx, pod.Name, types.StrategicMergePatchType, patch, meta.PatchOptions{}); eIfNotFound(err) != nil {
			return err
		}
		if err = eIfNotFound(pods.Delete(ctx, pod.Name, meta.DeleteOptions{Preconditions: &meta.Preconditions{UID: &pod.UID}})); err != nil {
			return err
		}
		if err = deleteInstanceService(ctx, o.kube, pod); err != nil {
			return err
		}
		if err = eIfNotFound(o.kube.CoreV1().ConfigMaps(pod.Namespace).Delete(ctx, pod.Name, meta.DeleteOptions{})); err != nil {
			return err
		}
	}
	return nil
}
