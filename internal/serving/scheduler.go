package serving

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

const (
	SLOPriorityExcess   = "excess"
	SLOPriorityWorkload = "workload"
)

// SchedulerSettings separates the observed population from the control cadence
// and the comparison period: every plan's complete pool energy is compared over
// the fixed ComparisonPeriod. AutonomousWorkloads lists the Workload UIDs under
// autonomous control.
// MaxReplicas and MaxInstances bound plan instance counts; CPU is shared.
type SchedulerSettings struct {
	ObservationWindow         time.Duration `json:"observationWindowNanos"`
	ControlInterval           time.Duration `json:"controlIntervalNanos"`
	ComparisonPeriod          time.Duration `json:"comparisonPeriodNanos"`
	TransitionReserveFraction float64       `json:"transitionReserveFraction"`
	ScaleDownHeadroomFraction float64       `json:"scaleDownHeadroomFraction"`
	MaxReplicas               int           `json:"maxReplicas"`
	MaxInstances              int           `json:"maxInstances"`
	ServingMemoryBytes        uint64        `json:"servingMemoryBytes"`
	CompletionMarginMS        float64       `json:"completionMarginMS"`
	AutonomousWorkloads       []string      `json:"autonomousWorkloads"`
	// SLOPriorityStrategy selects excess order (the zero-value default) or
	// the supplied Workload order. Unknown UIDs remain stored for evaluation.
	SLOPriorityStrategy string   `json:"sloPriorityStrategy,omitempty"`
	WorkloadPriority    []string `json:"workloadPriority"`
	// ExcludedNodes names Nodes the planner keeps as they are, like
	// measurement Nodes: no placement, removal, power change or lowering.
	ExcludedNodes []string `json:"excludedNodes"`
	// Offline capacity rows (BuildCapacityRow) take at least
	// CapacityMinimumRequests arrivals per window and judge by the grid and
	// bisection settings below.
	CapacityMinimumRequests int     `json:"capacityMinimumRequests"`
	CapacityGridSteps       int     `json:"capacityGridSteps"`
	CapacityBisectionWidth  float64 `json:"capacityBisectionWidth"`
	CapacityTailSamples     int     `json:"capacityTailSamples"`
	// Dispatch occupancy defaults to configured ns/request when no measured or profile d exists.
	DefaultDispatchOccupancyNanos int64 `json:"defaultDispatchOccupancyNanos"`
	// DefaultOperationCost supplies whole-wall costs for unmeasured operations.
	// Every operation kind and DefaultIdlePower must be configured explicitly.
	DefaultOperationCost map[string]OperationCostValue `json:"defaultOperationCost,omitempty"`
	DefaultIdlePower     *IdlePowerDefault             `json:"defaultIdlePower,omitempty"`
	// PlanSearchWorkers bounds the goroutines that evaluate plan sets at once;
	// each search round ends within half of ControlInterval.
	PlanSearchWorkers       int               `json:"planSearchWorkers"`
	PlanSearchStrategy      string            `json:"planSearchStrategy"`
	PlanSearchEvaluations   int               `json:"planSearchEvaluations"`
	PlanExactFinalists      int               `json:"planExactFinalists"`
	PlanPolishActive        int               `json:"planPolishActive"`
	PlanPostFilterFinalists bool              `json:"planPostFilterFinalists"`
	PlanPricedStart         bool              `json:"planPricedStart"`
	Controls                SchedulerControls `json:"controls"`
}

// Clone detaches mutable settings values for accepted policy and decision snapshots.
func (s SchedulerSettings) Clone() SchedulerSettings {
	s.AutonomousWorkloads = slices.Clone(s.AutonomousWorkloads)
	s.WorkloadPriority = slices.Clone(s.WorkloadPriority)
	s.ExcludedNodes = slices.Clone(s.ExcludedNodes)
	s.DefaultOperationCost = maps.Clone(s.DefaultOperationCost)
	if s.DefaultIdlePower != nil {
		idle := *s.DefaultIdlePower
		s.DefaultIdlePower = &idle
	}
	return s
}

// ValidateSchedulerSettingsPatch applies a public settings merge patch without
// creating a second evaluator settings schema.
func ValidateSchedulerSettingsPatch(current SchedulerSettings, patch json.RawMessage) (SchedulerSettings, error) {
	raw := bytes.TrimSpace(patch)
	if len(raw) == 0 || len(raw) > 64*1024 || raw[0] != '{' || !json.Valid(raw) {
		return SchedulerSettings{}, fmt.Errorf("settingsPatch must be a JSON object of at most 65536 bytes")
	}
	// Validate the patch too: an unknown key with null would disappear at merge.
	var partial SchedulerSettingsInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&partial); err != nil {
		return SchedulerSettings{}, err
	}
	before, err := json.Marshal(current)
	if err != nil {
		return SchedulerSettings{}, err
	}
	merged, err := jsonpatch.MergePatch(before, raw)
	if err != nil {
		return SchedulerSettings{}, err
	}
	var settings SchedulerSettingsInput
	decoder = json.NewDecoder(bytes.NewReader(merged))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return SchedulerSettings{}, err
	}
	return settings.Settings()
}

// DispatchOccupancy is the operational extra busy time per request beyond the
// profile's runner-service samples. Throughput and replay add batch size × d
// once per batch, including when d is a configured default. Signed session
// samples remain in the profile; only operational d is clamped.
// Source is always empty; it stays in the wire shape because capacity row
// keys (CapacityInput.Key) marshal it. Provenance.Source classifies selection.
type DispatchOccupancy struct {
	Node          NodeReference                `json:"node"`
	PowerMode     string                       `json:"powerMode"`
	RuntimeImage  string                       `json:"runtimeImage"`
	Model         string                       `json:"model"`
	ProfileID     string                       `json:"profileID,omitempty"`
	ExecutionPath string                       `json:"executionPath,omitempty"`
	Batch         int                          `json:"batch,omitempty"`
	PerRequest    time.Duration                `json:"perRequestNanos"`
	Source        string                       `json:"source"`
	Provenance    *DispatchOccupancyProvenance `json:"provenance,omitempty"`
}

// DispatchOccupancyProvenance records the selected operational value and facts
// behind it.
type DispatchOccupancyProvenance struct {
	Source    string    `json:"source"` // profile or default
	Pairs     int       `json:"pairs"`
	UpdatedAt time.Time `json:"updatedAt"`
	Value     int64     `json:"value"`
}

const (
	occupancySourceProfile = "profile"
	occupancySourceDefault = "default"
)

// ResolveDispatchOccupancy selects the median of the profile batch's own
// saturation samples, else the configured default.
func ResolveDispatchOccupancy(settings SchedulerSettings, i SchedulerInstance) *DispatchOccupancy {
	if i.Profile == nil {
		return nil
	}
	c := &i.Profile.Condition
	o := DispatchOccupancy{Node: i.Node, PowerMode: c.PowerMode, RuntimeImage: c.RuntimeImage, Model: c.LogicalModel,
		ProfileID: i.Profile.ID, ExecutionPath: c.Artifact.ExecutionPath, Batch: i.MaxBatch,
		PerRequest: time.Duration(settings.DefaultDispatchOccupancyNanos)}
	provenance := DispatchOccupancyProvenance{Source: occupancySourceDefault}
	if b := schedulerBatch(i.Profile, i.MaxBatch); b != nil && len(b.DispatchOccupancyNanos) > 0 {
		sample := slices.Clone(b.DispatchOccupancyNanos)
		slices.Sort(sample)
		median := sample[int(math.RoundToEven(float64(len(sample)-1)*.5))]
		o.PerRequest = time.Duration(math.Round(max(median, 0)))
		provenance = DispatchOccupancyProvenance{Source: occupancySourceProfile, Pairs: len(sample), UpdatedAt: i.Profile.MeasuredAt}
	}
	o.PerRequest = max(o.PerRequest, 0)
	provenance.Value = int64(o.PerRequest)
	o.Provenance = &provenance
	return &o
}

// SchedulerSelectedOccupancy records each exact cell actually used by a decision,
// including option-only pools, without mutating incoming evidence.
func SchedulerSelectedOccupancy(settings SchedulerSettings, snapshot SchedulerSnapshot) []DispatchOccupancy {
	if len(snapshot.Instances) == 0 && len(snapshot.Options) == 0 {
		return slices.Clone(snapshot.DispatchOccupancy)
	}
	var selected []DispatchOccupancy
	type cell struct {
		node, mode, model, path, profile string
		batch                            int
	}
	seen := map[cell]bool{}
	add := func(i SchedulerInstance) {
		if i.Profile == nil {
			return
		}
		c := &i.Profile.Condition
		key := cell{i.Node.UID, c.PowerMode, c.LogicalModel, c.Artifact.ExecutionPath, i.Profile.ID, i.MaxBatch}
		if seen[key] {
			return
		}
		seen[key] = true
		selected = append(selected, *ResolveDispatchOccupancy(settings, i))
	}
	for _, i := range snapshot.Instances {
		add(i)
	}
	for _, o := range snapshot.Options {
		for _, variant := range CapacityVariants(o) {
			add(variant.Instance)
		}
	}
	return selected
}

type SchedulerControls struct {
	Placement  bool `json:"placement"`
	Resources  bool `json:"resources"`
	Replicas   bool `json:"replicas"`
	Routing    bool `json:"routing"`
	Batching   bool `json:"batching"`
	Power      bool `json:"power"`
	Membership bool `json:"membership"`
}
type SchedulerAllocation struct {
	GPUUUID     string `json:"gpuUUID,omitempty"`
	MemoryBytes uint64 `json:"memoryBytes"`
}

// Node reasons: configuration a Node of the entire POOL lacks.
const (
	NodePlacementInventoryMissing  = "PlacementInventoryMissing"
	NodePlacementBudgetUnavailable = "PlacementBudgetUnavailable"
	NodePowerModeUnavailable       = "PowerModeUnavailable"
	// Refinements distinguish missing observation from unavailable actuation.
	NodePowerModeObservationUnavailable = "PowerModeObservationUnavailable"
	NodePowerTransitionUnavailable      = "PowerTransitionUnavailable"
	// NodeInstanceUnavailable: a CHILL serving instance on the Node has no
	// readable native or profile state. The Node keeps its state; other Nodes
	// are still planned.
	NodeInstanceUnavailable = "InstanceUnavailable"
	// NodeExcluded: settings.excludedNodes names the Node; it is never planned.
	NodeExcluded = "ExcludedNode"
)

// PowerReboot is an observed directed transition, not a device mode policy.
// Facts live only for this process and physical Node UID.
type PowerReboot struct {
	From, To                  string
	BeforeBootID, AfterBootID string
}

// SchedulerNode is one Node of the entire POOL. Measurement marks the
// measurement POOL; Reasons name configuration the Node lacks.
type SchedulerNode struct {
	Node            NodeReference         `json:"node"`
	PowerMode       string                `json:"powerMode"`
	SwitchableModes []string              `json:"switchableModes"`
	Ready           bool                  `json:"ready"`
	Measurement     bool                  `json:"measurement,omitempty"`
	GPUUUIDs        []string              `json:"gpuUUIDs"`
	MemoryBytes     uint64                `json:"memoryBytes"`
	Reserved        SchedulerAllocation   `json:"reserved"`
	NonServing      []SchedulerAllocation `json:"nonServing"`
	ObservedAt      time.Time             `json:"observedAt"`
	Reasons         []string              `json:"reasons,omitempty"`
	// Reboots selects lifecycle ordering only; it is not a stored cost contract.
	Reboots []PowerReboot `json:"-"`
}

type SchedulerInstance struct {
	ID               string        `json:"id"`
	PodUID           string        `json:"podUID,omitempty"`
	PodName          string        `json:"podName,omitempty"`
	TargetID         string        `json:"targetID,omitempty"`
	OwnerWorkloadUID string        `json:"ownerWorkloadUID,omitempty"`
	Node             NodeReference `json:"node"`
	// Profile is the catalog body in memory; stored records keep only ProfileID.
	Profile             *Profile `json:"profile,omitempty"`
	ProfileID           string   `json:"profileID,omitempty"`
	RuntimeImage        string   `json:"runtimeImage,omitempty"`
	RunnerCommand       []string `json:"runnerCommand,omitempty"`
	ActualImageID       string   `json:"actualImageID,omitempty"`
	DesiredRuntimeImage string   `json:"desiredRuntimeImage,omitempty"`
	ReplacementReason   string   `json:"replacementReason,omitempty"`
	// WorkloadUID is the one Workload the instance serves; empty when unrouted.
	WorkloadUID string              `json:"workloadUID"`
	Allocation  SchedulerAllocation `json:"allocation"`
	Ready       bool                `json:"ready"`
	Stopped     bool                `json:"stopped"`
	// Terminated retains exact physical termination without erasing occupancy.
	Terminated     bool                           `json:"terminated,omitempty"`
	StartupFailure *PreparationFailureObservation `json:"startupFailure,omitempty"`
	Routable       map[string]bool                `json:"routable"`
	Reasons        []string                       `json:"reasons,omitempty"`
	Capacity       int                            `json:"capacity"`
	Weight         int32                          `json:"weight"`
	MaxBatch       int                            `json:"maxBatch"`
	MaxWait        time.Duration                  `json:"maxWaitNanos"`
	ObservedAt     time.Time                      `json:"observedAt"`
	// NativeSpanAt anchors derived native idle/arrival spans. Zero means the
	// unchanged physical observation; ObservedAt always retains probe provenance.
	NativeSpanAt                time.Time `json:"nativeSpanAt"`
	LastServiceEndAgeUpperNanos *int64    `json:"lastServiceEndAgeUpperNanos,omitempty"`
	AdmissionIdleUnknown        bool      `json:"admissionIdleUnknown,omitempty"`
	// LastServiceEndAgeNanos is worker-clock idle age at the native span anchor.
	// With an upper age it is an interval bound. Nil only before readiness: a
	// ready actor was warmed, and a ready instance without it is not planned.
	LastServiceEndAgeNanos *int64 `json:"lastServiceEndAgeNanos,omitempty"`
	// ArrivalAgesMS freezes the native actor's bounded, most-recent-first
	// accepted arrival history at ObservedAt, including already finished work.
	ArrivalAgesMS map[string][]float64 `json:"arrivalAgesMS"`
	IdleMeter     string               `json:"idleMeter"`
	IdleScope     string               `json:"idleScope"`
}

// Options are evidence-bound placements supplied by the catalog, not invented
// allocations. CompatibleUIDs lists the immutable definitions whose model,
// runtime and full input contract the catalog checked against the profile.
type SchedulerOption struct {
	Instance       SchedulerInstance `json:"instance"`
	CompatibleUIDs []string          `json:"compatibleUIDs"`
}
type SchedulerRequest struct {
	ID                string         `json:"id"`
	QueueOrder        int            `json:"queueOrder"`
	RemainingLifetime *time.Duration `json:"remainingLifetimeNanos,omitempty"`
	Definition        *Definition    `json:"definition"`
	DefinitionID      string         `json:"definitionID,omitempty"` // Stored records keep Definition.Identity().
	ReceivedAt        time.Time      `json:"receivedAt"`
	Elapsed           time.Duration  `json:"elapsedNanos"`
	QueueAge          time.Duration  `json:"queueAgeNanos"`
	Phase             string         `json:"phase"` // pending, assigned, queued, deferred, running, returning
	Instance          string         `json:"instance,omitempty"`
	BatchID           string         `json:"batchID,omitempty"`
	RunningElapsed    time.Duration  `json:"runningElapsedNanos"`
	// PrecedingIdleNanos and ExchangeElapsedNanos refer to the actual IPC exchange,
	// not the actor running span. Nil prior idle preserves startup/unknown history.
	PrecedingIdleNanos    *int64        `json:"precedingIdleNanos,omitempty"`
	ExchangeElapsedNanos  *int64        `json:"exchangeElapsedNanos,omitempty"`
	ReturnElapsed         time.Duration `json:"returnElapsedNanos"`
	ArrivalRate           float64       `json:"arrivalRate"`
	NativeSenderElapsedMS *float64      `json:"nativeSenderElapsedMS,omitempty"`
	NativeElapsedNanos    *float64      `json:"nativeElapsedNanos,omitempty"`
	CompletionMarginMS    *float64      `json:"completionMarginMS,omitempty"`
	NativeOutcome         string        `json:"nativeOutcome,omitempty"`
}

// SchedulerArrival is one received request. ClientOutcome and ClientElapsed
// are its client's report (ClientObservation), set once the report has reached
// the server; an unreported arrival has an empty ClientOutcome.
type SchedulerArrival struct {
	ID            string        `json:"id"`
	Lifetime      time.Duration `json:"lifetimeNanos"`
	At            time.Time     `json:"at"`
	Definition    *Definition   `json:"definition,omitempty"`
	Accepted      bool          `json:"accepted"`
	Compute       bool          `json:"compute"`
	WorkloadUID   string        `json:"workloadUID,omitempty"`
	Outcome       string        `json:"outcome,omitempty"`
	ClientOutcome string        `json:"clientOutcome,omitempty"`
	ClientElapsed time.Duration `json:"clientElapsedNanos,omitempty"`
}

// computeInput reports an arrival the replay serves: a validated or accepted
// request of a known Definition.
func (a SchedulerArrival) computeInput() bool {
	return (a.Compute || a.Accepted) && a.Definition != nil
}

type NodeBaseline struct {
	ID              string            `json:"id"`
	Node            NodeReference     `json:"node"`
	DeviceClass     string            `json:"deviceClass,omitempty"`
	PowerMode       string            `json:"powerMode"`
	State           []string          `json:"state"` // sorted profile ID + allocation identity, once per physical instance
	StateIdentities map[string]string `json:"stateIdentities,omitempty"`
	Watts           float64           `json:"watts"`
	// WattsUncertainty is the deterministic shift/gap half-width, not a confidence interval.
	WattsUncertainty float64                      `json:"wattsUncertainty,omitempty"`
	Meter            string                       `json:"meter"`
	Scope            string                       `json:"scope"` // node-exclusive-wall
	MeasuredAt       time.Time                    `json:"measuredAt"`
	Reference        evidencestore.ObjectIdentity `json:"reference"`
}
type OperationTrial struct {
	Duration time.Duration `json:"durationNanos"`
	Joules   float64       `json:"joules"`
	// EnergyUncertaintyJoules bounds wall energy even when Joules stores a signed
	// baseline delta: reconstructing that same baseline does not add it again.
	EnergyUncertaintyJoules float64 `json:"energyUncertaintyJoules,omitempty"`
}
type OperationCost struct {
	ID                  string                       `json:"id"`
	Node                NodeReference                `json:"node"`
	DeviceClass         string                       `json:"deviceClass,omitempty"`
	Kind                string                       `json:"kind"`
	From                string                       `json:"from"`
	To                  string                       `json:"to"`
	Trials              []OperationTrial             `json:"trials"`
	EnergyScope         string                       `json:"energyScope"` // whole-wall or target-baseline-delta
	SubtractedWatts     *float64                     `json:"subtractedWatts,omitempty"`
	Meter               string                       `json:"meter"`
	Scope               string                       `json:"scope"`
	Reference           evidencestore.ObjectIdentity `json:"reference"`
	PowerMode           string                       `json:"powerMode,omitempty"`
	BeforeState         []string                     `json:"beforeState"`
	TargetState         []string                     `json:"targetState"`
	BeforeBaselineWatts *float64                     `json:"beforeBaselineWatts,omitempty"`
	// MeasuredAt selects the latest cost per condition.
	MeasuredAt time.Time `json:"measuredAt"`
	// StateIdentities resolves historical profile IDs to measurement cells.
	StateIdentities map[string]string `json:"stateIdentities,omitempty"`
}

// ActionExpectedSource identifies the measured trials behind an action duration.
type ActionExpectedSource struct {
	CostID     string                       `json:"costID"`
	Reference  evidencestore.ObjectIdentity `json:"reference"`
	Statistic  string                       `json:"statistic"`
	TrialCount int                          `json:"trialCount"`
}
type Action struct {
	ID              string                   `json:"id"`
	Kind            string                   `json:"kind"` // prepare, route, stop, drain, remove, power
	Node            NodeReference            `json:"node"`
	Instance        string                   `json:"instance,omitempty"`
	From            string                   `json:"from,omitempty"`
	To              string                   `json:"to,omitempty"`
	DependsOn       []string                 `json:"dependsOn"`
	CostID          string                   `json:"costID,omitempty"`
	CostProvenance  *OperationCostProvenance `json:"costProvenance,omitempty"`
	ExpectedSeconds *float64                 `json:"expectedSeconds"`
	ExpectedSource  *ActionExpectedSource    `json:"expectedSource"`
	// RouteTarget freezes an intermediate ACK handoff without replacing the
	// final goal. It contains no Actions and only routes already-prepared peers.
	RouteTarget *SchedulerCandidate `json:"routeTarget,omitempty"`
	// Fixed lowers an available Node to its lowest power mode; every candidate shares it.
	Fixed   bool          `json:"fixed,omitempty"`
	Status  string        `json:"status"` // planned, running, complete, failed, cancel
	Elapsed time.Duration `json:"elapsedNanos"`
	// EndedAt is when the action's operation ended; nil until it has ended.
	EndedAt            *time.Time          `json:"endedAt,omitempty"`
	Cancelable         bool                `json:"cancelable"`
	Reason             string              `json:"reason,omitempty"`
	PreparationFailure *PreparationFailure `json:"preparationFailure,omitempty"`
}

type SchedulerSnapshot struct {
	// At is the current request read and decision origin, after capture I/O.
	At time.Time `json:"at"`
	// ObservationEnd is the fixed historical arrival cutoff.
	ObservationEnd time.Time `json:"observationEnd"`
	// ObservedSince is when arrival observation became complete (the process
	// start); the observation window starts no earlier.
	ObservedSince  time.Time                   `json:"observedSince,omitempty"`
	Workloads      []*Definition               `json:"workloads"`
	WorkloadIDs    []string                    `json:"workloadIDs,omitempty"` // Stored records reference the catalog.
	Nodes          []SchedulerNode             `json:"nodes"`
	Instances      []SchedulerInstance         `json:"instances"`
	Routing        map[string]map[string]int32 `json:"routing"`
	RoutingCredits map[string]map[string]int64 `json:"routingCredits"` // Workload UID, instance ID
	Outstanding    []SchedulerRequest          `json:"outstanding"`
	Arrivals       []SchedulerArrival          `json:"arrivals"`
	PathFeedback   []PathFeedback              `json:"pathFeedback,omitempty"`
	Options        []SchedulerOption           `json:"options"`
	Baselines      []NodeBaseline              `json:"baselines"`
	BaselineRefs   []string                    `json:"baselineRefs,omitempty"`
	Costs          []OperationCost             `json:"costs"`
	// DispatchOccupancy holds the selected d and provenance per accepted
	// node/model/mode/profile/execution path/batch, including configured defaults.
	DispatchOccupancy []DispatchOccupancy `json:"dispatchOccupancy"`
	CostRefs          []string            `json:"costRefs,omitempty"`
	Actions           []Action            `json:"actions"`
	Reasons           []string            `json:"reasons"`
	References        []string            `json:"references"`
	// Reservations are in launch order: their owners' first actions' starts.
	Reservations []SchedulerReservation `json:"reservations,omitempty"`
	// LoadChangedAt is, by Workload UID, the latest arrival-rate change an
	// earlier decision detected (SchedulerLoad.WindowStart). It starts the
	// Workload's load window while it lies inside the observation window.
	LoadChangedAt map[string]time.Time `json:"loadChangedAt,omitempty"`
	// DecidedFor is what the configuration was last decided for: the
	// SchedulerDecision.DecidedFor of the latest published energy decision,
	// cleared by a detected load change. Empty means never decided.
	DecidedFor string `json:"decidedFor,omitempty"`
	// replayScope is immutable, decision-local prediction authority, never stored.
	replayScope *schedulerReplayScope
}

// SchedulerReservation fixes physical ownership while an operation or its DAG
// still owns effects. Target belongs to this owner, never to the whole scheduler.
type SchedulerReservation struct {
	Owner        string              `json:"owner"`
	Nodes        []NodeReference     `json:"nodes"`
	WorkloadUIDs []string            `json:"workloadUIDs"`
	PodUIDs      []string            `json:"podUIDs"`
	Instances    []string            `json:"instances"`
	Target       *SchedulerCandidate `json:"target,omitempty"`
	Reason       string              `json:"reason,omitempty"`
}
type SchedulerEstimate struct {
	UID                  string         `json:"uid"`
	Definition           string         `json:"definition"`
	Samples              int            `json:"samples"`
	Percentile           float64        `json:"percentile"`
	ExpectedNanos        *float64       `json:"expectedNanos"`
	TargetNanos          int64          `json:"targetNanos"`
	NormalizedExceedance float64        `json:"normalizedExceedance"`
	PredictedOutcomes    map[string]int `json:"predictedOutcomes"`
}
type SchedulerCandidate struct {
	ID                 string                      `json:"id"`
	Keep               bool                        `json:"keep"`
	Nodes              []SchedulerNode             `json:"nodes,omitempty"`
	Instances          []SchedulerInstance         `json:"instances,omitempty"`
	Routing            map[string]map[string]int32 `json:"routing,omitempty"`
	Actions            []Action                    `json:"actions"`
	Status             string                      `json:"status"`
	Reasons            []string                    `json:"reasons"`
	SLO                []SchedulerEstimate         `json:"slo,omitempty"`
	Feedback           []FeedbackWorkloadScore     `json:"feedback,omitempty"`
	ExpectedJoules     *float64                    `json:"expectedJoules"`
	WorstExceedance    float64                     `json:"worstExceedance"`
	Assumptions        []string                    `json:"assumptions,omitempty"`
	Sources            []string                    `json:"sources,omitempty"`
	ConditionalService *ConditionalServiceEvidence `json:"conditionalService,omitempty"`
}
type SchedulerSearch struct {
	// Examined counts node plans built, not plan-set candidates evaluated by
	// the sweep (Capacity.Search.Evaluated).
	Examined      int               `json:"examined"`
	Bounds        SchedulerSettings `json:"bounds"`
	WindowStart   time.Time         `json:"windowStart"`
	WindowEnd     time.Time         `json:"windowEnd"`
	Received      int               `json:"received"`
	Accepted      int               `json:"accepted"`
	ComputeInputs int               `json:"computeInputs"`
	Population    string            `json:"population"`
	Loads         []SchedulerLoad   `json:"loads"`
	// ComparedNodes are the Nodes with an eligible plan other than keep; ExpectedJoules sums only them.
	ComparedNodes []NodeReference `json:"comparedNodes"`
}

// SchedulerLoad is one Workload's load: its arrivals since WindowStart, the
// last detected change of its arrival rate in the observation window or the
// window's start (schedulerLoadStart). Changed is set when this decision
// detected the change. InputsPerSecond is the observed rate, ComputeInputs
// over the load window; energy reads it. PlannedPerSecond is the rate the
// planner plans for (schedulerPlannedRate): the exact Poisson upper
// confidence limit of ComputeInputs at the share the Workload's SLO
// percentile leaves; capacity, routing and the replay read it, and the replay
// repeats the observed arrivals compressed in time by InputsPerSecond /
// PlannedPerSecond. A load without a current Workload, or one whose
// percentile is outside (0, 100) where no such limit exists (the Workload API
// admits only (0, 100)), is planned at its observed rate.
type SchedulerLoad struct {
	UID              string    `json:"uid"`
	WindowStart      time.Time `json:"windowStart"`
	Changed          bool      `json:"changed,omitempty"`
	Received         int       `json:"received"`
	Accepted         int       `json:"accepted"`
	ComputeInputs    int       `json:"computeInputs"`
	InputsPerSecond  float64   `json:"inputsPerSecond"`
	PlannedPerSecond float64   `json:"plannedPerSecond"`
}

// Workload decision states.
const (
	WorkloadDisabled                   = "Disabled"
	WorkloadMeasurementEvidenceMissing = "MeasurementEvidenceMissing"
	WorkloadControlled                 = "Controlled"
)

type SchedulerWorkloadState struct {
	UID    string `json:"uid"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type SchedulerDecision struct {
	At     time.Time          `json:"at"`
	Status string             `json:"status"`
	Keep   SchedulerCandidate `json:"keep"`
	// Committed are the instances still preparing for a reserved target that
	// Keep's and Target's predictions count as Ready (schedulerCommittedView).
	// Keep's Instances and Routing are the running configuration.
	Committed    []string                 `json:"committed,omitempty"`
	Target       *SchedulerCandidate      `json:"target"`
	Reasons      []string                 `json:"reasons"`
	Settings     SchedulerSettings        `json:"settings"`
	Search       SchedulerSearch          `json:"search"`
	Workloads    []SchedulerWorkloadState `json:"workloads"`
	References   []string                 `json:"references"`
	Capacity     *SchedulerCapacity       `json:"capacity,omitempty"`
	PathFeedback []PathFeedback           `json:"pathFeedback,omitempty"`
	// EvaluationElapsed is the monotonic Tick time from capture to the decision.
	EvaluationElapsed time.Duration             `json:"evaluationElapsedNanos"`
	EvaluationTiming  SchedulerEvaluationTiming `json:"evaluationTiming"`
	// DecidedFor is the signature of the controlled Workload definitions
	// (capacityPlanner.decidedFor) when this decision searched for the
	// cheapest configuration while keep met every SLO; empty otherwise.
	DecidedFor string `json:"decidedFor,omitempty"`
}

// SchedulerEvaluationTiming records non-overlapping EvaluateScheduler stages.
// Surrogate and Exact are backend subsets of Search, including filters/seeding;
// they survive errors without retaining a candidate or publishable decision.
type SchedulerEvaluationTiming struct {
	Preparation time.Duration `json:"preparationNanos"`
	Search      time.Duration `json:"searchNanos"`
	Payback     time.Duration `json:"paybackNanos"`
	Projection  time.Duration `json:"projectionNanos"`
	Surrogate   time.Duration `json:"surrogateElapsedNanos"`
	Exact       time.Duration `json:"exactElapsedNanos"`
}

func (s SchedulerSettings) Validate() error {
	if s.ObservationWindow <= 0 || s.ControlInterval <= 0 || s.ComparisonPeriod <= 0 {
		return fmt.Errorf("scheduler time intervals must be positive")
	}
	if !schedulerUnitFraction(s.TransitionReserveFraction) {
		return fmt.Errorf("transitionReserveFraction must be finite and between 0 and 1")
	}
	if !schedulerUnitFraction(s.ScaleDownHeadroomFraction) {
		return fmt.Errorf("scaleDownHeadroomFraction must be finite and between 0 and 1")
	}
	if s.MaxReplicas < 1 || s.MaxInstances < 1 {
		return fmt.Errorf("maxReplicas and maxInstances must be positive")
	}
	if math.IsNaN(s.CompletionMarginMS) || math.IsInf(s.CompletionMarginMS, 0) || s.CompletionMarginMS < 0 {
		return fmt.Errorf("completionMarginMS must be finite and nonnegative")
	}
	if s.DefaultDispatchOccupancyNanos < 0 {
		return fmt.Errorf("defaultDispatchOccupancyNanos must be nonnegative")
	}
	if err := s.validateEstimateDefaults(); err != nil {
		return err
	}
	if err := s.validateSLOPriority(); err != nil {
		return err
	}
	if s.CapacityMinimumRequests < 1 {
		return fmt.Errorf("capacityMinimumRequests must be at least 1")
	}
	if err := s.validateCapacityNumerics(); err != nil {
		return err
	}
	if err := s.validatePlanSearch(); err != nil {
		return err
	}
	return s.validateExcludedNodes()
}

func (s SchedulerSettings) validateSLOPriority() error {
	if s.SLOPriorityStrategy != "" && s.SLOPriorityStrategy != SLOPriorityExcess && s.SLOPriorityStrategy != SLOPriorityWorkload {
		return fmt.Errorf("sloPriorityStrategy must be excess or workload")
	}
	priority := make(map[string]bool, len(s.WorkloadPriority))
	for _, uid := range s.WorkloadPriority {
		if uid == "" || priority[uid] {
			return fmt.Errorf("workloadPriority entries need unique nonempty Workload UIDs")
		}
		priority[uid] = true
	}
	return nil
}

func (s SchedulerSettings) validatePlanSearch() error {
	if s.PlanSearchWorkers < 1 {
		return fmt.Errorf("planSearchWorkers must be at least 1")
	}
	if s.PlanSearchStrategy != protocol.V1 && s.PlanSearchStrategy != protocol.V2 {
		return fmt.Errorf("planSearchStrategy must be v1 or v2")
	}
	if s.PlanSearchEvaluations < 1 || s.PlanExactFinalists < 1 || s.PlanPolishActive < 0 {
		return fmt.Errorf("planSearchEvaluations and planExactFinalists must be at least 1; planPolishActive must be nonnegative (0 searches all plans)")
	}
	return nil
}

func (s SchedulerSettings) validateCapacityNumerics() error {
	if s.CapacityGridSteps < 1 || s.CapacityTailSamples < 1 || !schedulerFinite(s.CapacityBisectionWidth) ||
		s.CapacityBisectionWidth <= 0 || s.CapacityBisectionWidth > 1 {
		return fmt.Errorf("capacityGridSteps and capacityTailSamples must be positive; capacityBisectionWidth must be finite in (0,1]")
	}
	return nil
}

func (s SchedulerSettings) validateExcludedNodes() error {
	excluded := map[string]bool{}
	for _, name := range s.ExcludedNodes {
		if name == "" || excluded[name] {
			return fmt.Errorf("excludedNodes entries need unique nonempty Node names")
		}
		excluded[name] = true
	}
	return nil
}

// SchedulerSettingsInput is the settings decode boundary; it tells an omitted
// completionMarginMS apart from zero.
type SchedulerSettingsInput struct {
	SchedulerSettings
	CompletionMarginMS      *float64 `json:"completionMarginMS"`
	CapacityGridSteps       *int     `json:"capacityGridSteps"`
	CapacityBisectionWidth  *float64 `json:"capacityBisectionWidth"`
	CapacityTailSamples     *int     `json:"capacityTailSamples"`
	PlanSearchStrategy      *string  `json:"planSearchStrategy,omitempty"`
	SLOPriorityStrategy     *string  `json:"sloPriorityStrategy,omitempty"`
	PlanSearchEvaluations   *int     `json:"planSearchEvaluations,omitempty"`
	PlanExactFinalists      *int     `json:"planExactFinalists,omitempty"`
	PlanPolishActive        *int     `json:"planPolishActive,omitempty"`
	PlanPostFilterFinalists *bool    `json:"planPostFilterFinalists,omitempty"`
	PlanPricedStart         *bool    `json:"planPricedStart,omitempty"`
}

// Settings returns validated settings carrying the supplied margin.
func (in SchedulerSettingsInput) Settings() (SchedulerSettings, error) {
	if in.CompletionMarginMS == nil {
		return SchedulerSettings{}, fmt.Errorf("completionMarginMS required")
	}
	settings := in.SchedulerSettings.Clone()
	settings.SLOPriorityStrategy = SLOPriorityExcess
	if in.SLOPriorityStrategy != nil {
		if *in.SLOPriorityStrategy == "" {
			return SchedulerSettings{}, fmt.Errorf("sloPriorityStrategy must be excess or workload")
		}
		settings.SLOPriorityStrategy = *in.SLOPriorityStrategy
	}
	if settings.WorkloadPriority == nil {
		settings.WorkloadPriority = []string{}
	}
	settings.PlanSearchStrategy, settings.PlanSearchEvaluations = protocol.V2, 500000
	settings.PlanExactFinalists, settings.PlanPolishActive, settings.PlanPostFilterFinalists = 16, 8, false
	settings.PlanPricedStart = true
	if in.PlanSearchStrategy != nil {
		settings.PlanSearchStrategy = *in.PlanSearchStrategy
	}
	if in.PlanSearchEvaluations != nil {
		settings.PlanSearchEvaluations = *in.PlanSearchEvaluations
	}
	if in.PlanExactFinalists != nil {
		settings.PlanExactFinalists = *in.PlanExactFinalists
	}
	if in.PlanPolishActive != nil {
		settings.PlanPolishActive = *in.PlanPolishActive
	}
	if in.PlanPostFilterFinalists != nil {
		settings.PlanPostFilterFinalists = *in.PlanPostFilterFinalists
	}
	if in.PlanPricedStart != nil {
		settings.PlanPricedStart = *in.PlanPricedStart
	}
	settings.CapacityGridSteps, settings.CapacityBisectionWidth, settings.CapacityTailSamples = 20, 0.005, 100
	if in.CapacityGridSteps != nil {
		settings.CapacityGridSteps = *in.CapacityGridSteps
	}
	if in.CapacityBisectionWidth != nil {
		settings.CapacityBisectionWidth = *in.CapacityBisectionWidth
	}
	if in.CapacityTailSamples != nil {
		settings.CapacityTailSamples = *in.CapacityTailSamples
	}
	settings.CompletionMarginMS = *in.CompletionMarginMS
	return settings, settings.Validate()
}

// SmoothWeightedRoute is shared by ingress and replay. State belongs to a UID's
// ordered route and persists between calls; unavailable targets gain no credit.
func SmoothWeightedRoute(weights []int32, current []int64, available []bool) int {
	best := -1
	var total int64
	for i, weight := range weights {
		if weight <= 0 || !available[i] {
			continue
		}
		total += int64(weight)
		current[i] += int64(weight)
		if best < 0 || current[i] > current[best] {
			best = i
		}
	}
	if best >= 0 {
		current[best] -= total
	}
	return best
}

func schedulerFinite(x float64) bool       { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func schedulerUnitFraction(x float64) bool { return schedulerFinite(x) && x >= 0 && x <= 1 }
func schedulerState(i SchedulerInstance) string {
	profile := ""
	if i.Profile != nil {
		profile = i.Profile.ID
	}
	return fmt.Sprintf("%s:%s:%d", profile, i.Allocation.GPUUUID, i.Allocation.MemoryBytes)
}
func SchedulerInstanceState(i SchedulerInstance) string { return schedulerState(i) }

// schedulerCostState drops the memory field of a schedulerState: memory is a
// measured placement constraint, not part of the measured operation condition
// (as in schedulerSameResourceChoice), so a planned allocation with queue
// headroom matches the cost measured at the bare peak. Other states pass.
func schedulerCostState(state string) string {
	memory := strings.LastIndexByte(state, ':')
	if memory < 0 {
		return state
	}
	return state[:memory]
}
