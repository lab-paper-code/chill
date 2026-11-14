package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// DispatchOccupancy holds legacy evidence for controllers without a cache.
	DispatchOccupancy []serving.DispatchOccupancy
	// DispatchOccupancySnapshot reads an immutable, already-resolved selection;
	// it must not scan observations, list runtime state or perform I/O.
	DispatchOccupancySnapshot func() []serving.DispatchOccupancy
	// CatalogIssues are the profiles the catalog or the preparations omit.
	CatalogIssues []CatalogIssue
	LoadGenerator LoadGeneratorConfig
	Session       SessionConfig
	// GatewayRequestTimeout is the actual HTTP handler lifetime cap.
	GatewayRequestTimeout time.Duration
	Meters                []servingops.MeterDeclaration
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
	// Dataset is the dataset the load Job samples.
	Dataset profilingrun.DatasetContract
	// Meter is the wall meter of the target Node.
	Meter metricobserver.SourceIdentity
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
	sessionReads sessionReads
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

// Capacity returns the rate tests used by the current committed decision.
func (c *SchedulerController) Capacity() []serving.SchedulerFeedbackRate {
	c.mu.Lock()
	defer c.mu.Unlock()
	capacity := c.state.Decision.Capacity
	if capacity == nil {
		return nil
	}
	count := 0
	for _, workload := range capacity.Workloads {
		count += len(workload.Tests)
	}
	result := make([]serving.SchedulerFeedbackRate, 0, count)
	for _, workload := range capacity.Workloads {
		for _, test := range workload.Tests {
			test.Tests = slices.Clone(test.Tests)
			if test.Input.DispatchOccupancy != nil {
				occupancy := *test.Input.DispatchOccupancy
				test.Input.DispatchOccupancy = &occupancy
			}
			result = append(result, test)
		}
	}
	return result
}

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
	// immutable input and cannot publish it after another control call advances.
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
	decision.PathFeedback = slices.Clone(snapshot.PathFeedback)
	next("lock", &event.LockWait)
	c.tickMu.Lock()
	locked = true
	next("publish", &event.Publish)
	c.mu.Lock()
	latest := c.config
	advanced := !c.state.Snapshot.At.Equal(publishedAt)
	hasRuns := len(c.runs) != 0
	newTarget := decision.Target != nil && len(decision.Target.Actions) != 0 &&
		c.runs[transitionRunKind+"/"+decision.Target.ID] == nil
	c.mu.Unlock()
	if advanced || !reflect.DeepEqual(schedulerApprovalConfiguration(config), schedulerApprovalConfiguration(latest)) {
		return decision, errors.New("scheduler evaluation invalidated: progress or configuration changed")
	}
	current := snapshot
	if hasRuns || newTarget || len(decision.Keep.Actions) != 0 {
		// Only physical approval/progress requires another inventory read.
		// A pure observation/Keep round does not repeat the refresh Lists.
		next("capture", &event.Capture)
		current, err = c.captureReconciled(ctx, now, latest, false, &event)
		if err != nil {
			return decision, err
		}
		if !reflect.DeepEqual(schedulerApprovalInputs(latest.Settings, snapshot), schedulerApprovalInputs(latest.Settings, current)) {
			return decision, errors.New("scheduler evaluation invalidated: physical state or reservations changed")
		}
	}
	if newTarget {
		current, latest, err = c.recheckEvaluationTarget(ctx, latest, current, *decision.Target, &event, next)
		if err != nil {
			return decision, err
		}
	}
	config = latest
	next("publish", &event.Publish)
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return serving.SchedulerDecision{}, err
	}
	// Executors may finish while capture returns. Approve against the ledger
	// under its mutex, not the earlier projection from physical observation.
	observed := serving.SchedulerSnapshot{Actions: snapshot.Actions, Reservations: snapshot.Reservations}
	live := serving.SchedulerSnapshot{Actions: c.actionsLocked(snapshot.At), Reservations: c.reservationsLocked()}
	if !reflect.DeepEqual(schedulerApprovalInputs(latest.Settings, observed), schedulerApprovalInputs(latest.Settings, live)) {
		c.mu.Unlock()
		return decision, errors.New("scheduler evaluation invalidated: live reservations changed")
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
			return decision, fmt.Errorf("persist decision before physical action: %w", err)
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

// recheckEvaluationTarget is called with tickMu held and returns holding it.
// Exact replay uses fresh worker-clock history without blocking control progress.
func (c *SchedulerController) recheckEvaluationTarget(
	ctx context.Context, config SchedulerControllerConfig, current serving.SchedulerSnapshot,
	target serving.SchedulerCandidate, event *observation.SchedulerControlObservation, next func(string, *time.Duration),
) (serving.SchedulerSnapshot, SchedulerControllerConfig, error) {
	c.mu.Lock()
	publishedAt := c.state.Snapshot.At
	c.mu.Unlock()
	next("evaluate", &event.Evaluate)
	c.tickMu.Unlock()
	reason, err := serving.SchedulerTargetAdmission(ctx, config.Settings, current, target)
	next("lock", &event.LockWait)
	c.tickMu.Lock()
	if err != nil {
		return current, config, err
	}
	if reason != "" {
		return current, config, fmt.Errorf("scheduler evaluation invalidated: latest target admission: %s", reason)
	}
	c.mu.Lock()
	latest := c.config
	changed := !c.state.Snapshot.At.Equal(publishedAt) ||
		!reflect.DeepEqual(schedulerApprovalConfiguration(config), schedulerApprovalConfiguration(latest))
	c.mu.Unlock()
	if changed {
		return current, latest, errors.New("scheduler evaluation invalidated: progress or configuration changed during admission")
	}
	next("capture", &event.Capture)
	approved, err := c.captureReconciled(ctx, current.ObservationEnd, latest, false, event)
	if err != nil {
		return current, latest, err
	}
	if !reflect.DeepEqual(schedulerApprovalInputs(latest.Settings, current), schedulerApprovalInputs(latest.Settings, approved)) {
		return current, latest, errors.New("scheduler evaluation invalidated: physical state or reservations changed during admission")
	}
	return approved, latest, nil
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
