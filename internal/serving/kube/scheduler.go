package kube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/cpuset"
)

const controlPlaneLabel = "node-role.kubernetes.io/control-plane"

// Intermediate routes are observed operations, not prerequisites of the frozen
// goal: later ready subsets supersede their routing effects.
const readySubsetRoute = "ready-subset"

type SchedulerControllerConfig struct {
	Settings     serving.SchedulerSettings
	Reserved     map[string]serving.SchedulerAllocation
	Preparations map[string]PrepareSpec
	Costs        []serving.OperationCost
	Baselines    []serving.NodeBaseline
	CostIssues   []CostIssue
	// DispatchOccupancy are the measured dispatch occupancies.
	DispatchOccupancy []serving.DispatchOccupancy
	// CatalogIssues are the profiles the catalog or the preparations omit.
	CatalogIssues []CatalogIssue
	LoadGenerator LoadGeneratorConfig
	// GatewayRequestTimeout is the actual HTTP handler lifetime cap.
	GatewayRequestTimeout time.Duration
	Meters                []MeterDeclaration
	// PowerPolicies are the configured actuation policies; a non-Fixed
	// DeviceClass mode with a policy is a mode ChillServer can request.
	PowerPolicies []nodeactuator.ActuationPolicySpec
	StaticRouting bool
	// MeasurementNamespaces hold the measurement objects Admit grants Nodes to.
	MeasurementNamespaces []string
	// Evidence stores operation measurements and sequence records in EvidenceStore;
	// MeterSource reads a configured wall meter.
	Evidence      measurementEvidence
	EvidenceStore string
	MeterSource   func(metricobserver.SourceIdentity) (metricobserver.Source, error)
	OnDecision    func(serving.SchedulerDecision) error
	OnAction      func(OperationResult) error
}
type SchedulerControllerState struct {
	Snapshot serving.SchedulerSnapshot `json:"snapshot"`
	Decision serving.SchedulerDecision `json:"decision"`
	Actions  []serving.Action          `json:"actions"`
	Results  []OperationResult         `json:"results"`
	// CostIssues are the measurements the configured costs omit.
	CostIssues []CostIssue `json:"costIssues"`
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
	// measurement acts only on measurement POOL Nodes: operation measurements and hand-back.
	measurement    *Operations
	observationLog *serving.ObservationLog
	capacity       *serving.CapacityTable
	config         SchedulerControllerConfig
	state          SchedulerControllerState
	runs           map[string]*actionRun
	goal           *serving.SchedulerCandidate
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	// powerTargets holds, by "<Node UID>/<mode>", the maximum actuation
	// duration of each mode the last capture found requestable.
	powerTargets map[string]time.Duration
	// measureStep measures one step of an operation-cost measurement.
	measureStep func(context.Context, SchedulerControllerConfig, MeasurementRequest) (OperationMeasurement, evidencestore.Receipt, error)
	// unrouted holds when each captured instance last lost every route weight.
	// overloaded holds the slots prepared while keep was in Overload: zero
	// until the instance is Ready, then the end of its hold.
	unrouted, overloaded map[string]time.Time
}

// NewSchedulerController configures control without taking ownership of routing.
// The composition root freezes routing after restoring the actual baseline.
func NewSchedulerController(store *Store, kube kubernetes.Interface, runtime *serving.Runtime, observer *Observer, operations *Operations, observationLog *serving.ObservationLog, config SchedulerControllerConfig) (*SchedulerController, error) {
	if store == nil || kube == nil || runtime == nil || observer == nil || operations == nil || observationLog == nil {
		return nil, errors.New("scheduler requires actual Kubernetes, runtime, observer, operation and observation log owners")
	}
	if err := validateSchedulerConfiguration(config); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	measurement := *operations
	measurement.config.Measurement = true
	c := &SchedulerController{store: store, kube: kube, runtime: runtime, observer: observer, operations: operations, measurement: &measurement, observationLog: observationLog, config: config, runs: map[string]*actionRun{}, ctx: ctx, cancel: cancel}
	c.measureStep = c.measureOperation
	c.capacity = serving.NewCapacityTable(goruntime.GOMAXPROCS(0), func(row serving.CapacityRow) error {
		return observationLog.ManagementEvent(serving.ManagementEvent{WorkloadUID: row.Input.WorkloadUID, Kind: serving.CapacityRowBuiltEvent, Phase: row.Status, At: row.BuiltAt, Capacity: &row})
	}, observationLog.CapacityRecords)
	return c, nil
}
func (c *SchedulerController) Settings() serving.SchedulerSettings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.config.Settings
}
func (c *SchedulerController) SetConfiguration(config SchedulerControllerConfig) error {
	if err := validateSchedulerConfiguration(config); err != nil {
		return err
	}
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	c.mu.Lock()
	before := len(c.config.Settings.AutonomousWorkloads) > 0
	previousStatic := c.config.StaticRouting
	c.config = config
	c.mu.Unlock()
	after := len(config.Settings.AutonomousWorkloads) > 0
	c.setRoutingControl(before, after)
	if previousStatic && !config.StaticRouting && !after && !before {
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
	result.CostIssues = c.config.CostIssues
	return result
}
func (c *SchedulerController) Close() { c.cancel(); c.capacity.Wait(); c.wg.Wait() }

// Capacity returns the capacity rows the autonomous Workloads need.
func (c *SchedulerController) Capacity() []serving.CapacityRow { return c.capacity.Rows() }
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
	if err := ctx.Err(); err != nil {
		return serving.SchedulerDecision{}, err
	}
	c.mu.Lock()
	config := c.config
	goal := c.goal
	c.mu.Unlock()
	started := time.Now()
	snapshot, err := c.capture(ctx, now, config)
	if err != nil {
		return serving.SchedulerDecision{}, err
	}
	if err := ctx.Err(); err != nil {
		return serving.SchedulerDecision{}, err
	}
	snapshot, lost := c.reconcileSnapshot(snapshot, goal, snapshot.At, config.Settings.ObservationWindow)
	for _, result := range lost {
		if config.OnAction != nil {
			if err := config.OnAction(result); err != nil {
				return serving.SchedulerDecision{}, fmt.Errorf("record lost action effect: %w", err)
			}
		}
	}
	c.annotateHistory(&snapshot, config.Settings.ObservationWindow)
	c.capacity.Ensure(c.ctx, config.Settings, snapshot)
	decision, err := serving.EvaluateScheduler(ctx, config.Settings, snapshot, c.capacity.Rows())
	if err != nil {
		return serving.SchedulerDecision{}, err
	}
	decision.EvaluationElapsed = time.Since(started)
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return serving.SchedulerDecision{}, err
	}
	c.state.Snapshot = snapshot
	c.state.Decision = decision
	c.markOverloadPrepares(decision)
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return serving.SchedulerDecision{}, err
	}
	if config.OnDecision != nil {
		if err := config.OnDecision(decision); err != nil {
			return decision, fmt.Errorf("persist decision before physical action: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return serving.SchedulerDecision{}, err
	}
	// A decision without a target still lowers available Nodes (Fixed actions).
	if decision.Target != nil {
		c.launch(*decision.Target, config)
	} else {
		keep := decision.Keep
		keep.Actions = slices.DeleteFunc(slices.Clone(keep.Actions), func(a serving.Action) bool { return !a.Fixed })
		c.launch(keep, config)
	}
	return decision, nil
}

// annotateHistory sets each captured instance's UnroutedSince and HeldUntil
// from the controller's records and drops records of instances that left.
// A restart starts the records again, so an instance waits one more window.
func (c *SchedulerController) annotateHistory(s *serving.SchedulerSnapshot, window time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unrouted == nil {
		c.unrouted, c.overloaded = map[string]time.Time{}, map[string]time.Time{}
	}
	for key, until := range c.overloaded {
		if !until.IsZero() && !s.At.Before(until) {
			delete(c.overloaded, key)
		}
	}
	present := map[string]bool{}
	for k := range s.Instances {
		i := &s.Instances[k]
		present[i.ID] = true
		routed := false
		for _, weights := range s.Routing {
			routed = routed || weights[i.ID] > 0
		}
		if _, seen := c.unrouted[i.ID]; routed {
			delete(c.unrouted, i.ID)
		} else if !seen {
			c.unrouted[i.ID] = s.At
		}
		if since, ok := c.unrouted[i.ID]; ok {
			i.UnroutedSince = &since
		}
		for _, key := range []string{i.ID, i.TargetID} {
			until, ok := c.overloaded[key]
			if key == "" || !ok {
				continue
			}
			if until.IsZero() && i.Ready {
				until = s.At.Add(window)
				c.overloaded[key] = until
			}
			if !until.IsZero() {
				i.HeldUntil = &until
			}
		}
	}
	for id := range c.unrouted {
		if !present[id] {
			delete(c.unrouted, id)
		}
	}
	// A mark whose preparation neither runs nor waits in the goal is dropped.
	for key, until := range c.overloaded {
		preparing := c.goal != nil && slices.ContainsFunc(c.goal.Actions, func(a serving.Action) bool {
			return a.Kind == operationPrepare && a.Instance == key
		})
		for _, r := range c.runs {
			preparing = preparing || r.action.Kind == operationPrepare && r.action.Instance == key
		}
		if until.IsZero() && !preparing {
			delete(c.overloaded, key)
		}
	}
}

// markOverloadPrepares records the slots a decision prepares while keep is in
// Overload; once Ready they are held for one observation window.
func (c *SchedulerController) markOverloadPrepares(decision serving.SchedulerDecision) {
	if decision.Target == nil || decision.Keep.Status != serving.SchedulerStatusOverload {
		return
	}
	if c.overloaded == nil {
		c.unrouted, c.overloaded = map[string]time.Time{}, map[string]time.Time{}
	}
	for _, a := range decision.Target.Actions {
		if _, marked := c.overloaded[a.Instance]; a.Kind == operationPrepare && !marked {
			c.overloaded[a.Instance] = time.Time{}
		}
	}
}

// reconcileSnapshot attaches the run status and the goal still in progress. A
// goal action whose completed effect is gone (its instance died or left Ready)
// fails, the goal is abandoned and lost returns those failures. Failed runs
// stay in snapshot.Actions for one observation window.
func (c *SchedulerController) reconcileSnapshot(snapshot serving.SchedulerSnapshot, goal *serving.SchedulerCandidate, now time.Time, observationWindow time.Duration) (serving.SchedulerSnapshot, []OperationResult) {
	c.mu.Lock()
	snapshot.InProgressTarget = nil
	if goal == nil {
		goal = c.activeTargetLocked("")
		c.goal = goal
	}
	var lost []OperationResult
	if goal != nil {
		var failed bool
		if lost, failed = c.failGoalLocked(snapshot, goal, now); failed {
			goal = nil
			c.goal = nil
		}
	}
	for id, run := range c.runs {
		if run.action.Status == nativeOperationComplete && !c.actionEffectPresent(snapshot, run) {
			delete(c.runs, id)
		}
	}
	actions := c.actionsLocked(now)
	snapshot.Actions = actions
	if goal != nil {
		pending := false
		for _, desired := range goal.Actions {
			if desired.Status == nativeOperationCancel || desired.From == readySubsetRoute {
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
	if routing := c.activeTargetLocked(operationRoute); routing != nil &&
		(goal == nil || len(routing.Actions) != 1 || routing.Actions[0].From != readySubsetRoute) {
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
	return snapshot, lost
}

// failGoalLocked marks goal actions whose completed effect is gone as failed,
// returns them, and reports whether any goal action failed.
func (c *SchedulerController) failGoalLocked(snapshot serving.SchedulerSnapshot, goal *serving.SchedulerCandidate, now time.Time) ([]OperationResult, bool) {
	var lost []OperationResult
	failed := false
	for _, desired := range goal.Actions {
		if desired.From == readySubsetRoute {
			continue
		}
		run := c.runs[desired.ID]
		if run == nil || run.action.Status == nativeOperationRunning || run.action.Status == nativeOperationCancel || run.result == nil {
			continue
		}
		// Fixed lowering is undone by the goal's own raise of the same Node.
		if !desired.Fixed && run.result.Status == nativeOperationComplete && !c.actionEffectPresent(snapshot, run) {
			result := *run.result
			result.Status, result.Error, result.EndedAt = nativeOperationFailed, "completed effect lost", now
			run.result, run.action.Status, run.action.Reason = &result, nativeOperationFailed, result.Error
			lost = append(lost, result)
		}
		failed = failed || run.result.Status != nativeOperationComplete
	}
	return lost, failed
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
	arrivals, err := c.observationLog.SchedulerArrivals(now.Add(-config.Settings.ObservationWindow), now)
	if err != nil {
		return serving.SchedulerSnapshot{}, err
	}
	s := serving.SchedulerSnapshot{ObservationEnd: now, ObservedSince: c.observationLog.Started(), Arrivals: arrivals, Costs: config.Costs, Baselines: config.Baselines,
		DispatchOccupancy: config.DispatchOccupancy}
	// Historical reads must finish before refreshing authoritative live sources.
	if err = c.observer.Refresh(ctx); err != nil {
		return s, err
	}
	// Observer state is read before the Node list: another refresh holding the
	// observer lock would otherwise age every Node.
	profiles := c.observer.Profiles()
	// Request start is a conservative lower bound; slow live I/O still ages it.
	nodesObservedAt := time.Now().UTC()
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, meta.ListOptions{})
	if err != nil {
		return s, err
	}
	pods, err := c.kube.CoreV1().Pods("").List(ctx, meta.ListOptions{FieldSelector: nonTerminalPodSelector})
	if err != nil {
		return s, err
	}
	var classes edge.DeviceClassList
	if err = c.store.client.List(ctx, &classes); err != nil {
		return s, err
	}
	power := map[string]time.Duration{}
	profileByID := map[string]*serving.Profile{}
	for i := range profiles {
		profileByID[profiles[i].ID] = &profiles[i]
	}
	physical := map[string]serving.InstanceState{}
	for _, state := range c.runtime.Instances() {
		physical[state.ID] = state
	}
	// The entire POOL is every Node without the control-plane role.
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if _, controlPlane := n.Labels[controlPlaneLabel]; controlPlane {
			continue
		}
		sn := serving.SchedulerNode{Node: serving.NodeReference{Name: n.Name, UID: string(n.UID)}, Measurement: profilingrun.InMeasurement(n), Reserved: config.Reserved[n.Name], MemoryBytes: uint64(n.Status.Allocatable.Memory().Value()), ObservedAt: nodesObservedAt}
		for _, condition := range n.Status.Conditions {
			if condition.Type == core.NodeReady {
				sn.Ready = condition.Status == core.ConditionTrue
			}
		}
		if slices.Contains(config.Settings.ExcludedNodes, n.Name) {
			sn.Reasons = append(sn.Reasons, serving.NodeExcluded)
		}
		captureNodeInventory(&sn, n, profiles)
		sn.ModeCPUs = declaredModeCPUs(nodeClass(n, classes.Items))
		sn.PowerMode = readBackMode(n, nodeClass(n, classes.Items))
		lowest := serving.SchedulerLowestMode(config.Baselines, sn.Node)
		targets := nodePowerTargets(n, classes.Items, config.PowerPolicies)
		if _, ok := targets[lowest]; sn.PowerMode == "" || len(targets) == 0 || lowest != "" && lowest != sn.PowerMode && !ok {
			sn.Reasons = append(sn.Reasons, serving.NodePowerModeUnavailable)
		}
		for mode, maxDuration := range targets {
			power[sn.Node.UID+"/"+mode] = maxDuration
		}
		captureNodePods(&s, &sn, n, pods, physical, profileByID)
		s.Nodes = append(s.Nodes, sn)
	}
	c.mu.Lock()
	c.powerTargets = power
	c.mu.Unlock()
	current := c.runtime.SchedulerInputs(time.Now)
	s.At, s.Workloads, s.Outstanding = current.At, current.Workloads, current.Outstanding
	s.Routing, s.RoutingCredits = current.Routing, current.RoutingCredits
	for _, profile := range profiles {
		template, ok := config.Preparations[profile.ID]
		// A strict fixed-size preparation is a baseline, never an autonomous candidate.
		if !ok || template.Config.Policy.FixedBatchSize > 0 {
			continue
		}
		instance := template.Instance
		instance.Profile = profileByID[profile.ID]
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
	bindProfileMeters(&s, config.Meters)
	return s, nil
}

// nodePowerTargets returns, by mode, the maximum actuation duration of each
// mode the Node can be switched into: the non-Fixed modes of its one
// DeviceClass that have a configured actuation policy.
func nodePowerTargets(n *core.Node, classes []edge.DeviceClass, policies []nodeactuator.ActuationPolicySpec) map[string]time.Duration {
	class := nodeClass(n, classes)
	if class == nil {
		return nil
	}
	targets := map[string]time.Duration{}
	for _, mode := range class.Spec.PowerModes {
		for _, p := range policies {
			if mode.Actuator.Kind != edge.PowerModeActuatorFixed && p.DeviceClass == class.Name && p.PowerMode == mode.Name &&
				p.ActuatorKind == string(mode.Actuator.Kind) {
				targets[mode.Name] = time.Duration(p.MaxActuationDurationSeconds) * time.Second
			}
		}
	}
	return targets
}

// captureNodeInventory reads the online CPU list the node agent publishes and
// the GPUs the Node's profiles were measured on. A missing or unreadable list,
// more devices than allocatable, or a reservation outside them leaves the
// Node without placement inventory.
func captureNodeInventory(sn *serving.SchedulerNode, n *core.Node, profiles []serving.Profile) {
	cpus, err := cpuset.Parse(n.Annotations[chillmeta.OnlineCPUs])
	sn.CPUs, sn.GPUUUIDs = cpus.List(), measuredGPUs(profiles, sn.Node.UID)
	gpuQuantity := n.Status.Allocatable["nvidia.com/gpu"]
	if err != nil || len(sn.CPUs) == 0 || int64(len(sn.CPUs)) > n.Status.Allocatable.Cpu().Value() ||
		int64(len(sn.GPUUUIDs)) > gpuQuantity.Value() || !cpuset.New(sn.Reserved.CPUs...).IsSubsetOf(cpus) ||
		sn.Reserved.GPUUUID != "" && !slices.Contains(sn.GPUUUIDs, sn.Reserved.GPUUUID) {
		sn.Reasons = append(sn.Reasons, serving.NodePlacementInventoryMissing)
	}
}

// declaredModeCPUs holds, by mode, the online CPUs the Node's DeviceClass
// declares; a plan into another mode places on them.
func declaredModeCPUs(class *edge.DeviceClass) map[string][]int {
	if class == nil {
		return nil
	}
	var modes map[string][]int
	for _, m := range class.Spec.PowerModes {
		if cpus, err := cpuset.Parse(m.OnlineCPUs); m.OnlineCPUs != "" && err == nil {
			if modes == nil {
				modes = map[string][]int{}
			}
			modes[m.Name] = cpus.List()
		}
	}
	return modes
}

// measuredGPUs is the GPU inventory of one Node: the GPUs its verified
// profiles were measured on.
func measuredGPUs(profiles []serving.Profile, nodeUID string) []string {
	var gpus []string
	for _, p := range profiles {
		if p.Condition.NodeUID == nodeUID && p.RuntimeMetadata != nil && p.RuntimeMetadata.GPUUUID != "" &&
			!slices.Contains(gpus, p.RuntimeMetadata.GPUUUID) {
			gpus = append(gpus, p.RuntimeMetadata.GPUUUID)
		}
	}
	slices.Sort(gpus)
	return gpus
}

func bindProfileMeters(s *serving.SchedulerSnapshot, meters []MeterDeclaration) {
	for index := range s.Instances {
		bindProfileMeter(&s.Instances[index], meters)
	}
	for index := range s.Options {
		bindProfileMeter(&s.Options[index].Instance, meters)
	}
}

// captureNodePods records CHILL serving instances and the non-serving budget.
// A Pod CHILL did not place leaves the Node in the POOL; when its GPU or CPU use
// cannot be admitted the Node gets PlacementBudgetUnavailable.
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
				sn.Reasons = appendReason(sn.Reasons, serving.NodeInstanceUnavailable)
				continue
			}
			m := state.Native.Metadata
			allocation := serving.SchedulerAllocation{CPUs: slices.Clone(m.Runtime.CPUAffinity), GPUUUID: m.Runtime.GPUUUID, Threads: m.Runtime.IntraOpThreads, MemoryBytes: uint64(p.Spec.Containers[0].Resources.Limits.Memory().Value())}
			instance := serving.SchedulerInstance{ID: state.ID, Node: state.Node, Profile: profile, Allocation: allocation, Ready: state.Ready && state.Native.Ready, Capacity: m.MaxQueueRequests, Weight: 1, MaxBatch: m.MaxBatchSize, ObservedAt: state.ObservedAt, MaxWait: time.Duration(m.Policy.MaxBatchWaitMS * float64(time.Millisecond))}
			instance.TargetID, instance.OwnerWorkloadUID = state.TargetID, state.OwnerWorkloadUID
			instance.Stopped, instance.FixedBatchSize = state.Stopped, m.Policy.FixedBatchSize
			if m.Policy.MaxBatchSize > 0 {
				instance.MaxBatch = m.Policy.MaxBatchSize
			}
			for _, container := range p.Spec.Containers {
				if container.Name == firstfloorContainerName {
					// Kubernetes request budget is independent of native affinity
					// width and ORT threads; retain all three observed quantities.
					instance.Allocation.CPUCount = int(container.Resources.Requests.Cpu().Value())
				}
			}
			weight, e := strconv.Atoi(p.Annotations[ServingWeightAnnotation])
			if e == nil {
				instance.Weight = int32(weight)
			}
			for _, w := range state.Workloads {
				if instance.WorkloadUID == "" || w.UID < instance.WorkloadUID {
					instance.WorkloadUID = w.UID
				}
			}
			s.Instances = append(s.Instances, instance)
			continue
		}
		milliCPU, memory, gpu := podBudget(p)
		nonServingMilliCPU += milliCPU
		if gpu {
			sn.Reasons = appendReason(sn.Reasons, serving.NodePlacementBudgetUnavailable)
		}
		if math.MaxUint64-nonServingMemory < uint64(memory) {
			sn.Reasons = appendReason(sn.Reasons, serving.NodePlacementBudgetUnavailable)
			continue
		}
		nonServingMemory += uint64(memory)
	}
	// Quota requests are an aggregate count budget, not observed affinity.
	// Round once; the explicit reservation must cover the entire milliCPU sum.
	if nonServingMilliCPU > 0 || nonServingMemory > 0 {
		sn.NonServing = append(sn.NonServing, serving.SchedulerAllocation{CPUCount: int((nonServingMilliCPU + 999) / 1000), CPUReserved: len(sn.Reserved.CPUs) > 0, MemoryBytes: nonServingMemory})
	}
	if nonServingMilliCPU > int64(len(sn.Reserved.CPUs))*1000 {
		sn.Reasons = appendReason(sn.Reasons, serving.NodePlacementBudgetUnavailable)
	}
}

func appendReason(reasons []string, reason string) []string {
	if slices.Contains(reasons, reason) {
		return reasons
	}
	return append(reasons, reason)
}

// ValidatePreparationCapacity admits coexistence using fresh physical Pod
// requests and the owner's configured inventory/reservations, not CPU isolation.
// Only serving-namespace Pods establish serving ownership; all Pod requests count.
// It never retires an existing serving instance.
func ValidatePreparationCapacity(
	servingNamespace string,
	node *core.Node, pods *core.PodList, inventory serving.SchedulerNode, wanted serving.SchedulerInstance,
) error {
	if inventory.Node != wanted.Node || string(node.UID) != wanted.Node.UID || node.Name != wanted.Node.Name {
		return errors.New("preparation inventory differs from the current physical Node")
	}
	ready := false
	for _, condition := range node.Status.Conditions {
		if condition.Type == core.NodeMemoryPressure && condition.Status == core.ConditionTrue {
			return errors.New("preparation target has actual Node memory pressure")
		}
		ready = ready || condition.Type == core.NodeReady && condition.Status == core.ConditionTrue
	}
	memory := node.Status.Allocatable.Memory().Value()
	gpus := node.Status.Allocatable["nvidia.com/gpu"]
	if !ready || len(inventory.CPUs) == 0 || int64(len(inventory.CPUs)) > node.Status.Allocatable.Cpu().Value() ||
		memory <= 0 || int64(len(inventory.GPUUUIDs)) > gpus.Value() {
		return errors.New("preparation requires current Node readiness and bounded physical CPU/memory inventory")
	}
	for _, pod := range pods.Items {
		if pod.Namespace == servingNamespace && pod.Spec.NodeName == node.Name &&
			pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed &&
			pod.Labels["app.kubernetes.io/component"] == servingInstanceComponent {
			return errors.New("preparation target already has an active serving Pod")
		}
	}
	// Discard the older aggregate; the same capture path recomputes all current
	// foreign/system Pod requests, init/sidecar maxima and overhead exactly once.
	inventory.NonServing = nil
	inventory.Reasons = nil
	inventory.MemoryBytes = uint64(memory)
	var snapshot serving.SchedulerSnapshot
	captureNodePods(&snapshot, &inventory, node, pods, nil, nil)
	if len(inventory.Reasons) != 0 {
		return fmt.Errorf("preparation non-serving admission: %s", strings.Join(inventory.Reasons, "; "))
	}
	allocations := append([]serving.SchedulerAllocation{inventory.Reserved}, inventory.NonServing...)
	allocations = append(allocations, wanted.Allocation)
	if reasons := serving.ValidateNodeBudget(inventory, allocations, nil); len(reasons) != 0 {
		return fmt.Errorf("preparation declared Node budget: %s", strings.Join(reasons, "; "))
	}
	return nil
}

func actionEquivalent(a, b serving.Action) bool {
	return a.Kind == b.Kind && a.Node == b.Node && a.Instance == b.Instance && a.From == b.From && a.To == b.To
}
func slotName(slot string) string {
	sum := sha256.Sum256([]byte(slot))
	return "serving-" + hex.EncodeToString(sum[:10])
}

func (c *SchedulerController) conflictingActionRunning(action serving.Action) bool {
	// Global policy writes share one ConfigMap/ACK path. Independent Node
	// preparations remain concurrent; one mutation per Node stays serialized.
	for _, r := range c.runs {
		if r.action.Status == nativeOperationRunning && ((action.Node.UID != "" && r.action.Node == action.Node) ||
			((action.Kind == operationRoute || action.Kind == operationStop) && r.action.Kind == operationRoute)) {
			return true
		}
	}
	return false
}

func (c *SchedulerController) launch(target serving.SchedulerCandidate, config SchedulerControllerConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(target.Actions) == 0 {
		return
	}
	// Fixed lowering alone is no transition to track.
	if !target.Keep || slices.ContainsFunc(target.Actions, func(a serving.Action) bool { return !a.Fixed }) {
		c.goal = &target
	}
	actions, intermediate := c.intermediateActions(target)
	// Decide eligible old-source stops before starting policy refreshes that can
	// supersede the existing acknowledged handoff proof in this same tick.
	for phase := range 2 {
		for _, action := range actions {
			if (action.Kind == operationStop) != (phase == 0) {
				continue
			}
			// Evaluation can retain a running snapshot after its live run finishes.
			// Reconciliation, not a stale target, retires a terminal action identity.
			if run := c.runs[action.ID]; run != nil && actionEquivalent(run.action, action) &&
				run.action.Status != nativeOperationRunning && run.action.Status != nativeOperationCancel {
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
			if c.conflictingActionRunning(action) {
				continue
			}
			selectedTarget := schedulerOperationTarget(action, &target, intermediate)
			operation := scheduledOperation(action, selectedTarget, config, c.powerTargets, c.state.Snapshot.Workloads)
			runCtx, cancel := c.runContext(action, config)
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
					if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
						run.action.Status, run.action.Reason = nativeOperationFailed, serving.ActionDeadlineExceeded+": "+run.action.Reason
					}
				}
				run.action.Elapsed = result.EndedAt.Sub(result.StartedAt)
				c.state.Results = append(c.state.Results, result)
				c.mu.Unlock()
			}(operation, run)
		}
	}
}

func schedulerOperationTarget(
	action serving.Action, target, intermediate *serving.SchedulerCandidate,
) *serving.SchedulerCandidate {
	if action.From == readySubsetRoute {
		return intermediate
	}
	return target
}

func (c *SchedulerController) intermediateActions(
	target serving.SchedulerCandidate,
) ([]serving.Action, *serving.SchedulerCandidate) {
	// The final handoff still waits for its DAG. Independent Ready destinations
	// can receive traffic meanwhile through the same acknowledged route path.
	pendingRoute := slices.ContainsFunc(target.Actions, func(a serving.Action) bool {
		return a.Kind == operationRoute && !c.dependenciesReady(a, target)
	})
	if !pendingRoute {
		return target.Actions, nil
	}
	partial := c.readySubset(target)
	if partial == nil {
		return target.Actions, nil
	}
	route := serving.Action{ID: partial.ID, Kind: operationRoute, From: readySubsetRoute, To: partial.ID}
	// Recovery must retain the running route's own frozen target even when the
	// full goal fails. Only final-goal completion ignores this intermediate action.
	partial.Actions = []serving.Action{route}
	actions := append(slices.Clone(target.Actions), route)
	return actions, partial
}

// readySubset preserves the goal's relative weights among matching Ready slots.
// Workloads without a Ready destination retain their current Ready distribution.
// Runtime admission still enforces each request's measured execution/SLO contract.
func (c *SchedulerController) readySubset(target serving.SchedulerCandidate) *serving.SchedulerCandidate {
	snapshot, physical := c.state.Snapshot, c.runtime.Instances()
	partial := serving.SchedulerCandidate{Routing: map[string]map[string]int32{}}
	retiring := map[string]bool{}
	for _, action := range target.Actions {
		if action.Kind == operationStop && c.dependenciesReady(action, target) {
			retiring[action.Instance] = true
		}
	}
	for uid, weights := range target.Routing {
		selected := []serving.SchedulerInstance{}
		for _, wanted := range target.Instances {
			if wanted.WorkloadUID == uid && weights[wanted.ID] > 0 && !retiring[wanted.ID] && schedulerTargetReady(wanted, snapshot, physical, false) {
				selected = append(selected, wanted)
			}
		}
		// Do not withdraw another workload's still-serving route while all its
		// new destinations prepare. Never resurrect a stopped/draining runner.
		if len(selected) == 0 && len(weights) != 0 {
			weights = snapshot.Routing[uid]
			for _, actual := range snapshot.Instances {
				if actual.WorkloadUID == uid && weights[actual.ID] > 0 && !retiring[actual.ID] && schedulerTargetReady(actual, snapshot, physical, false) {
					selected = append(selected, actual)
				}
			}
		}
		partial.Routing[uid] = map[string]int32{}
		var total, cumulative, assigned int64
		for _, wanted := range selected {
			total += int64(weights[wanted.ID])
		}
		for _, wanted := range selected {
			cumulative += int64(weights[wanted.ID])
			normalized := cumulative * 1000 / total
			partial.Routing[uid][wanted.ID] = int32(normalized - assigned)
			assigned = normalized
			partial.Instances = append(partial.Instances, wanted)
		}
	}
	if len(partial.Instances) == 0 || c.routingTargetPresent(&partial, snapshot, physical, true) {
		return nil
	}
	// JSON map keys are sorted; physical identities and weights identify this
	// immutable subset independently of slow action completion order.
	raw, _ := json.Marshal(partial.Routing)
	sum := sha256.Sum256(raw)
	partial.ID = target.ID + "-ready-" + hex.EncodeToString(sum[:10])
	return &partial
}

// runContext bounds a prepare run by serving.ActionDeadline.
// ponytail: prepare-only deadline, widen when a non-prepare stall is measured
func (c *SchedulerController) runContext(action serving.Action, config SchedulerControllerConfig) (context.Context, context.CancelFunc) {
	if action.Kind == operationPrepare {
		return context.WithTimeout(c.ctx, serving.ActionDeadline(config.Settings.ObservationWindow, config.Costs, action))
	}
	return context.WithCancel(c.ctx)
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
	physical := c.runtime.Instances()
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

// scheduledOperation builds the operation of one planned action. A preparation
// names the Workload its target instance serves as the Pod's owner, so the
// instance keeps its Workload while unrouted.
func scheduledOperation(action serving.Action, target *serving.SchedulerCandidate, config SchedulerControllerConfig, power map[string]time.Duration, workloads []*serving.Definition) Operation {
	operation := Operation{ID: action.ID, Kind: action.Kind, Node: action.Node, Instance: action.Instance, Target: target}
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
					MaxBatchSize:   i.MaxBatch, MaxBatchWaitMS: float64(i.MaxWait) / float64(time.Millisecond), FixedBatchSize: i.FixedBatchSize}
				for _, d := range workloads {
					if d != nil && d.UID == i.WorkloadUID {
						template.Owner = &serving.WorkloadReference{Name: d.Name, UID: d.UID}
					}
				}
				operation.Prepare = &template
				break
			}
		}
	}
	if action.Kind == operationPower {
		if maxDuration, ok := power[action.Node.UID+"/"+action.To]; ok {
			operation.PowerMode, operation.MaxActuationDuration = action.To, maxDuration
		}
	}
	return operation
}

func bindProfileMeter(instance *serving.SchedulerInstance, meters []MeterDeclaration) {
	if instance.Profile == nil {
		return
	}
	for _, meter := range meters {
		if meter.Node == instance.Node && meter.Scope == measurementWallScope &&
			meter.Source.NodeName == instance.Profile.MetricSource.NodeName && sameWallSeries(meter.Source, instance.Profile.MetricSource) {
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
	physical := c.runtime.Instances()
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
	if wanted.Profile == nil {
		return false
	}
	state := schedulerPhysicalState(wanted.ID, physical)
	if state == nil || !state.Ready || state.Stopped || state.Native == nil || !state.Native.Ready {
		return false
	}
	captured := slices.ContainsFunc(snapshot.Instances, func(actual serving.SchedulerInstance) bool {
		return actual.ID == state.ID && actual.Ready && !actual.Stopped && actual.Node == wanted.Node &&
			serving.SchedulerInstanceState(actual) == serving.SchedulerInstanceState(wanted)
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
	return actual.Policy.ProfileID == wanted.Profile.ID && maxBatch == wanted.MaxBatch && actual.Policy.FixedBatchSize == wanted.FixedBatchSize &&
		actual.Policy.MaxBatchWaitMS == float64(wanted.MaxWait)/float64(time.Millisecond) &&
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
