package kube

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	"k8s.io/client-go/kubernetes"
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
	measurement        *servingops.Operations
	observationLog     *observation.ObservationLog
	capacity           *serving.CapacityTable
	config             SchedulerControllerConfig
	bootstrapStartedAt time.Time
	tempo              serving.SchedulerTempo
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	completions        chan struct{}
	// powerTargets holds, by "<Node UID>/<mode>", the maximum actuation
	// duration of each mode the last capture found requestable.
	powerTargets map[string]time.Duration
	// measureStep measures one step of an operation-cost measurement.
	measureStep func(context.Context, SchedulerControllerConfig, servingops.MeasurementRequest) (servingops.OperationMeasurement, evidencestore.Receipt, error)
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
	ctx, cancel := context.WithCancel(context.Background())
	measurement := *operations
	measurement.Config.Measurement = true
	c := &SchedulerController{store: store, kube: kube, runtime: runtime, observer: observer, operations: operations, measurement: &measurement, observationLog: observationLog, config: config, reservationState: &cluster.reservation, ctx: ctx, cancel: cancel}
	c.completions = make(chan struct{}, 1)
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
	c.bootstrapStartedAt = time.Time{}
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
		if n, ok := meters[b.Meter]; ok && n != b.Node {
			return errors.New("baseline meter reused across physical Nodes")
		}
		meters[b.Meter] = b.Node
	}
	for _, cost := range config.Costs {
		if cost.Meter == "" || cost.Scope != servingstate.MeasurementWallScope {
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
	result := c.stateViewLocked()
	result.CostIssues = c.config.CostIssues
	return result
}

func (c *SchedulerController) Close() { c.cancel(); c.capacity.Wait(); c.wg.Wait() }

// Capacity returns the capacity rows the autonomous Workloads need.
func (c *SchedulerController) Capacity() []serving.CapacityRow { return c.capacity.Rows() }

func (c *reservationState) actionsLocked(now time.Time) []serving.Action {
	result := make([]serving.Action, 0, len(c.runs))
	for _, r := range c.runs {
		if r.action.Kind == transitionRunKind || r.action.Kind == measurementLeaseKind || r.action.Kind == workloadDeletionKind {
			continue
		}
		a := r.action
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

func (c *SchedulerController) Tick(ctx context.Context, now time.Time) (serving.SchedulerDecision, error) {
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	if err := ctx.Err(); err != nil {
		return serving.SchedulerDecision{}, err
	}
	c.mu.Lock()
	config := c.config
	c.mu.Unlock()
	started := time.Now()
	snapshot, err := c.captureReconciled(ctx, now, config, false)
	if err != nil {
		return serving.SchedulerDecision{}, err
	}
	if len(snapshot.Instances) == 0 && len(snapshot.Reservations) == 0 && len(config.Settings.AutonomousWorkloads) > 0 {
		if c.bootstrapStartedAt.IsZero() {
			c.bootstrapStartedAt = snapshot.At
		}
		snapshot.BootstrapStartedAt = c.bootstrapStartedAt
	} else {
		c.bootstrapStartedAt = time.Time{}
	}
	c.capacity.Ensure(c.ctx, config.Settings, snapshot)
	c.tempo.Observe(config.Settings, &snapshot, c.capacity.Rows())
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
	c.publishDecisionLocked(&snapshot, &decision)
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return serving.SchedulerDecision{}, err
	}
	if config.OnDecision != nil {
		if err := config.OnDecision(decision, snapshot); err != nil {
			return decision, fmt.Errorf("persist decision before physical action: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return serving.SchedulerDecision{}, err
	}
	// Without a selected target, only finish previously authorized Fixed work.
	if decision.Target != nil {
		c.launch(*decision.Target, config)
	} else {
		keep := decision.Keep
		keep.Actions = slices.DeleteFunc(slices.Clone(keep.Actions), func(a serving.Action) bool {
			return !authorizedFixedContinuation(a, snapshot.Actions)
		})
		c.launch(keep, config)
	}
	return decision, nil
}

// captureReconciled keeps Tick and completion observation effects in one order.
func (c *SchedulerController) captureReconciled(ctx context.Context, now time.Time, config SchedulerControllerConfig, continuation bool) (serving.SchedulerSnapshot, error) {
	snapshot, err := c.capture(ctx, now, config)
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
	snapshot, lost := c.reconcileSnapshot(snapshot, snapshot.At, config.Settings.ObservationWindow, func() serving.SchedulerSettings { return c.config.Settings }, c.runtime)
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
