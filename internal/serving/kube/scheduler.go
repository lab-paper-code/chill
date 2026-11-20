package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	core "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
)

const controlPlaneLabel = "node-role.kubernetes.io/control-plane"

// Intermediate routes are observed operations, not prerequisites of the frozen
// goal: later ready subsets supersede their routing effects.
const readySubsetRoute = "ready-subset"

const externalActionSource = "external-operation"

type SchedulerControllerConfig struct {
	Settings          serving.SchedulerSettings
	ConfigurationName string
	ConfigurationKey  string
	Reserved          map[string]serving.SchedulerAllocation
	Preparations      map[string]servingstate.PrepareSpec
	Costs             []serving.OperationCost
	Baselines         []serving.NodeBaseline
	CostIssues        []CostIssue
	// DispatchOccupancySnapshot reads an immutable, already-resolved selection;
	// it must not scan observations, list runtime state or perform I/O.
	DispatchOccupancySnapshot func() []serving.DispatchOccupancy
	// CatalogIssues are the profiles the catalog or the preparations omit.
	CatalogIssues []CatalogIssue
	Session       SessionConfig
	Meters        []servingops.MeterDeclaration
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
	OnDecision    func(serving.SchedulerDecision, serving.SchedulerSnapshot) error
	OnAction      func(servingstate.OperationResult) error
}

// SessionConfig runs BatchEnergyOptimization measurement sessions. Prepare is
// nil when ChillServer has no Profiler facts to resolve a session from.
type SessionConfig struct {
	Prepare func(context.Context, *edge.BatchEnergyOptimization) (SessionPreparation, error)
	// Image, Namespace, ServiceAccount and NodeName place the session-load Job.
	Image, Namespace, ServiceAccount, NodeName string
	// MaxResultBytes is the byte limit the catalog reads one session result with.
	MaxResultBytes int64
	// RunnerTimeout and MaxRequestBytes bound a measurement instance's native
	// runner and requests with the values a serving instance gets.
	RunnerTimeout   time.Duration
	MaxRequestBytes int64
}

// SessionResolutionRetry marks a transient resolution failure. Err preserves
// the resolver's original failure for the pending status.
type SessionResolutionRetry struct{ Err error }

func (e *SessionResolutionRetry) Error() string { return e.Err.Error() }
func (e *SessionResolutionRetry) Unwrap() error { return e.Err }

// SessionPreparation is the measurement request resolved once per session:
// what each batch size's fixed-batch instance and the result's condition need.
type SessionPreparation struct {
	// RunConfig is the frozen candidate, Node and actor configuration.
	RunConfig profilingrun.RunConfig
	// MemoryBytes is the actor's memory envelope, RunConfig.Actor.MemoryLimitBytes.
	MemoryBytes uint64
	// RuntimeDelivery is RunConfig.Delivery as JSON, the measured delivery a
	// prepared instance reproduces and the result stores.
	RuntimeDelivery json.RawMessage
	// BatchSizes are the batch sizes the session may measure, ascending: powers
	// of two up to the execution path's maximum that the artifact declares.
	BatchSizes []int
	// SLOPercentile and SLOTarget are the measured Workload's latency objective.
	SLOPercentile float64
	SLOTarget     time.Duration
	// RequestLifetime is the deadline serving applies to the measured
	// Workload's requests; the load Job sends it with every request.
	RequestLifetime time.Duration
	// Dataset is the dataset the load Job samples.
	Dataset profilingrun.DatasetContract
}

type SchedulerControllerState struct {
	Snapshot serving.SchedulerSnapshot      `json:"snapshot"`
	Decision serving.SchedulerDecision      `json:"decision"`
	Actions  []serving.Action               `json:"actions"`
	Results  []servingstate.OperationResult `json:"results"`
	// CostIssues are the measurements the configured costs omit.
	CostIssues []CostIssue `json:"costIssues"`
}

type SchedulerController struct {
	*reservationState
	store      *Store
	kube       kubernetes.Interface
	runtime    *serving.Runtime
	observer   *Observer
	operations *servingops.Operations
	// measurement acts only on measurement POOL Nodes: operation measurements and hand-back.
	measurement    *servingops.Operations
	observationLog *observation.ObservationLog
	config         SchedulerControllerConfig
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	completions    chan struct{}
	// powerTargets holds, by "<Node UID>/<mode>", the maximum actuation
	// duration of each mode the last capture found requestable.
	powerTargets map[string]time.Duration
	// measureStep measures one step of an operation-cost measurement.
	measureStep func(context.Context, SchedulerControllerConfig, servingops.MeasurementRequest) (servingops.OperationMeasurement, evidencestore.Receipt, error)
	// sessionReads reads the observation records a measurement session derives from.
	sessionReads   sessionReads
	sessionLoadLog func(context.Context, *core.Pod) (io.ReadCloser, error)
	// measurements caches payloads against each round's direct metadata baseline.
	// tickMu owns the cache and every consumer's selected-object copies.
	measurements []*measurementCache
}

// NewSchedulerController configures control without taking ownership of routing.
// The composition root freezes routing after restoring the actual baseline.
func NewSchedulerController(cluster *ClusterState, store *Store, kube kubernetes.Interface, runtime *serving.Runtime, observer *Observer, operations *servingops.Operations, observationLog *observation.ObservationLog, config SchedulerControllerConfig) (*SchedulerController, error) {
	if cluster == nil || store == nil || kube == nil || runtime == nil || observer == nil || operations == nil || observationLog == nil {
		return nil, errors.New("scheduler requires actual Kubernetes, runtime, observer, operation and observation log owners")
	}
	if err := validateSchedulerConfiguration(config); err != nil {
		return nil, err
	}
	config.Settings = config.Settings.Clone()
	ctx, cancel := context.WithCancel(context.Background())
	measurement := *operations
	measurement.Config.Measurement = true
	c := &SchedulerController{store: store, kube: kube, runtime: runtime, observer: observer, operations: operations, measurement: &measurement, observationLog: observationLog, config: config, reservationState: &cluster.reservation, ctx: ctx, cancel: cancel}
	c.completions = make(chan struct{}, 1)
	c.measureStep = c.measureOperation
	c.sessionReads = observedSessionReads(observationLog)
	c.sessionLoadLog = func(ctx context.Context, pod *core.Pod) (io.ReadCloser, error) {
		return c.kube.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &core.PodLogOptions{Container: sessionLoadContainer}).Stream(ctx)
	}
	return c, nil
}

func (c *SchedulerController) Settings() serving.SchedulerSettings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.config.Settings.Clone()
}

func (c *SchedulerController) SetConfiguration(config SchedulerControllerConfig) error {
	if err := validateSchedulerConfiguration(config); err != nil {
		return err
	}
	config.Settings = config.Settings.Clone()
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

// AcceptSettings applies user policy to already accepted preparations. Source
// costs, physical bindings, reservations and routing declarations stay intact.
func (c *SchedulerController) AcceptSettings(ctx context.Context, settings serving.SchedulerSettings, preparations map[string]servingstate.PrepareSpec) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	before := len(c.config.Settings.AutonomousWorkloads) > 0
	c.config.Settings, c.config.Preparations = settings.Clone(), preparations
	c.mu.Unlock()
	c.setRoutingControl(before, len(settings.AutonomousWorkloads) > 0)
	return nil
}

func (c *SchedulerController) setRoutingControl(before, after bool) {
	if before == after {
		return
	}
	if after {
		c.runtime.EnableSchedulerRouting()
	} else {
		c.cancelCancelableRuns()
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
		if m.ID == "" || m.Scope != servingstate.MeasurementWallScope || m.Node.Name == "" || m.Node.UID == "" || m.Source.NodeName != m.Node.Name {
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
		if b.Meter == "" || b.Scope != servingstate.MeasurementWallScope {
			return errors.New("baseline meter declaration required")
		}
	}
	for _, cost := range config.Costs {
		if cost.Meter == "" || cost.Scope != servingstate.MeasurementWallScope {
			return errors.New("cost meter declaration required")
		}
	}
	return nil
}

func (c *SchedulerController) State() SchedulerControllerState {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := c.stateViewLocked()
	result.CostIssues = c.config.CostIssues
	return result
}

func (c *SchedulerController) Close() { c.cancel(); c.wg.Wait() }

func (c *reservationState) actionsLocked(now time.Time) []serving.Action {
	result := make([]serving.Action, 0, len(c.runs))
	for _, r := range c.runs {
		if r.action.Kind == transitionRunKind || r.action.Kind == measurementLeaseKind || r.action.Kind == workloadDeletionKind {
			continue
		}
		a := r.action
		if a.Kind == servingstate.OperationRoute {
			a.RouteTarget = nil
			if r.result != nil {
				a.RouteTarget = controlledTarget(r.result.Operation)
				if a.RouteTarget != nil {
					a.RouteTarget.Actions = nil
				}
			}
		}
		if a.Status == servingstate.NativeOperationRunning {
			a.Elapsed = now.Sub(r.started)
		} else if r.result != nil && !r.result.EndedAt.IsZero() {
			ended := r.result.EndedAt
			a.EndedAt = &ended
		}
		result = append(result, a)
	}
	slices.SortFunc(result, func(a, b serving.Action) int {
		return strings.Compare(a.ID, b.ID)
	})
	return result
}

func (c *SchedulerController) Tick(ctx context.Context, now time.Time) (result serving.SchedulerDecision, err error) {
	started := time.Now()
	event := observation.SchedulerControlObservation{Scope: "tick", TickAt: now, StartedAt: started.UTC(), Phase: "lock"}
	stage, stageStarted := &event.LockWait, started
	next := func(phase string, elapsed *time.Duration) {
		*stage += time.Since(stageStarted)
		event.Phase, stage, stageStarted = phase, elapsed, time.Now()
	}
	// Registered before unlock, so elapsed includes the controller lock lifetime.
	defer func() {
		finished := time.Now()
		*stage += finished.Sub(stageStarted)
		event.Rest = event.Capture - event.Window - event.RefreshList - event.SourceProbe
		event.FinishedAt, event.Elapsed = finished.UTC(), finished.Sub(started)
		if err != nil {
			event.Error = err.Error()
		}
		if recordErr := c.observationLog.SchedulerRecord(observation.SchedulerObservation{At: event.FinishedAt, Control: &event}); recordErr != nil {
			ctrl.Log.Error(recordErr, "record scheduler Tick completion")
		}
	}()
	c.tickMu.Lock()
	locked := true
	defer func() {
		if locked {
			c.tickMu.Unlock()
		}
	}()
	next("capture", &event.Capture)
	if err := ctx.Err(); err != nil {
		return serving.SchedulerDecision{}, err
	}
	c.mu.Lock()
	config := c.config
	publishedAt := c.state.Snapshot.At
	c.mu.Unlock()
	evaluationStarted := time.Now()
	snapshot, err := c.captureReconciled(ctx, now, config, false, &event)
	if err != nil {
		return serving.SchedulerDecision{}, err
	}
	next("evaluate", &event.Evaluate)
	// Only capture and approval serialize with progress. Evaluation owns its
	// immutable input; approval publishes the latest capture.
	c.tickMu.Unlock()
	locked = false
	decision, err := serving.EvaluateScheduler(ctx, config.Settings, snapshot)
	decision.EvaluationElapsed = time.Since(evaluationStarted)
	event.Evaluation = decision.EvaluationTiming
	event.DecisionAt = decision.At
	event.Status = decision.Status
	if decision.Target != nil {
		event.TargetID = decision.Target.ID
	}
	if decision.Capacity != nil && decision.Capacity.Search != nil {
		event.Fallback = decision.Capacity.Search.Fallback
	}
	if err != nil {
		return serving.SchedulerDecision{}, err
	}
	c.rememberLoadChanges(decision.Search)
	decision.PathFeedback = slices.Clone(snapshot.PathFeedback)
	next("lock", &event.LockWait)
	c.tickMu.Lock()
	locked = true
	return c.approveEvaluation(ctx, now, config, publishedAt, snapshot, decision, &event, next)
}

// plannedNodeState is what an action plan assumes of one Node it acts on.
type plannedNodeState struct {
	PowerMode string
	Ready     bool
	Instances []string
}

// plannedNodeStates is the state of every Node the actions touch: its power
// mode and readiness and the executors on it that are not yet terminated.
func plannedNodeStates(actions []serving.Action, s serving.SchedulerSnapshot) map[string]plannedNodeState {
	states := map[string]plannedNodeState{}
	for _, action := range actions {
		states[action.Node.UID] = plannedNodeState{}
	}
	for _, node := range s.Nodes {
		if _, planned := states[node.Node.UID]; planned {
			states[node.Node.UID] = plannedNodeState{PowerMode: node.PowerMode, Ready: node.Ready}
		}
	}
	for _, instance := range s.Instances {
		if state, planned := states[instance.Node.UID]; planned && !instance.Terminated {
			state.Instances = append(state.Instances, instance.ID+"\x00"+instance.PodUID+"\x00"+instance.ProfileID)
			states[instance.Node.UID] = state
		}
	}
	return states
}

// approveEvaluation is called with tickMu held and returns holding it. Progress
// during evaluation only selects a fresh capture for publication. A new target
// needs the Nodes its actions touch unchanged since evaluation and admission
// on that capture; afterwards only a changed action and reservation ledger or
// approval configuration discards the evaluation.
func (c *SchedulerController) approveEvaluation(
	ctx context.Context, now time.Time, config SchedulerControllerConfig, publishedAt time.Time,
	snapshot serving.SchedulerSnapshot, decision serving.SchedulerDecision,
	event *observation.SchedulerControlObservation, next func(string, *time.Duration),
) (serving.SchedulerDecision, error) {
	// discard returns the decision's error with the target it chose kept in the
	// record, for a decision that never launches.
	discard := func(err error) (serving.SchedulerDecision, error) {
		if decision.Target != nil {
			event.DiscardedActions = slices.Clone(decision.Target.Actions)
		}
		return decision, err
	}
	next("publish", &event.Publish)
	c.mu.Lock()
	latest := c.config
	advanced := !c.state.Snapshot.At.Equal(publishedAt)
	hasRuns := len(c.runs) != 0
	planned := decision.Target != nil && len(decision.Target.Actions) != 0
	c.mu.Unlock()
	current := snapshot
	if advanced || hasRuns || planned || len(decision.Keep.Actions) != 0 {
		// A pure observation/Keep round on the published capture does not
		// repeat the refresh Lists.
		next("capture", &event.Capture)
		var err error
		current, err = c.captureReconciled(ctx, now, latest, false, event)
		if err != nil {
			return decision, err
		}
	}
	// The capture retires ended owners, so only now is it known whether the
	// target already runs.
	c.mu.Lock()
	newTarget := planned && c.runs[transitionRunKind+"/"+decision.Target.ID] == nil
	c.mu.Unlock()
	if newTarget {
		// The action plan was built for the evaluated state of its Nodes.
		if !reflect.DeepEqual(plannedNodeStates(decision.Target.Actions, snapshot), plannedNodeStates(decision.Target.Actions, current)) {
			return discard(errors.New("scheduler evaluation invalidated: planned Node changed"))
		}
		// Exact replay uses fresh worker-clock history without blocking control progress.
		next("evaluate", &event.Evaluate)
		c.tickMu.Unlock()
		reason, err := serving.SchedulerTargetAdmission(ctx, latest.Settings, current, *decision.Target)
		next("lock", &event.LockWait)
		c.tickMu.Lock()
		if err != nil {
			return decision, err
		}
		if reason != "" {
			return discard(fmt.Errorf("scheduler evaluation invalidated: latest target admission: %s", reason))
		}
	}
	next("publish", &event.Publish)
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return serving.SchedulerDecision{}, err
	}
	if !reflect.DeepEqual(schedulerApprovalConfiguration(config), schedulerApprovalConfiguration(c.config)) {
		c.mu.Unlock()
		return discard(errors.New("scheduler evaluation invalidated: configuration changed"))
	}
	config = c.config
	// Executors may finish while admission runs. A new target launches only on
	// the ledger it was admitted on, read under the ledger mutex.
	if newTarget && !reflect.DeepEqual(schedulerApprovalLedger(current.Actions, current.Reservations),
		schedulerApprovalLedger(c.actionsLocked(current.At), c.reservationsLocked())) {
		c.mu.Unlock()
		return discard(errors.New("scheduler evaluation invalidated: live reservations changed"))
	}
	c.publishDecisionLocked(&current, &decision)
	c.runtime.SetPathFeedback(decision.PathFeedback)
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return serving.SchedulerDecision{}, err
	}
	next("record", &event.Record)
	if config.OnDecision != nil {
		if err := config.OnDecision(decision, snapshot); err != nil {
			return discard(fmt.Errorf("persist decision before physical action: %w", err))
		}
	}
	if err := ctx.Err(); err != nil {
		return serving.SchedulerDecision{}, err
	}
	next("launch", &event.Launch)
	// Without a selected target, only finish previously authorized Fixed work.
	if decision.Target != nil {
		c.launch(ctx, *decision.Target, config)
	} else {
		keep := decision.Keep
		keep.Actions = slices.DeleteFunc(slices.Clone(keep.Actions), func(a serving.Action) bool {
			return !authorizedFixedContinuation(a, current.Actions)
		})
		c.launch(ctx, keep, config)
	}
	return decision, ctx.Err()
}

// captureReconciled keeps Tick and completion observation effects in one order.
func (c *SchedulerController) captureReconciled(ctx context.Context, now time.Time, config SchedulerControllerConfig, continuation bool, timing *observation.SchedulerControlObservation) (serving.SchedulerSnapshot, error) {
	demand := true
	if continuation {
		c.mu.Lock()
		demand = c.continuationNeedsWindowLocked()
		c.mu.Unlock()
	}
	snapshot, err := c.captureTimed(ctx, now, config, timing, demand)
	if err != nil {
		return snapshot, err
	}
	if err := ctx.Err(); err != nil {
		return snapshot, err
	}
	if continuation {
		if reasons := serving.SchedulerContinuationReasons(config.Settings, snapshot); len(reasons) != 0 {
			return snapshot, fmt.Errorf("continuation observation unavailable: %s", strings.Join(reasons, ", "))
		}
	}
	snapshot, lost := c.reconcileSnapshot(snapshot, snapshot.At, config.Settings.ObservationWindow, c.runtime)
	for _, result := range lost {
		if config.OnAction != nil {
			if err := config.OnAction(result); err != nil {
				return snapshot, fmt.Errorf("record lost action effect: %w", err)
			}
		}
	}
	c.annotateHistory(&snapshot, config.Settings.ObservationWindow)
	return snapshot, nil
}
