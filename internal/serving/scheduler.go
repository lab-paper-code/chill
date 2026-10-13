package serving

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// SchedulerSettings separates the observed population from the control cadence
// and the interval over which every candidate's complete pool cost is compared.
type SchedulerSettings struct {
	ObservationWindow  time.Duration     `json:"observationWindowNanos"`
	ControlInterval    time.Duration     `json:"controlIntervalNanos"`
	Horizon            time.Duration     `json:"horizonNanos"`
	MaxObservationAge  time.Duration     `json:"maxObservationAgeNanos"`
	Seed               int64             `json:"seed"`
	Replays            int               `json:"replays"`
	MaxCandidates      int               `json:"maxCandidates"`
	MaxReplicas        int               `json:"maxReplicas"`
	MaxInstances       int               `json:"maxInstances"`
	MaxReplayRequests  int               `json:"maxReplayRequests"`
	MaxCPUs            int               `json:"maxCPUs"`
	ServingMemoryBytes uint64            `json:"servingMemoryBytes"`
	Controls           SchedulerControls `json:"controls"`
}
type SchedulerControls struct {
	Enabled    bool `json:"enabled"`
	Placement  bool `json:"placement"`
	Resources  bool `json:"resources"`
	Replicas   bool `json:"replicas"`
	Sharing    bool `json:"sharing"`
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
type SchedulerNode struct {
	Node        NodeReference         `json:"node"`
	Pool        string                `json:"pool"`
	PowerMode   string                `json:"powerMode"`
	Ready       bool                  `json:"ready"`
	CPUs        []int                 `json:"cpus"`
	GPUUUIDs    []string              `json:"gpuUUIDs"`
	MemoryBytes uint64                `json:"memoryBytes"`
	Reserved    SchedulerAllocation   `json:"reserved"`
	NonServing  []SchedulerAllocation `json:"nonServing"`
	ObservedAt  time.Time             `json:"observedAt"`
	Reason      string                `json:"reason,omitempty"`
}
type SchedulerInstance struct {
	ID             string                     `json:"id"`
	Node           NodeReference              `json:"node"`
	Pool           string                     `json:"pool"`
	Profile        *Profile                   `json:"profile"`
	WorkloadUIDs   []string                   `json:"workloadUIDs"`
	Allocation     SchedulerAllocation        `json:"allocation"`
	Ready          bool                       `json:"ready"`
	Stopped        bool                       `json:"stopped"`
	Capacity       int                        `json:"capacity"`
	Weight         int32                      `json:"weight"`
	MaxBatch       int                        `json:"maxBatch"`
	MaxWait        time.Duration              `json:"maxWaitNanos"`
	Forward        time.Duration              `json:"forwardNanos"`
	Return         time.Duration              `json:"returnNanos"`
	EnvelopeSource string                     `json:"envelopeSource"`
	ObservedAt     time.Time                  `json:"observedAt"`
	ArrivalAges    map[string][]time.Duration `json:"arrivalAgesNanos"`
	IdleMeter      string                     `json:"idleMeter"`
	IdleScope      string                     `json:"idleScope"`
}

// Options are evidence-bound placements supplied by the catalog, not invented
// allocations. CompatibleUIDs permits sharing only after the catalog has checked
// the model, runtime and full input contract for every listed immutable definition.
type SchedulerOption struct {
	Instance       SchedulerInstance `json:"instance"`
	CompatibleUIDs []string          `json:"compatibleUIDs"`
}
type SchedulerRequest struct {
	ID                    string         `json:"id"`
	InputPath             int            `json:"inputPath"`
	QueueOrder            int            `json:"queueOrder"`
	RemainingLifetime     *time.Duration `json:"remainingLifetimeNanos,omitempty"`
	Definition            *Definition    `json:"definition"`
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
	InputPath   int           `json:"inputPath"`
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
}
type Action struct {
	ID         string        `json:"id"`
	Kind       string        `json:"kind"` // prepare, route, stop, drain, remove, power, membership
	Node       NodeReference `json:"node"`
	Instance   string        `json:"instance,omitempty"`
	From       string        `json:"from,omitempty"`
	To         string        `json:"to,omitempty"`
	DependsOn  []string      `json:"dependsOn"`
	CostID     string        `json:"costID,omitempty"`
	Status     string        `json:"status"` // planned, running, complete, failed, cancel
	Elapsed    time.Duration `json:"elapsedNanos"`
	Cancelable bool          `json:"cancelable"`
	Reason     string        `json:"reason,omitempty"`
}
type SchedulerSnapshot struct {
	At               time.Time                           `json:"at"`
	Workloads        []*Definition                       `json:"workloads"`
	Nodes            []SchedulerNode                     `json:"nodes"`
	Instances        []SchedulerInstance                 `json:"instances"`
	Routing          map[string]map[string]int32         `json:"routing"`
	RoutingCredits   map[string]map[int]map[string]int64 `json:"routingCredits"`
	Outstanding      []SchedulerRequest                  `json:"outstanding"`
	Arrivals         []SchedulerArrival                  `json:"arrivals"`
	Options          []SchedulerOption                   `json:"options"`
	Baselines        []NodeBaseline                      `json:"baselines"`
	Costs            []OperationCost                     `json:"costs"`
	Actions          []Action                            `json:"actions"`
	Reasons          []string                            `json:"reasons"`
	References       []string                            `json:"references"`
	InProgressTarget *SchedulerCandidate                 `json:"inProgressTarget,omitempty"`
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
	Nodes           []SchedulerNode             `json:"nodes"`
	Instances       []SchedulerInstance         `json:"instances"`
	Routing         map[string]map[string]int32 `json:"routing"`
	Actions         []Action                    `json:"actions"`
	Status          string                      `json:"status"`
	Reasons         []string                    `json:"reasons"`
	SLO             []SchedulerEstimate         `json:"slo"`
	ExpectedJoules  *float64                    `json:"expectedJoules"`
	WorstExceedance float64                     `json:"worstExceedance"`
	Assumptions     []string                    `json:"assumptions"`
	Sources         []string                    `json:"sources"`
}
type SchedulerSearch struct {
	Examined      int               `json:"examined"`
	Truncated     bool              `json:"truncated"`
	Bounds        SchedulerSettings `json:"bounds"`
	WindowStart   time.Time         `json:"windowStart"`
	WindowEnd     time.Time         `json:"windowEnd"`
	Received      int               `json:"received"`
	Accepted      int               `json:"accepted"`
	ComputeInputs int               `json:"computeInputs"`
	Population    string            `json:"population"`
	Loads         []SchedulerLoad   `json:"loads"`
}
type SchedulerLoad struct {
	UID             string  `json:"uid"`
	Received        int     `json:"received"`
	Accepted        int     `json:"accepted"`
	ComputeInputs   int     `json:"computeInputs"`
	InputsPerSecond float64 `json:"inputsPerSecond"`
}
type SchedulerDecision struct {
	At         time.Time            `json:"at"`
	Status     string               `json:"status"`
	Keep       SchedulerCandidate   `json:"keep"`
	Target     *SchedulerCandidate  `json:"target"`
	Candidates []SchedulerCandidate `json:"candidates"`
	Reasons    []string             `json:"reasons"`
	Settings   SchedulerSettings    `json:"settings"`
	Search     SchedulerSearch      `json:"search"`
	References []string             `json:"references"`
}

func (s SchedulerSettings) Validate() error {
	if s.ObservationWindow <= 0 || s.ControlInterval <= 0 || s.Horizon <= 0 || s.MaxObservationAge <= 0 {
		return fmt.Errorf("scheduler time intervals must be positive")
	}
	if s.Replays <= 0 || s.MaxCandidates < 1 || s.MaxReplicas < 1 || s.MaxInstances < 1 || s.MaxReplayRequests < 1 || s.MaxCPUs < 1 {
		return fmt.Errorf("scheduler search and replay bounds must be positive")
	}
	return nil
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
