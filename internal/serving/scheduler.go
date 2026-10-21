package serving

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// SchedulerSettings separates the observed population from the control cadence
// and the comparison period: every plan's complete pool energy is compared over
// T = max(ComparisonPeriod, the longest transition chain). AutonomousWorkloads
// lists the Workload UIDs under autonomous control.
// MaxReplicas, MaxInstances and MaxCPUs gate node plans: a plan holds at most
// MaxReplicas instances of one Workload and MaxInstances instances in total,
// each with at most MaxCPUs CPUs.
type SchedulerSettings struct {
	ObservationWindow   time.Duration `json:"observationWindowNanos"`
	ControlInterval     time.Duration `json:"controlIntervalNanos"`
	ComparisonPeriod    time.Duration `json:"comparisonPeriodNanos"`
	MaxObservationAge   time.Duration `json:"maxObservationAgeNanos"`
	MaxReplicas         int           `json:"maxReplicas"`
	MaxInstances        int           `json:"maxInstances"`
	MaxCPUs             int           `json:"maxCPUs"`
	ServingMemoryBytes  uint64        `json:"servingMemoryBytes"`
	CompletionMarginMS  float64       `json:"completionMarginMS"`
	AutonomousWorkloads []string      `json:"autonomousWorkloads"`
	// ExcludedNodes names Nodes the planner keeps as they are, like
	// measurement Nodes: no placement, removal, power change or lowering.
	ExcludedNodes []string `json:"excludedNodes"`
	// Capacity rows judge CapacityWindows Poisson windows of at least
	// CapacityWindow and CapacityMinimumRequests arrivals; a rate is met when
	// CapacityPassingWindows of them meet the SLO.
	CapacityWindow          time.Duration     `json:"capacityWindowNanos"`
	CapacityWindows         int               `json:"capacityWindows"`
	CapacityPassingWindows  int               `json:"capacityPassingWindows"`
	CapacityMinimumRequests int               `json:"capacityMinimumRequests"`
	Controls                SchedulerControls `json:"controls"`
}

// DispatchOccupancy is the measured time per request that an instance of
// Model on Node in PowerMode running RuntimeImage stays busy beyond its profile
// service samples: 1/X − (the profile's mean service at batch b)/b, where X is
// the sustained completions per second of one saturated instance at batch b,
// and 0 when that is negative. Capacity replay adds it per request to every
// batch it starts; Source names the measurement record.
type DispatchOccupancy struct {
	Node         NodeReference `json:"node"`
	PowerMode    string        `json:"powerMode"`
	RuntimeImage string        `json:"runtimeImage"`
	Model        string        `json:"model"`
	PerRequest   time.Duration `json:"perRequestNanos"`
	Source       string        `json:"source"`
}

// dispatchOccupancy is the measurement for instance i's Node, power mode,
// runtime image and model, or nil when none exists.
func dispatchOccupancy(measured []DispatchOccupancy, i SchedulerInstance) *DispatchOccupancy {
	if i.Profile == nil {
		return nil
	}
	for _, o := range measured {
		if o.Node.UID == i.Node.UID && o.PowerMode == i.Profile.Condition.PowerMode &&
			o.RuntimeImage == i.Profile.Condition.RuntimeImage && o.Model == i.Profile.Condition.LogicalModel {
			return &o
		}
	}
	return nil
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
	CPUs        []int  `json:"cpus"`
	GPUUUID     string `json:"gpuUUID,omitempty"`
	MemoryBytes uint64 `json:"memoryBytes"`
	Threads     int    `json:"threads"`
	CPUCount    int    `json:"cpuCount"`
	CPUReserved bool   `json:"cpuReserved"`
}

// Node reasons: configuration a Node of the entire POOL lacks.
const (
	NodePlacementInventoryMissing  = "PlacementInventoryMissing"
	NodePlacementBudgetUnavailable = "PlacementBudgetUnavailable"
	NodePowerModeUnavailable       = "PowerModeUnavailable"
	// NodeInstanceUnavailable: a CHILL serving instance on the Node has no
	// readable native or profile state. The Node keeps its state; other Nodes
	// are still planned.
	NodeInstanceUnavailable = "InstanceUnavailable"
	// NodeExcluded: settings.excludedNodes names the Node; it is never planned.
	NodeExcluded = "ExcludedNode"
)

// SchedulerNode is one Node of the entire POOL. Measurement marks the
// measurement POOL; Reasons name configuration the Node lacks.
type SchedulerNode struct {
	Node        NodeReference `json:"node"`
	PowerMode   string        `json:"powerMode"`
	Ready       bool          `json:"ready"`
	Measurement bool          `json:"measurement,omitempty"`
	CPUs        []int         `json:"cpus"`
	// ModeCPUs holds, by power mode, the CPUs online in that mode where the
	// DeviceClass declares them; CPUs are the current mode's.
	ModeCPUs    map[string][]int      `json:"modeCPUs,omitempty"`
	GPUUUIDs    []string              `json:"gpuUUIDs"`
	MemoryBytes uint64                `json:"memoryBytes"`
	Reserved    SchedulerAllocation   `json:"reserved"`
	NonServing  []SchedulerAllocation `json:"nonServing"`
	ObservedAt  time.Time             `json:"observedAt"`
	Reasons     []string              `json:"reasons,omitempty"`
}

// AtMode is the Node with the CPUs online in mode, and false when they are
// unknown: the current mode reads its published CPUs, another mode needs its
// DeviceClass declaration.
func (n SchedulerNode) AtMode(mode string) (SchedulerNode, bool) {
	if mode == n.PowerMode {
		return n, true
	}
	cpus, ok := n.ModeCPUs[mode]
	if !ok {
		return n, false
	}
	n.CPUs = cpus
	return n, true
}

type SchedulerInstance struct {
	ID               string        `json:"id"`
	TargetID         string        `json:"targetID,omitempty"`
	OwnerWorkloadUID string        `json:"ownerWorkloadUID,omitempty"`
	Node             NodeReference `json:"node"`
	// Profile is the catalog body in memory; stored records keep only ProfileID.
	Profile   *Profile `json:"profile,omitempty"`
	ProfileID string   `json:"profileID,omitempty"`
	// WorkloadUID is the one Workload the instance serves; empty when unrouted.
	WorkloadUID    string              `json:"workloadUID"`
	Allocation     SchedulerAllocation `json:"allocation"`
	Ready          bool                `json:"ready"`
	Stopped        bool                `json:"stopped"`
	Capacity       int                 `json:"capacity"`
	Weight         int32               `json:"weight"`
	MaxBatch       int                 `json:"maxBatch"`
	FixedBatchSize int                 `json:"fixedBatchSize,omitempty"` // Strict baseline B; autonomous control never moves it.
	MaxWait        time.Duration       `json:"maxWaitNanos"`
	ObservedAt     time.Time           `json:"observedAt"`
	IdleMeter      string              `json:"idleMeter"`
	IdleScope      string              `json:"idleScope"`
	// UnroutedSince is when the instance last lost every positive route weight
	// (nil while routed); after one observation window unrouted it is removed.
	UnroutedSince *time.Time `json:"unroutedSince,omitempty"`
	// HeldUntil keeps an instance prepared while keep was in Overload from
	// removal and replacement for one observation window after it became Ready.
	HeldUntil *time.Time `json:"heldUntil,omitempty"`
}

// Options are evidence-bound placements supplied by the catalog, not invented
// allocations. CompatibleUIDs lists the immutable definitions whose model,
// runtime and full input contract the catalog checked against the profile.
type SchedulerOption struct {
	Instance       SchedulerInstance `json:"instance"`
	CompatibleUIDs []string          `json:"compatibleUIDs"`
}
type SchedulerRequest struct {
	ID                    string         `json:"id"`
	QueueOrder            int            `json:"queueOrder"`
	RemainingLifetime     *time.Duration `json:"remainingLifetimeNanos,omitempty"`
	Definition            *Definition    `json:"definition"`
	DefinitionID          string         `json:"definitionID,omitempty"` // Stored records keep Definition.Identity().
	ReceivedAt            time.Time      `json:"receivedAt"`
	Elapsed               time.Duration  `json:"elapsedNanos"`
	QueueAge              time.Duration  `json:"queueAgeNanos"`
	Phase                 string         `json:"phase"` // pending, assigned, queued, deferred, running, returning
	Instance              string         `json:"instance,omitempty"`
	BatchID               string         `json:"batchID,omitempty"`
	RunningElapsed        time.Duration  `json:"runningElapsedNanos"`
	ReturnElapsed         time.Duration  `json:"returnElapsedNanos"`
	ArrivalRate           float64        `json:"arrivalRate"`
	NativeSenderElapsedMS *float64       `json:"nativeSenderElapsedMS,omitempty"`
	NativeOutcome         string         `json:"nativeOutcome,omitempty"`
}
type SchedulerArrival struct {
	ID          string        `json:"id"`
	Lifetime    time.Duration `json:"lifetimeNanos"`
	At          time.Time     `json:"at"`
	Definition  *Definition   `json:"definition,omitempty"`
	Accepted    bool          `json:"accepted"`
	Compute     bool          `json:"compute"`
	WorkloadUID string        `json:"workloadUID,omitempty"`
	Outcome     string        `json:"outcome,omitempty"`
}
type NodeBaseline struct {
	ID         string                       `json:"id"`
	Node       NodeReference                `json:"node"`
	PowerMode  string                       `json:"powerMode"`
	State      []string                     `json:"state"` // sorted profile ID + allocation identity, once per physical instance
	Watts      float64                      `json:"watts"`
	Meter      string                       `json:"meter"`
	Scope      string                       `json:"scope"` // node-exclusive-wall
	MeasuredAt time.Time                    `json:"measuredAt"`
	Reference  evidencestore.ObjectIdentity `json:"reference"`
}
type OperationTrial struct {
	Duration time.Duration `json:"durationNanos"`
	Joules   float64       `json:"joules"`
}
type OperationCost struct {
	ID                  string                       `json:"id"`
	Node                NodeReference                `json:"node"`
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
}
type Action struct {
	ID        string        `json:"id"`
	Kind      string        `json:"kind"` // prepare, route, stop, drain, remove, power
	Node      NodeReference `json:"node"`
	Instance  string        `json:"instance,omitempty"`
	From      string        `json:"from,omitempty"`
	To        string        `json:"to,omitempty"`
	DependsOn []string      `json:"dependsOn"`
	CostID    string        `json:"costID,omitempty"`
	// Fixed lowers an available Node to its lowest power mode; every candidate shares it.
	Fixed      bool          `json:"fixed,omitempty"`
	Status     string        `json:"status"` // planned, running, complete, failed, cancel
	Elapsed    time.Duration `json:"elapsedNanos"`
	Cancelable bool          `json:"cancelable"`
	Reason     string        `json:"reason,omitempty"`
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
	Options        []SchedulerOption           `json:"options"`
	Baselines      []NodeBaseline              `json:"baselines"`
	BaselineRefs   []string                    `json:"baselineRefs,omitempty"`
	Costs          []OperationCost             `json:"costs"`
	// DispatchOccupancy holds the latest accepted measurement per Node, power
	// mode, runtime image and model.
	DispatchOccupancy []DispatchOccupancy `json:"dispatchOccupancy"`
	CostRefs          []string            `json:"costRefs,omitempty"`
	Actions           []Action            `json:"actions"`
	Reasons           []string            `json:"reasons"`
	References        []string            `json:"references"`
	InProgressTarget  *SchedulerCandidate `json:"inProgressTarget,omitempty"`
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
	ID              string                      `json:"id"`
	Keep            bool                        `json:"keep"`
	Nodes           []SchedulerNode             `json:"nodes,omitempty"`
	Instances       []SchedulerInstance         `json:"instances,omitempty"`
	Routing         map[string]map[string]int32 `json:"routing,omitempty"`
	Actions         []Action                    `json:"actions"`
	Status          string                      `json:"status"`
	Reasons         []string                    `json:"reasons"`
	SLO             []SchedulerEstimate         `json:"slo,omitempty"`
	ExpectedJoules  *float64                    `json:"expectedJoules"`
	WorstExceedance float64                     `json:"worstExceedance"`
	Assumptions     []string                    `json:"assumptions,omitempty"`
	Sources         []string                    `json:"sources,omitempty"`
}
type SchedulerSearch struct {
	// Examined counts the node plans built for the decision.
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
type SchedulerLoad struct {
	UID             string  `json:"uid"`
	Received        int     `json:"received"`
	Accepted        int     `json:"accepted"`
	ComputeInputs   int     `json:"computeInputs"`
	InputsPerSecond float64 `json:"inputsPerSecond"`
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
	At         time.Time                `json:"at"`
	Status     string                   `json:"status"`
	Keep       SchedulerCandidate       `json:"keep"`
	Target     *SchedulerCandidate      `json:"target"`
	Reasons    []string                 `json:"reasons"`
	Settings   SchedulerSettings        `json:"settings"`
	Search     SchedulerSearch          `json:"search"`
	Workloads  []SchedulerWorkloadState `json:"workloads"`
	References []string                 `json:"references"`
	Capacity   *SchedulerCapacity       `json:"capacity,omitempty"`
	// EvaluationElapsed is the monotonic Tick time from capture to the decision.
	EvaluationElapsed time.Duration `json:"evaluationElapsedNanos"`
}

func (s SchedulerSettings) Validate() error {
	if s.ObservationWindow <= 0 || s.ControlInterval <= 0 || s.ComparisonPeriod <= 0 || s.MaxObservationAge <= 0 {
		return fmt.Errorf("scheduler time intervals must be positive")
	}
	if s.MaxReplicas < 1 || s.MaxInstances < 1 || s.MaxCPUs < 1 {
		return fmt.Errorf("maxReplicas, maxInstances and maxCPUs must be positive")
	}
	if math.IsNaN(s.CompletionMarginMS) || math.IsInf(s.CompletionMarginMS, 0) || s.CompletionMarginMS < 0 {
		return fmt.Errorf("completionMarginMS must be finite and nonnegative")
	}
	if s.CapacityWindow <= 0 || s.CapacityWindows < 1 || s.CapacityPassingWindows < 1 || s.CapacityPassingWindows > s.CapacityWindows || s.CapacityMinimumRequests < 1 {
		return fmt.Errorf("capacity settings need capacityWindowNanos > 0, capacityWindows >= 1, 1 <= capacityPassingWindows <= capacityWindows and capacityMinimumRequests >= 1")
	}
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
	CompletionMarginMS *float64 `json:"completionMarginMS"`
}

// Settings returns validated settings carrying the supplied margin.
func (in SchedulerSettingsInput) Settings() (SchedulerSettings, error) {
	if in.CompletionMarginMS == nil {
		return SchedulerSettings{}, fmt.Errorf("completionMarginMS required")
	}
	settings := in.SchedulerSettings
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

func schedulerFinite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func schedulerHas(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func schedulerState(i SchedulerInstance) string {
	cpus := append([]int(nil), i.Allocation.CPUs...)
	sort.Ints(cpus)
	profile := ""
	if i.Profile != nil {
		profile = i.Profile.ID
	}
	return fmt.Sprintf("%s:%v:%s:%d:%d", profile, cpus, i.Allocation.GPUUUID, i.Allocation.MemoryBytes, i.Allocation.Threads)
}
func SchedulerInstanceState(i SchedulerInstance) string { return schedulerState(i) }

// schedulerCostState drops the memory field of a schedulerState: memory is a
// measured placement constraint, not part of the measured operation condition
// (as in schedulerSameResourceChoice), so a planned allocation with queue
// headroom matches the cost measured at the bare peak. Other states pass.
func schedulerCostState(state string) string {
	if strings.Count(state, ":") < 4 {
		return state
	}
	threads := strings.LastIndexByte(state, ':')
	memory := strings.LastIndexByte(state[:threads], ':')
	return state[:memory] + state[threads:]
}
