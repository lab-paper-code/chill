package kube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SchedulerControllerConfig struct {
	Settings               serving.SchedulerSettings
	CPUInventory           map[string][]int
	GPUInventory           map[string][]string
	Reserved               map[string]serving.SchedulerAllocation
	Preparations           map[string]PrepareSpec
	Costs                  []serving.OperationCost
	Baselines              []serving.NodeBaseline
	Meters                 []MeterDeclaration
	PowerRequests          map[string]*edge.NodeActuation
	StaticRouting          bool
	CompletionCalibrations map[string]map[string]serving.ObservedOverhead
	CalibrationErrors      map[string]string
	OnDecision             func(serving.SchedulerDecision) error
	OnAction               func(OperationResult) error
}
type SchedulerControllerState struct {
	Snapshot serving.SchedulerSnapshot `json:"snapshot"`
	Decision serving.SchedulerDecision `json:"decision"`
	Actions  []serving.Action          `json:"actions"`
	Results  []OperationResult         `json:"results"`
}
type actionRun struct {
	action  serving.Action
	result  *OperationResult
	cancel  context.CancelFunc
	started time.Time
}
type SchedulerController struct {
	mu         sync.Mutex
	tickMu     sync.Mutex
	store      *Store
	kube       kubernetes.Interface
	runtime    *serving.Runtime
	observer   *Observer
	operations *Operations
	journal    *serving.Journal
	config     SchedulerControllerConfig
	state      SchedulerControllerState
	runs       map[string]*actionRun
	goal       *serving.SchedulerCandidate
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

func NewSchedulerController(store *Store, kube kubernetes.Interface, runtime *serving.Runtime, observer *Observer, operations *Operations, journal *serving.Journal, config SchedulerControllerConfig) (*SchedulerController, error) {
	if store == nil || kube == nil || runtime == nil || observer == nil || operations == nil || journal == nil {
		return nil, errors.New("scheduler requires actual Kubernetes, runtime, observer, operation and journal owners")
	}
	if err := validateSchedulerConfiguration(config); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	if config.Settings.Controls.Enabled {
		runtime.EnableSchedulerRouting()
	}
	return &SchedulerController{store: store, kube: kube, runtime: runtime, observer: observer, operations: operations, journal: journal, config: config, runs: map[string]*actionRun{}, ctx: ctx, cancel: cancel}, nil
}
func (c *SchedulerController) Settings() serving.SchedulerSettings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.config.Settings
}
func (c *SchedulerController) SetSettings(settings serving.SchedulerSettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	c.mu.Lock()
	before := c.config.Settings.Controls.Enabled
	c.config.Settings = settings
	c.mu.Unlock()
	c.setRoutingControl(before, settings.Controls.Enabled)
	return nil
}
func (c *SchedulerController) SetConfiguration(config SchedulerControllerConfig) error {
	if err := validateSchedulerConfiguration(config); err != nil {
		return err
	}
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	c.mu.Lock()
	before := c.config.Settings.Controls.Enabled
	previousStatic := c.config.StaticRouting
	c.config = config
	c.mu.Unlock()
	c.setRoutingControl(before, config.Settings.Controls.Enabled)
	if previousStatic && !config.StaticRouting && !config.Settings.Controls.Enabled && !before {
		c.runtime.DisableSchedulerRouting()
	}
	return nil
}
func (c *SchedulerController) setRoutingControl(before, after bool) {
	if before == after {
		return
	}
	if after {
		c.runtime.EnableSchedulerRouting()
	} else {
		c.mu.Lock()
		for _, run := range c.runs {
			if run.action.Status == nativeOperationRunning && run.action.Cancelable && run.cancel != nil {
				run.cancel()
			}
		}
		c.mu.Unlock()
		c.mu.Lock()
		static := c.config.StaticRouting
		c.mu.Unlock()
		if !static {
			c.runtime.DisableSchedulerRouting()
		}
	}
}
func validateSchedulerConfiguration(config SchedulerControllerConfig) error {
	if err := config.Settings.Validate(); err != nil {
		return err
	}
	meters := map[string]serving.NodeReference{}
	for _, m := range config.Meters {
		if m.ID == "" || m.Scope != measurementWallScope || m.Node.Name == "" || m.Node.UID == "" || m.Source.NodeName != m.Node.Name {
			return errors.New("exact physical meter declaration required")
		}
		if node, ok := meters[m.ID]; ok && node != m.Node {
			return errors.New("meter reused across physical Nodes")
		}
		meters[m.ID] = m.Node
		for _, other := range config.Meters {
			if other.Node != m.Node && sameWallSeries(other.Source, m.Source) {
				return errors.New("one wall series declared for multiple physical Nodes")
			}
		}
	}
	for _, b := range config.Baselines {
		if b.Meter == "" || b.Scope != measurementWallScope {
			return errors.New("baseline meter declaration required")
		}
		if n, ok := meters[b.Meter]; ok && n != b.Node {
			return errors.New("baseline meter reused across physical Nodes")
		}
		meters[b.Meter] = b.Node
	}
	for _, cost := range config.Costs {
		if cost.Meter == "" || cost.Scope != measurementWallScope {
			return errors.New("cost meter declaration required")
		}
		if n, ok := meters[cost.Meter]; ok && n != cost.Node {
			return errors.New("cost meter reused across physical Nodes")
		}
		meters[cost.Meter] = cost.Node
	}
	return nil
}
func (c *SchedulerController) State() SchedulerControllerState {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := c.state
	result.Actions = c.actionsLocked(time.Now())
	result.Results = append([]OperationResult(nil), c.state.Results...)
	return result
}
func (c *SchedulerController) Close() { c.cancel(); c.wg.Wait() }
func (c *SchedulerController) actionsLocked(now time.Time) []serving.Action {
	result := make([]serving.Action, 0, len(c.runs))
	for _, r := range c.runs {
		a := r.action
		if a.Status == nativeOperationRunning {
			a.Elapsed = now.Sub(r.started)
		}
		result = append(result, a)
	}
	slices.SortFunc(result, func(a, b serving.Action) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return result
}
func (c *SchedulerController) Tick(ctx context.Context, now time.Time) (serving.SchedulerDecision, error) {
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	if err := c.observer.Refresh(ctx); err != nil {
		return serving.SchedulerDecision{}, err
	}
	c.mu.Lock()
	config := c.config
	goal := c.goal
	c.mu.Unlock()
	snapshot, err := c.capture(ctx, now, config)
	if err != nil {
		return serving.SchedulerDecision{}, err
	}
	snapshot = c.reconcileSnapshot(snapshot, goal, now, config.Settings.ObservationWindow)
	decision := serving.EvaluateScheduler(config.Settings, snapshot)
	c.mu.Lock()
	c.state.Snapshot = snapshot
	c.state.Decision = decision
	c.mu.Unlock()
	if config.OnDecision != nil {
		if err := config.OnDecision(decision); err != nil {
			return decision, fmt.Errorf("persist decision before physical action: %w", err)
		}
	}
	if config.Settings.Controls.Enabled && decision.Target != nil {
		c.launch(*decision.Target, config)
	}
	return decision, nil
}
func (c *SchedulerController) reconcileSnapshot(snapshot serving.SchedulerSnapshot, goal *serving.SchedulerCandidate, now time.Time, observationWindow time.Duration) serving.SchedulerSnapshot {
	c.mu.Lock()
	snapshot.InProgressTarget = nil
	if goal == nil {
		goal = c.activeTargetLocked("")
		c.goal = goal
	}
	if goal != nil {
		for _, desired := range goal.Actions {
			if run := c.runs[desired.ID]; run != nil && run.action.Status != nativeOperationRunning && run.action.Status != nativeOperationCancel && run.result != nil && run.result.Status != nativeOperationComplete {
				goal = nil
				c.goal = nil
				break
			}
		}
	}
	for id, run := range c.runs {
		if run.action.Status != nativeOperationRunning && run.action.Status != nativeOperationCancel && !c.actionEffectPresent(snapshot, run) {
			delete(c.runs, id)
		}
	}
	actions := c.actionsLocked(now)
	snapshot.Actions = actions
	if goal != nil {
		pending := false
		for _, desired := range goal.Actions {
			if desired.Status == nativeOperationCancel {
				continue
			}
			run := c.runs[desired.ID]
			if run == nil || run.action.Status != nativeOperationComplete {
				pending = true
				break
			}
		}
		if pending {
			snapshot.InProgressTarget = goal
		} else {
			c.goal = nil
		}
	}
	if routing := c.activeTargetLocked(operationRoute); routing != nil {
		snapshot.InProgressTarget = routing
	}
	cutoff := now.Add(-observationWindow)
	c.state.Results = slices.DeleteFunc(c.state.Results, func(r OperationResult) bool { return r.EndedAt.Before(cutoff) })
	for id, run := range c.runs {
		if run.action.Status == nativeOperationRunning || run.action.Status == nativeOperationCancel || run.result == nil || !run.result.EndedAt.Before(cutoff) {
			continue
		}
		needed := false
		if snapshot.InProgressTarget != nil {
			for _, a := range snapshot.InProgressTarget.Actions {
				if a.ID == id {
					needed = true
					break
				}
			}
		}
		if !needed {
			delete(c.runs, id)
		}
	}
	c.mu.Unlock()
	return snapshot
}

func (c *SchedulerController) activeTargetLocked(kind string) *serving.SchedulerCandidate {
	var active *actionRun
	for _, run := range c.runs {
		if run.action.Status != nativeOperationRunning || (kind != "" && run.action.Kind != kind) || run.result == nil || run.result.Operation.Target == nil {
			continue
		}
		if active == nil || run.started.Before(active.started) {
			active = run
		}
	}
	if active == nil {
		return nil
	}
	return active.result.Operation.Target
}
func (c *SchedulerController) capture(ctx context.Context, now time.Time, config SchedulerControllerConfig) (serving.SchedulerSnapshot, error) {
	arrivals, err := c.journal.SchedulerArrivals(now.Add(-config.Settings.ObservationWindow), now)
	if err != nil {
		return serving.SchedulerSnapshot{}, err
	}
	s := serving.SchedulerSnapshot{At: now, Workloads: c.runtime.SchedulerDefinitions(), Outstanding: c.runtime.SchedulerOutstanding(now), Arrivals: arrivals, Costs: config.Costs, Baselines: config.Baselines}
	s.Routing, s.RoutingCredits = c.runtime.SchedulerRouting()
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, meta.ListOptions{})
	if err != nil {
		return s, err
	}
	pods, err := c.kube.CoreV1().Pods("").List(ctx, meta.ListOptions{FieldSelector: nonTerminalPodSelector})
	if err != nil {
		return s, err
	}
	var acts edge.NodeActuationList
	if err = c.store.client.List(ctx, &acts, client.InNamespace(c.observer.config.ActuationNamespace)); err != nil {
		return s, err
	}
	profiles := c.observer.Profiles()
	profileByID := map[string]*serving.Profile{}
	for i := range profiles {
		profileByID[profiles[i].ID] = &profiles[i]
	}
	physical := map[string]serving.InstanceState{}
	for _, pool := range c.runtime.Pools() {
		for _, state := range pool.Instances {
			physical[state.ID] = state
		}
	}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		pool := n.Labels[ServingPoolLabel]
		if pool == "" {
			continue
		}
		sn := serving.SchedulerNode{Node: serving.NodeReference{Name: n.Name, UID: string(n.UID)}, Pool: pool, CPUs: slices.Clone(config.CPUInventory[n.Name]), GPUUUIDs: slices.Clone(config.GPUInventory[n.Name]), Reserved: config.Reserved[n.Name], MemoryBytes: uint64(n.Status.Allocatable.Memory().Value()), ObservedAt: now}
		for _, condition := range n.Status.Conditions {
			if condition.Type == core.NodeReady {
				sn.Ready = condition.Status == core.ConditionTrue
			}
		}
		if len(sn.CPUs) == 0 || int64(len(sn.CPUs)) > n.Status.Allocatable.Cpu().Value() {
			sn.Reason = "physical CPU inventory unavailable or exceeds actual allocatable budget"
			s.Reasons = append(s.Reasons, n.Name+": "+sn.Reason)
		}
		c.captureNodePower(ctx, &s, &sn, n, &acts, physical, profileByID)
		gpuQuantity := n.Status.Allocatable["nvidia.com/gpu"]
		if int64(len(sn.GPUUUIDs)) > gpuQuantity.Value() {
			sn.Reason = "physical GPU inventory exceeds actual device allocation"
			sn.Ready = false
			s.Reasons = append(s.Reasons, n.Name+": "+sn.Reason)
		}
		captureNodePods(&s, &sn, n, pods, physical, profileByID)
		s.Nodes = append(s.Nodes, sn)
	}
	for _, profile := range profiles {
		template, ok := config.Preparations[profile.ID]
		if !ok {
			continue
		}
		instance := template.Instance
		instance.Profile = profileByID[profile.ID]
		for _, node := range s.Nodes {
			if node.Node == instance.Node {
				if instance.Pool == "" {
					instance.Pool = node.Pool
				}
				break
			}
		}
		instance.ObservedAt = now
		var compatible []string
		for _, d := range s.Workloads {
			if serving.MatchesProfile(d, instance.Profile) {
				compatible = append(compatible, d.UID)
			}
		}
		if len(compatible) > 0 {
			s.Options = append(s.Options, serving.SchedulerOption{Instance: instance, CompatibleUIDs: compatible})
		}
	}
	if err = c.captureCompletionOverhead(&s, physical, config); err != nil {
		return s, err
	}
	return s, nil
}

func (c *SchedulerController) captureCompletionOverhead(
	s *serving.SchedulerSnapshot, physical map[string]serving.InstanceState, config SchedulerControllerConfig,
) error {
	overheads, err := c.journal.ObservedOverhead(s.At.Add(-config.Settings.ObservationWindow), s.At)
	if err != nil {
		return err
	}
	for profile, facts := range config.CompletionCalibrations {
		for id, fact := range facts {
			overheads["calibration/"+profile+"/"+id] = fact
		}
	}
	for index := range s.Instances {
		instance := &s.Instances[index]
		observeOverhead(instance, overheads)
		if instance.Profile != nil && !instance.CompletionObserved && config.CalibrationErrors[instance.Profile.ID] != "" {
			instance.EnvelopeSource = "completion calibration Pending: " + config.CalibrationErrors[instance.Profile.ID]
		}
		state := physical[instance.ID]
		instance.ArrivalAges = map[string][]time.Duration{}
		if state.Native != nil {
			for uid, ages := range state.Native.ArrivalAgesMS {
				for _, age := range ages {
					instance.ArrivalAges[uid] = append(instance.ArrivalAges[uid], time.Duration(age*float64(time.Millisecond)))
				}
			}
		}
		bindProfileMeter(instance, config.Meters)
	}
	for index := range s.Options {
		observeOverhead(&s.Options[index].Instance, overheads)
		if instance := &s.Options[index].Instance; instance.Profile != nil &&
			!instance.CompletionObserved && config.CalibrationErrors[instance.Profile.ID] != "" {
			instance.EnvelopeSource = "completion calibration Pending: " + config.CalibrationErrors[instance.Profile.ID]
		}
		bindProfileMeter(&s.Options[index].Instance, config.Meters)
	}
	return nil
}
func (c *SchedulerController) captureNodePower(ctx context.Context, s *serving.SchedulerSnapshot, sn *serving.SchedulerNode, n *core.Node, acts *edge.NodeActuationList, physical map[string]serving.InstanceState, profileByID map[string]*serving.Profile) {
	var latest *edge.NodeActuation
	for j := range acts.Items {
		a := &acts.Items[j]
		if a.Spec.Node.UID == string(n.UID) && (latest == nil || a.Spec.StageRequestedAt.After(latest.Spec.StageRequestedAt.Time)) {
			latest = a
		}
	}
	if latest != nil && latest.Status.Phase == edge.NodeActuationPhaseReady && latest.Status.TargetObservation != nil {
		sn.PowerMode = latest.Status.TargetObservation.PowerMode
	}
	// A fixed mode is accepted only from an actually verified ready profile.
	for _, state := range physical {
		if state.Node == sn.Node && state.Ready {
			if p := profileByID[state.Profile]; p != nil && sn.PowerMode == "" {
				sn.PowerMode = p.Condition.PowerMode
			}
		}
	}
	if sn.PowerMode == "" {
		actual, e := c.operations.Capture(ctx, sn.Node)
		if e != nil || actual.PowerMode == "" {
			s.Reasons = append(s.Reasons, n.Name+": current power state unavailable")
		} else {
			sn.PowerMode = actual.PowerMode
		}
	}
}
func captureNodePods(s *serving.SchedulerSnapshot, sn *serving.SchedulerNode, n *core.Node, pods *core.PodList, physical map[string]serving.InstanceState, profileByID map[string]*serving.Profile) {
	var nonServingMilliCPU int64
	var nonServingMemory uint64
	for j := range pods.Items {
		p := &pods.Items[j]
		if p.Spec.NodeName != n.Name || p.Status.Phase == core.PodSucceeded || p.Status.Phase == core.PodFailed {
			continue
		}
		status := firstfloorStatus(p)
		id := ""
		if status != nil {
			id = physicalID(p.UID, status.ContainerID)
		}
		state, isServing := physical[id]
		if isServing && p.Labels["app.kubernetes.io/component"] == servingInstanceComponent {
			profile := profileByID[state.Profile]
			if profile == nil || state.Native == nil {
				s.Reasons = append(s.Reasons, p.Name+": actual native/profile snapshot unavailable")
				continue
			}
			m := state.Native.Metadata
			allocation := serving.SchedulerAllocation{CPUs: slices.Clone(m.Runtime.CPUAffinity), GPUUUID: m.Runtime.GPUUUID, Threads: m.Runtime.IntraOpThreads, MemoryBytes: uint64(p.Spec.Containers[0].Resources.Limits.Memory().Value())}
			instance := serving.SchedulerInstance{ID: state.ID, Node: state.Node, Pool: state.Pool, Profile: profile, Allocation: allocation, Ready: state.Ready && state.Native.Ready, Capacity: m.MaxQueueRequests, Weight: 1, MaxBatch: m.MaxBatchSize, ObservedAt: state.ObservedAt, MaxWait: time.Duration(m.Policy.MaxBatchWaitMS * float64(time.Millisecond)), CompletionOverhead: time.Duration(m.Policy.CompletionOverheadMS * float64(time.Millisecond)), EnvelopeSource: m.Policy.EnvelopeSource}
			instance.TargetID, instance.OwnerWorkloadUID = state.TargetID, state.OwnerWorkloadUID
			instance.Stopped = state.Stopped
			if m.Policy.MaxBatchSize > 0 {
				instance.MaxBatch = m.Policy.MaxBatchSize
			}
			for _, container := range p.Spec.Containers {
				if container.Name == firstfloorContainerName {
					instance.Allocation.CPUCount = int((container.Resources.Requests.Cpu().MilliValue() + 999) / 1000)
				}
			}
			weight, e := strconv.Atoi(p.Annotations[ServingWeightAnnotation])
			if e == nil {
				instance.Weight = int32(weight)
			}
			for _, w := range state.Workloads {
				instance.WorkloadUIDs = append(instance.WorkloadUIDs, w.UID)
			}
			s.Instances = append(s.Instances, instance)
			continue
		}
		milliCPU, memory, gpu := podBudget(p)
		if gpu {
			sn.Ready = false
			sn.Reason = "non-serving physical GPU ownership unobserved"
			// Placement uncertainty quarantines this Node, not unrelated candidates.
		}
		nonServingMilliCPU += milliCPU
		if math.MaxUint64-nonServingMemory < uint64(memory) {
			sn.Ready = false
			sn.Reason = "non-serving memory budget overflow"
			s.Reasons = append(s.Reasons, n.Name+": "+sn.Reason)
			return
		}
		nonServingMemory += uint64(memory)
	}
	// Quota requests are an aggregate count budget, not observed affinity.
	// Round once; the explicit reservation must cover the entire milliCPU sum.
	if nonServingMilliCPU > 0 || nonServingMemory > 0 {
		sn.NonServing = append(sn.NonServing, serving.SchedulerAllocation{CPUCount: int((nonServingMilliCPU + 999) / 1000), CPUReserved: len(sn.Reserved.CPUs) > 0, MemoryBytes: nonServingMemory})
	}
	if nonServingMilliCPU > int64(len(sn.Reserved.CPUs))*1000 {
		sn.Ready = false
		sn.Reason = "non-serving CPU affinity unknown and exclusive reservation insufficient"
		s.Reasons = append(s.Reasons, n.Name+": "+sn.Reason)
	}
}
func actionEquivalent(a, b serving.Action) bool {
	return a.Kind == b.Kind && a.Node == b.Node && a.Instance == b.Instance && a.From == b.From && a.To == b.To
}
func slotName(slot string) string {
	sum := sha256.Sum256([]byte(slot))
	return "serving-" + hex.EncodeToString(sum[:10])
}
func (c *SchedulerController) launch(target serving.SchedulerCandidate, config SchedulerControllerConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if target.Keep && len(target.Actions) == 0 {
		return
	}
	c.goal = &target
	// Decide eligible old-source stops before starting policy refreshes that can
	// supersede the existing acknowledged handoff proof in this same tick.
	for phase := range 2 {
		for _, action := range target.Actions {
			if (action.Kind == operationStop) != (phase == 0) {
				continue
			}
			if action.Status == nativeOperationCancel {
				if r := c.runs[action.ID]; r != nil && r.action.Cancelable && r.cancel != nil {
					r.cancel()
					r.action.Status = nativeOperationCancel
				}
				continue
			}
			if action.Status == nativeOperationComplete || action.Status == nativeOperationFailed {
				continue
			}
			existing := false
			for _, r := range c.runs {
				if actionEquivalent(r.action, action) && r.action.Status == nativeOperationRunning {
					existing = true
					break
				}
			}
			if existing {
				continue
			}
			ready := c.dependenciesReady(action, target)
			if !ready {
				continue
			}
			// Global policy writes share one ConfigMap/ACK path. Independent Node
			// preparations remain concurrent; one mutation per Node stays serialized.
			for _, r := range c.runs {
				if r.action.Status == nativeOperationRunning && ((action.Node.UID != "" && r.action.Node == action.Node) || (action.Kind == operationRoute && r.action.Kind == operationRoute)) {
					ready = false
				}
			}
			if !ready {
				continue
			}
			operation := scheduledOperation(action, &target, config)
			runCtx, cancel := context.WithCancel(c.ctx)
			action.Status = nativeOperationRunning
			action.Cancelable = action.Kind == operationPrepare
			started := time.Now()
			initial := OperationResult{Operation: operation, StartedAt: started.UTC(), Status: "starting"}
			run := &actionRun{action: action, result: &initial, cancel: cancel, started: started}
			c.runs[action.ID] = run
			c.wg.Add(1)
			go func(operation Operation, run *actionRun) {
				defer c.wg.Done()
				result := initial
				var err error
				if config.OnAction != nil {
					err = config.OnAction(result)
				}
				if err == nil {
					result, err = c.operations.Execute(runCtx, operation)
				} else {
					result.Status = nativeOperationFailed
					result.Error = "persist action start before physical effect: " + err.Error()
					result.EndedAt = time.Now().UTC()
				}
				if config.OnAction != nil {
					if recordErr := config.OnAction(result); recordErr != nil {
						err = errors.Join(err, fmt.Errorf("record physical operation outcome: %w", recordErr))
						result.Error = err.Error()
						if result.Status == nativeOperationComplete {
							result.Status = nativeOperationPartial
						}
					}
				}
				c.mu.Lock()
				run.result = &result
				run.action.Status = nativeOperationComplete
				if err != nil {
					run.action.Status = result.Status
					run.action.Reason = err.Error()
				}
				run.action.Elapsed = result.EndedAt.Sub(result.StartedAt)
				c.state.Results = append(c.state.Results, result)
				c.mu.Unlock()
			}(operation, run)
		}
	}
}

func (c *SchedulerController) dependenciesReady(action serving.Action, target serving.SchedulerCandidate) bool {
	for _, dep := range action.DependsOn {
		if run := c.runs[dep]; run != nil && run.action.Status == nativeOperationComplete {
			continue
		}
		route := slices.ContainsFunc(target.Actions, func(a serving.Action) bool {
			return a.ID == dep && a.Kind == operationRoute
		})
		if action.Kind != operationStop || !route || !c.completedHandoffPresent(target) {
			return false
		}
	}
	return true
}

func (c *SchedulerController) completedHandoffPresent(target serving.SchedulerCandidate) bool {
	var physical []serving.InstanceState
	for _, pool := range c.runtime.Pools() {
		physical = append(physical, pool.Instances...)
	}
	if !c.routingTargetPresent(&target, c.state.Snapshot, physical, false) {
		return false
	}
	for _, run := range c.runs {
		if run.action.Kind == operationRoute && run.action.Status == nativeOperationComplete &&
			run.result != nil && run.result.Status == nativeOperationComplete &&
			c.routingTargetPresent(run.result.Operation.Target, c.state.Snapshot, physical, true) {
			return true
		}
	}
	return false
}
func scheduledOperation(action serving.Action, target *serving.SchedulerCandidate, config SchedulerControllerConfig) Operation {
	operation := Operation{ID: action.ID, Kind: action.Kind, Node: action.Node, Instance: action.Instance, Pool: action.To, Target: target}
	if action.Kind == operationPrepare {
		for _, i := range target.Instances {
			if i.ID == action.Instance && i.Profile != nil && i.Profile.Condition.Firstfloor != nil {
				template, ok := config.Preparations[i.Profile.ID]
				if !ok {
					continue
				}
				template.Instance = i
				template.Name = slotName(i.ID)
				template.Config.Policy = native.Policy{ProfileID: i.Profile.ID, DecisionID: target.ID,
					DeadlinePolicy: native.EffectiveDeadlinePolicy(i.Profile.Condition.Firstfloor.DeadlinePolicy),
					MaxBatchSize:   i.MaxBatch, MaxBatchWaitMS: float64(i.MaxWait) / float64(time.Millisecond),
					CompletionOverheadMS: float64(i.CompletionOverhead) / float64(time.Millisecond),
					EnvelopeSource:       i.CompletionPolicySource()}
				operation.Prepare = &template
				break
			}
		}
	}
	if action.Kind == operationPower {
		operation.Actuation = config.PowerRequests[action.Node.UID+"/"+action.To]
	}
	return operation
}

// observeOverhead uses recent completed requests on the exact physical Node and
// immutable measured profile, including executors that no longer exist. Frozen
// model/input definitions establish compatibility; workload UID and SLO are not
// execution conditions. No matching measurement leaves the bootstrap unobserved.
func observeOverhead(instance *serving.SchedulerInstance, overheads map[string]serving.ObservedOverhead) {
	instance.CompletionObserved = false
	instance.CompletionMeasurement = nil
	observedID := ""
	for id, o := range overheads {
		if !o.Matches(*instance) {
			continue
		}
		if instance.CompletionObserved && (o.Completion < instance.CompletionOverhead || o.Completion == instance.CompletionOverhead && id >= observedID) {
			continue
		}
		source := o.Source
		if id != instance.ID {
			source = "inherited from " + id + ": " + source
		}
		instance.CompletionOverhead, instance.EnvelopeSource, instance.CompletionObserved = o.Completion, source, true
		instance.CompletionMeasurement = &o
		observedID = id
	}
}
func bindProfileMeter(instance *serving.SchedulerInstance, meters []MeterDeclaration) {
	if instance.Profile == nil {
		return
	}
	for _, meter := range meters {
		if meter.Node == instance.Node && meter.Scope == measurementWallScope && reflect.DeepEqual(meter.Source, instance.Profile.MetricSource) {
			instance.IdleMeter = meter.ID
			instance.IdleScope = meter.Scope
			return
		}
	}
}
func sameWallSeries(a, b metricobserver.SourceIdentity) bool {
	return a.Kind == b.Kind && a.Endpoint == b.Endpoint && a.Metric == b.Metric && reflect.DeepEqual(a.MetricLabels, b.MetricLabels)
}
func (c *SchedulerController) actionEffectPresent(snapshot serving.SchedulerSnapshot, run *actionRun) bool {
	if run.result == nil || run.result.Status != nativeOperationComplete {
		return false
	}
	a := run.action
	var physical []serving.InstanceState
	for _, pool := range c.runtime.Pools() {
		physical = append(physical, pool.Instances...)
	}
	switch a.Kind {
	case operationPrepare:
		p := run.result.Operation.Prepare
		if p == nil || p.Instance.Profile == nil {
			return false
		}
		for _, i := range physical {
			if (i.ID == a.Instance || i.TargetID == a.Instance) && i.Node == a.Node && i.Profile == p.Instance.Profile.ID && i.Native != nil && i.Native.Ready {
				return true
			}
		}
	case operationRemove:
		for _, i := range physical {
			if i.ID == a.Instance {
				return false
			}
		}
		return true
	case operationStop:
		for _, i := range physical {
			if i.ID == a.Instance {
				return i.Stopped
			}
		}
		return true
	case operationDrain:
		for _, r := range snapshot.Outstanding {
			if r.Instance == a.Instance {
				return false
			}
		}
		return true
	case operationMembership:
		for _, n := range snapshot.Nodes {
			if n.Node == a.Node {
				return n.Pool == a.To
			}
		}
	case operationPower:
		for _, n := range snapshot.Nodes {
			if n.Node == a.Node {
				return n.PowerMode == a.To
			}
		}
	case operationRoute:
		return c.routingTargetPresent(run.result.Operation.Target, snapshot, physical, true)
	}
	return false
}
func (c *SchedulerController) routingTargetPresent(target *serving.SchedulerCandidate, snapshot serving.SchedulerSnapshot, physical []serving.InstanceState, requirePolicy bool) bool {
	if target == nil {
		return false
	}
	actual, _ := c.runtime.SchedulerRouting()
	if len(actual) != len(target.Routing) {
		return false
	}
	for uid, weights := range target.Routing {
		if len(actual[uid]) != len(weights) {
			return false
		}
		for id, weight := range weights {
			state := schedulerPhysicalState(id, physical)
			if state == nil {
				return false
			}
			resolved := state.ID
			if actual[uid][resolved] != weight {
				return false
			}
		}
	}
	for _, wanted := range target.Instances {
		if !schedulerTargetReady(wanted, snapshot, physical, requirePolicy) {
			return false
		}
	}
	return true
}

func schedulerPhysicalState(id string, physical []serving.InstanceState) *serving.InstanceState {
	var state *serving.InstanceState
	for index := range physical {
		if physical[index].ID == id || physical[index].TargetID == id {
			if state != nil {
				return nil
			}
			state = &physical[index]
		}
	}
	return state
}

func schedulerTargetReady(wanted serving.SchedulerInstance, snapshot serving.SchedulerSnapshot, physical []serving.InstanceState, requirePolicy bool) bool {
	if wanted.Profile == nil || !wanted.CompletionObserved || !wanted.CompletionMeasurement.Valid() ||
		wanted.CompletionOverhead != wanted.CompletionMeasurement.Completion || !wanted.CompletionMeasurement.Matches(wanted) {
		return false
	}
	state := schedulerPhysicalState(wanted.ID, physical)
	if state == nil || !state.Ready || state.Stopped || state.Native == nil || !state.Native.Ready {
		return false
	}
	captured := slices.ContainsFunc(snapshot.Instances, func(actual serving.SchedulerInstance) bool {
		return actual.ID == state.ID && actual.Ready && !actual.Stopped && actual.Node == wanted.Node &&
			actual.Pool == wanted.Pool && serving.SchedulerInstanceState(actual) == serving.SchedulerInstanceState(wanted)
	})
	if !captured || state.Native.Metadata.ProfileID != wanted.Profile.ID || state.Native.Metadata.MaxQueueRequests != wanted.Capacity {
		return false
	}
	return !requirePolicy || schedulerPolicyAcknowledged(wanted, state.Native.Metadata)
}

func schedulerPolicyAcknowledged(wanted serving.SchedulerInstance, actual native.Metadata) bool {
	if wanted.Profile.Condition.Firstfloor == nil {
		return false
	}
	maxBatch := actual.Policy.MaxBatchSize
	if maxBatch == 0 {
		maxBatch = actual.MaxBatchSize
	}
	return actual.Policy.ProfileID == wanted.Profile.ID && maxBatch == wanted.MaxBatch &&
		actual.Policy.MaxBatchWaitMS == float64(wanted.MaxWait)/float64(time.Millisecond) &&
		actual.Policy.CompletionOverheadMS == float64(wanted.CompletionOverhead)/float64(time.Millisecond) &&
		native.EffectiveDeadlinePolicy(actual.Policy.DeadlinePolicy) == native.EffectiveDeadlinePolicy(wanted.Profile.Condition.Firstfloor.DeadlinePolicy)
}
func podBudget(pod *core.Pod) (int64, int64, bool) {
	var cpu, memory, sideCPU, sideMemory, initCPU, initMemory int64
	gpu := false
	for _, container := range pod.Spec.Containers {
		cpu += container.Resources.Requests.Cpu().MilliValue()
		memory += container.Resources.Requests.Memory().Value()
		q := container.Resources.Requests["nvidia.com/gpu"]
		gpu = gpu || q.Sign() > 0
	}
	for _, container := range pod.Spec.InitContainers {
		c, m := container.Resources.Requests.Cpu().MilliValue(), container.Resources.Requests.Memory().Value()
		q := container.Resources.Requests["nvidia.com/gpu"]
		gpu = gpu || q.Sign() > 0
		if container.RestartPolicy != nil && *container.RestartPolicy == core.ContainerRestartPolicyAlways {
			sideCPU += c
			sideMemory += m
			c = sideCPU
			m = sideMemory
		} else {
			c += sideCPU
			m += sideMemory
		}
		initCPU = max(initCPU, c)
		initMemory = max(initMemory, m)
	}
	cpu = max(cpu+sideCPU, initCPU) + pod.Spec.Overhead.Cpu().MilliValue()
	memory = max(memory+sideMemory, initMemory) + pod.Spec.Overhead.Memory().Value()
	return cpu, memory, gpu
}
