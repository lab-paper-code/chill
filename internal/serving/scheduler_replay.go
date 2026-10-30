package serving

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"gonum.org/v1/gonum/stat"
)

const (
	schedulerStatusPending  = "Pending"
	schedulerStatusFeasible = "Feasible"
	SchedulerStatusOverload = "Overload"
	schedulerStatusLifetime = "InfeasibleLifetime"
	schedulerNodeWallScope  = "node-exclusive-wall"
	schedulerActionPrepare  = "prepare"
	schedulerActionRemove   = "remove"
	schedulerActionPower    = "power"
	schedulerActionRoute    = "route"
	schedulerActionStop     = "stop"
	schedulerActionDrain    = "drain"
	schedulerActionPlanned  = "planned"
	schedulerActionComplete = "complete"
	schedulerActionFailed   = "failed"
	schedulerActionCancel   = "cancel"
)

type schedulerReplayRequest struct {
	source       SchedulerRequest
	identity     string // source.Definition.Identity(), formatted once
	arrival      float64
	native       float64
	queueAt      float64
	senderMS     float64
	completed    bool
	dispatched   bool
	returning    bool
	completion   float64
	observed     bool
	running      bool
	expired      bool
	deferred     bool
	dropped      bool
	expires      float64
	capacityMiss bool // counted once against the capacity window's fixed population
}
type schedulerReplayInstance struct {
	config       SchedulerInstance
	loaded       bool
	ready        bool
	stopped      bool
	queue        []*schedulerReplayRequest
	batch        []*schedulerReplayRequest
	finish       float64
	wake         float64
	outstanding  int
	policy       firstfloor.Policy
	arrivalTimes map[string][]float64
	occupancy    float64 // measured dispatch occupancy per request (ns); 0 without a measurement
}
type schedulerReplayAction struct {
	action   Action
	started  bool
	done     bool
	start    float64
	finish   float64
	trial    OperationTrial
	wallRate float64
}
type schedulerReplayResult struct {
	energy      float64
	samples     map[string][]float64
	definitions map[string]*Definition
	reasons     []string
	assumptions []string
	sources     []string
	outcomes    map[string]map[string]int
}

// schedulerReplayEstimate replays candidate c once with seed and records each
// definition's SLO-percentile estimate and predicted outcomes in c. Energy
// counts the compared Nodes only (nil counts every Node). Reasons name the
// snapshot, resource or replay facts that block the replay.
func schedulerReplayEstimate(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, c SchedulerCandidate, seed uint64, compared map[NodeReference]bool, capacity *capacityWindowLimit) (SchedulerCandidate, error) {
	c.Status = schedulerStatusPending
	c.Reasons = append(c.Reasons, snapshot.Reasons...)
	c.Reasons = append(c.Reasons, schedulerValidateSnapshot(settings, snapshot)...)
	c.Reasons = append(c.Reasons, schedulerValidateResources(settings, c.Nodes, c.Instances)...)
	if len(c.Reasons) != 0 {
		return c, ctx.Err()
	}
	r, err := schedulerReplay(ctx, settings, snapshot, c, rand.New(rand.NewPCG(seed, 0)), compared, capacity)
	if err != nil {
		return c, err
	}
	if capacity != nil && capacity.failed {
		return c, nil // Partial samples are not an estimate.
	}
	c.Reasons, c.Assumptions, c.Sources = appendUnique(c.Reasons, r.reasons...), r.assumptions, r.sources
	if len(c.Reasons) != 0 {
		return c, ctx.Err()
	}
	energy := r.energy
	c.ExpectedJoules = &energy
	c.Status = schedulerStatusFeasible
	for _, d := range snapshot.Workloads {
		r.definitions[d.Identity()] = d
	}
	keys := []string{}
	for key := range r.definitions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		d := r.definitions[key]
		samples := r.samples[key]
		e := SchedulerEstimate{UID: d.UID, Definition: key, Samples: len(samples), Percentile: d.SLO.Percentile, TargetNanos: int64(d.SLO.TargetLatency), PredictedOutcomes: r.outcomes[key]}
		if len(samples) > 0 {
			sort.Float64s(samples)
			q := stat.Quantile(d.SLO.Percentile/100, stat.Empirical, samples, nil)
			e.ExpectedNanos = &q
			e.NormalizedExceedance = math.Max(0, q/float64(d.SLO.TargetLatency)-1)
			c.WorstExceedance = math.Max(c.WorstExceedance, e.NormalizedExceedance)
		}
		c.SLO = append(c.SLO, e)
	}
	if c.WorstExceedance > 0 {
		c.Status = SchedulerStatusOverload
	}
	for _, counts := range r.outcomes {
		if counts["timeout"] > 0 && c.Status != schedulerStatusLifetime {
			c.Status = SchedulerStatusOverload
		}
		for kind, count := range counts {
			if strings.HasPrefix(kind, "expired-") && count > 0 {
				c.Status = schedulerStatusLifetime
			}
		}
	}
	return c, ctx.Err()
}

// schedulerRouted reports whether some instance of c receives uid's requests.
func schedulerRouted(c SchedulerCandidate, uid string) bool {
	for _, i := range c.Instances {
		weight := int32(0)
		if i.WorkloadUID == uid {
			weight = i.Weight
		}
		if route, explicit := c.Routing[uid]; explicit {
			weight = route[i.ID]
		}
		if weight > 0 {
			return true
		}
	}
	return false
}

// SchedulerFixedBatchAutonomy rejects autonomy for a Workload still routed to a
// strict fixed-batch baseline; the operator removes that routing first.
func SchedulerFixedBatchAutonomy(snapshot SchedulerSnapshot, autonomous []string) error {
	fixed := SchedulerCandidate{Routing: snapshot.Routing}
	for _, i := range snapshot.Instances {
		if i.FixedBatchSize > 0 {
			fixed.Instances = append(fixed.Instances, i)
		}
	}
	for _, uid := range autonomous {
		if schedulerRouted(fixed, uid) {
			return fmt.Errorf("workload %s is routed to a fixedBatchSize instance; remove that routing before enabling autonomy", uid)
		}
	}
	return nil
}
func appendUnique(xs []string, more ...string) []string {
	for _, s := range more {
		if !slices.Contains(xs, s) {
			xs = append(xs, s)
		}
	}
	return xs
}
func schedulerValidateSnapshot(settings SchedulerSettings, snapshot SchedulerSnapshot) []string {
	reasons := schedulerValidateObservationTimes(&snapshot, settings.MaxObservationAge)
	if len(snapshot.Nodes) == 0 {
		reasons = append(reasons, "MissingPoolInventory")
	}
	ids := map[string]bool{}
	for _, i := range snapshot.Instances {
		ids[i.ID] = true
	}
	for _, r := range snapshot.Outstanding {
		reasons = schedulerValidateRequest(r, ids, reasons)
	}
	for _, a := range snapshot.Arrivals {
		if (a.Compute || a.Accepted) && a.Lifetime < 0 {
			reasons = appendUnique(reasons, "InvalidArrivalLifetime:"+a.ID)
		}
	}
	definitions := append([]*Definition(nil), snapshot.Workloads...)
	for _, r := range snapshot.Outstanding {
		definitions = append(definitions, r.Definition)
	}
	for _, a := range snapshot.Arrivals {
		if a.Compute || a.Accepted {
			definitions = append(definitions, a.Definition)
		}
	}
	for _, d := range definitions {
		if d == nil {
			reasons = appendUnique(reasons, "MissingImmutableDefinition")
			continue
		}
		if d.SLO.Percentile <= 0 || d.SLO.Percentile > 100 || !schedulerFinite(d.SLO.Percentile) || d.SLO.TargetLatency <= 0 {
			reasons = appendUnique(reasons, "InvalidSLO:"+d.UID)
		}
	}
	return reasons
}
func schedulerValidateObservationTimes(snapshot *SchedulerSnapshot, maxAge time.Duration) []string {
	reasons := []string{}
	if snapshot.At.IsZero() || snapshot.ObservationEnd.IsZero() || snapshot.ObservationEnd.After(snapshot.At) {
		reasons = append(reasons, "UnknownOrFutureObservationWindow")
	}
	for _, n := range snapshot.Nodes {
		if n.Node.UID == "" || n.ObservedAt.IsZero() || n.ObservedAt.After(snapshot.At) ||
			snapshot.At.Sub(n.ObservedAt) > maxAge {
			reasons = appendUnique(reasons, "UnknownOrStaleNode:"+n.Node.Name)
		}
	}
	for _, i := range snapshot.Instances {
		if i.Terminated {
			continue // A terminal physical fact never needs another live native probe.
		}
		if schedulerFixedReason(*snapshot, i.WorkloadUID) != "" || schedulerFixedReason(*snapshot, i.OwnerWorkloadUID) != "" {
			continue
		}
		if i.ObservedAt.IsZero() || i.ObservedAt.After(snapshot.At) || snapshot.At.Sub(i.ObservedAt) > maxAge {
			reasons = appendUnique(reasons, "UnknownOrStaleInstance:"+i.ID)
		}
	}
	return reasons
}
func schedulerValidateRequest(r SchedulerRequest, ids map[string]bool, reasons []string) []string {
	if r.Definition == nil || r.ReceivedAt.IsZero() || r.Elapsed < 0 || r.QueueAge < 0 || r.RunningElapsed < 0 {
		reasons = appendUnique(reasons, "UnknownRequestContext:"+r.ID)
		return reasons
	}
	if r.Phase != phasePending && r.Phase != phaseAssigned && r.Phase != phaseQueued && r.Phase != phaseDeferred && r.Phase != phaseRunning && r.Phase != phaseReturning {
		reasons = appendUnique(reasons, "UnknownRequestPhase:"+r.ID)
	}
	if r.Phase != phasePending && !ids[r.Instance] {
		reasons = appendUnique(reasons, "UnknownNativeInstance:"+r.ID)
	}
	if r.Phase == phaseRunning && r.BatchID == "" {
		reasons = appendUnique(reasons, "UnknownRunningBatch:"+r.ID)
	}
	if r.RemainingLifetime != nil && *r.RemainingLifetime < 0 {
		reasons = appendUnique(reasons, "InvalidRequestLifetime:"+r.ID)
	}
	if r.Phase == phaseReturning && r.ReturnElapsed < 0 {
		reasons = appendUnique(reasons, "MissingReturningCompletionEvidence:"+r.ID)
	}
	if (r.Phase == phaseQueued || r.Phase == phaseDeferred || r.Phase == phaseRunning) && (r.NativeSenderElapsedMS == nil || !schedulerFinite(*r.NativeSenderElapsedMS) || *r.NativeSenderElapsedMS < 0) {
		reasons = appendUnique(reasons, "MissingNativeSenderContext:"+r.ID)
	}
	return reasons
}

// schedulerValidateResources checks the budget of every Node that hosts an
// instance; a Node without instances stays in the POOL with its own reasons.
func schedulerValidateResources(settings SchedulerSettings, nodes []SchedulerNode, instances []SchedulerInstance) []string {
	reasons := []string{}
	seen := map[string]bool{}
	for _, n := range nodes {
		if seen[n.Node.UID] {
			reasons = appendUnique(reasons, "DuplicateNode:"+n.Node.UID)
		}
		seen[n.Node.UID] = true
		allocations := append([]SchedulerAllocation{n.Reserved}, n.NonServing...)
		hosted := false
		mode := ""
		projectMode := false
		for _, i := range instances {
			if i.Node != n.Node {
				continue
			}
			hosted = true
			// Retained occupancy is not an executable candidate. Its missing
			// native evidence fixes this Node, but must not reject other Nodes.
			if i.Terminated || !i.Ready && slices.Contains(n.Reasons, NodeInstanceUnavailable) {
				allocations = append(allocations, i.Allocation)
				continue
			}
			if i.Profile == nil {
				reasons = appendUnique(reasons, "MissingProfile:"+i.ID)
				continue
			}
			mode, projectMode = i.Profile.Condition.PowerMode, true
			reasons = schedulerValidateInstance(settings, n, i, reasons)
			allocations = append(allocations, i.Allocation)
		}
		if hosted {
			// Retained-only occupancy uses observed CPUs, not an unconfirmed
			// power target. Executable candidates need the target projection.
			at, known := n, true
			if projectMode {
				at, known = n.AtMode(mode)
			}
			if known {
				reasons = ValidateNodeBudget(at, allocations, reasons)
			} else {
				reasons = appendUnique(reasons, NodePlacementInventoryMissing+":"+n.Node.UID)
			}
		}
	}
	for _, i := range instances {
		if !seen[i.Node.UID] {
			reasons = appendUnique(reasons, "UnknownPlacementNode:"+i.ID)
		}
	}
	return reasons
}
func schedulerValidateInstance(settings SchedulerSettings, n SchedulerNode, i SchedulerInstance, reasons []string) []string {
	if !n.Ready {
		reasons = appendUnique(reasons, "NodeUnavailable:"+n.Node.UID)
	}
	if i.Profile.Condition.NodeUID != n.Node.UID || i.Profile.Condition.PowerMode != n.PowerMode {
		reasons = appendUnique(reasons, "ProfileConditionMismatch:"+i.ID)
	}
	// RKNNLite is measured with no ONNX Runtime threads on its CPU set.
	noThreads := i.Allocation.Threads == 0 && i.Profile.Condition.Backend == "rknnlite"
	if len(i.Allocation.CPUs) == 0 || !noThreads && (i.Allocation.Threads < 1 || i.Allocation.Threads > len(i.Allocation.CPUs)) ||
		i.Allocation.Threads != i.Profile.Condition.CPU.ORTIntraOpThreads {
		reasons = appendUnique(reasons, "CPUThreadAllocationMismatch:"+i.ID)
	}
	measuredAffinity := i.Profile.Condition.CPU.AffinityCPUs
	if runtime := i.Profile.RuntimeMetadata; runtime != nil {
		measuredAffinity = runtime.CPUAffinity
		if i.Allocation.Threads != runtime.IntraOpThreads {
			reasons = appendUnique(reasons, "CPUThreadAllocationMismatch:"+i.ID)
		}
	}
	if len(measuredAffinity) > 0 {
		measured := append([]int(nil), measuredAffinity...)
		actual := append([]int(nil), i.Allocation.CPUs...)
		sort.Ints(measured)
		sort.Ints(actual)
		if !reflect.DeepEqual(measured, actual) {
			reasons = appendUnique(reasons, "MeasuredAffinityMismatch:"+i.ID)
		}
	}
	if i.Capacity < 1 || i.Weight < 0 || i.MaxWait < 0 {
		reasons = appendUnique(reasons, "MissingQueueOrTransportEvidence:"+i.ID)
	}
	if len(i.Profile.Batches) == 0 || i.Profile.ID == "" || len(i.Profile.Evidence) == 0 {
		reasons = appendUnique(reasons, "MissingProfileEvidence:"+i.ID)
	}
	return schedulerValidateService(settings, i, reasons)
}

func schedulerValidateService(settings SchedulerSettings, i SchedulerInstance, reasons []string) []string {
	maxMemory := uint64(0)
	single := false
	for _, b := range i.Profile.Batches {
		if b.Batch > i.MaxBatch {
			continue
		}
		if b.Batch == 1 {
			single = true
		}
		if b.MemoryBytes > maxMemory {
			maxMemory = b.MemoryBytes
		}
		if len(b.DurationNanos) == 0 || b.EnergyPerInputJoules < 0 || !schedulerFinite(b.EnergyPerInputJoules) {
			reasons = appendUnique(reasons, "MissingServiceEnergyEvidence:"+i.ID)
		}
		for _, v := range b.DurationNanos {
			if v <= 0 || !schedulerFinite(v) {
				reasons = appendUnique(reasons, "InvalidServiceSample:"+i.ID)
			}
		}
	}
	if !single {
		reasons = appendUnique(reasons, "MissingSingleBatchEvidence:"+i.ID)
	}
	if maxMemory == 0 || maxMemory > i.Allocation.MemoryBytes || settings.ServingMemoryBytes > i.Allocation.MemoryBytes-maxMemory {
		reasons = appendUnique(reasons, "MeasuredMemoryInfeasible:"+i.ID)
	}
	return reasons
}

// ValidateNodeBudget applies the shared configured CPU/GPU/memory admission
// contract. Reserved CPU coverage is not observed physical CPU isolation.
func ValidateNodeBudget(n SchedulerNode, allocations []SchedulerAllocation, reasons []string) []string {
	cpus := map[int]bool{}
	gpu := map[string]bool{}
	var memory uint64
	cpuBudget := 0
	reservedQuota := 0
	for _, a := range allocations {
		if math.MaxUint64-memory < a.MemoryBytes {
			reasons = appendUnique(reasons, "MemoryBudgetOverflow:"+n.Node.UID)
		} else {
			memory += a.MemoryBytes
		}
		count := a.CPUCount
		if count == 0 {
			count = len(a.CPUs)
		}
		if count < 0 {
			reasons = appendUnique(reasons, "UnknownCPUAllocationCount:"+n.Node.UID)
			continue
		}
		if a.CPUReserved {
			reservedQuota += count
		} else {
			cpuBudget += count
			// Request count and the measured affinity width are distinct. The
			// full mask remains exclusive even when quota/threads use fewer CPUs.
			if count > len(a.CPUs) {
				reasons = appendUnique(reasons, "UnknownExclusiveCPUAffinity:"+n.Node.UID)
			}
		}
		if !a.CPUReserved {
			for _, cpu := range a.CPUs {
				found := false
				for _, v := range n.CPUs {
					if cpu == v {
						found = true
					}
				}
				if !found || cpus[cpu] {
					reasons = appendUnique(reasons, "ExclusiveCPUInfeasible:"+n.Node.UID)
				}
				cpus[cpu] = true
			}
		}
		if a.GPUUUID != "" {
			if !slices.Contains(n.GPUUUIDs, a.GPUUUID) || gpu[a.GPUUUID] {
				reasons = appendUnique(reasons, "ExclusiveGPUInfeasible:"+n.Node.UID)
			}
			gpu[a.GPUUUID] = true
		}
	}
	if cpuBudget > len(n.CPUs) || reservedQuota > len(n.Reserved.CPUs) {
		reasons = appendUnique(reasons, "CPUBudgetInfeasible:"+n.Node.UID)
	}
	if memory > n.MemoryBytes {
		reasons = appendUnique(reasons, "MemoryBudgetInfeasible:"+n.Node.UID)
	}
	return reasons
}

func schedulerPolicy(i SchedulerInstance) firstfloor.Policy {
	p := firstfloor.Policy{
		ProfileID: i.Profile.ID, MaxBatchSize: i.MaxBatch, MaxBatchWaitMS: float64(i.MaxWait) / 1e6,
	}
	if i.Profile.Condition.Firstfloor != nil {
		p.DeadlinePolicy = i.Profile.Condition.Firstfloor.DeadlinePolicy
	}
	p.DeadlinePolicy = firstfloor.EffectiveDeadlinePolicy(p.DeadlinePolicy)
	for _, b := range i.Profile.Batches {
		samples := make([]float64, len(b.DurationNanos))
		for j, v := range b.DurationNanos {
			samples[j] = v / 1e6
		}
		sort.Float64s(samples)
		p.Batches = append(p.Batches, firstfloor.BatchProfile{Size: b.Batch, ServiceSamplesMS: samples, EnergyPerItemJ: b.EnergyPerInputJoules})
	}
	return p
}

type schedulerReplayState struct {
	settings       SchedulerSettings
	snap           SchedulerSnapshot
	c              SchedulerCandidate
	rng            *rand.Rand
	out            schedulerReplayResult
	horizon        float64
	instances      []*schedulerReplayInstance
	byID           map[string]*schedulerReplayInstance
	nodes          []SchedulerNode
	goal           SchedulerCandidate
	applied        SchedulerCandidate
	previousRoute  map[string]map[string]int32
	creditInvalid  map[string]map[string]bool
	actions        []*schedulerReplayAction
	actionByID     map[string]*schedulerReplayAction
	requests       []*schedulerReplayRequest // ordered by arrival
	live           []*schedulerReplayRequest // arrived and not completed, in requests order
	arrived        int                       // requests[:arrived] have arrived
	routeCredits   map[string][]int64        // Workload UID
	routingApplied bool
	weights        []int32
	available      []bool
	now            float64
	// compared limits energy to these Nodes; nil counts every Node.
	compared map[NodeReference]bool
	// chooseBatches scratch, reused across events.
	entries  []schedulerReplayQueued
	states   []firstfloor.RequestState
	capacity *capacityWindowLimit // nil for ordinary candidate estimates
}

type schedulerReplayQueued struct {
	r   *schedulerReplayRequest
	slo float64
}

// compareReplayQueued puts normal requests first by EDF key and keeps deferred
// FIFO. Plain '<' leaves NaN unordered, as the actor's EDF does.
func compareReplayQueued(left, right schedulerReplayQueued) int {
	switch {
	case left.r.deferred != right.r.deferred:
		if left.r.deferred {
			return 1
		}
		return -1
	case left.r.deferred:
		return 0
	case left.slo < right.slo:
		return -1
	case right.slo < left.slo:
		return 1
	}
	return 0
}

func (s *schedulerReplayState) counted(node NodeReference) bool {
	return s.compared == nil || s.compared[node]
}

// schedulerReplay replays candidate c over the horizon; energy counts the
// compared Nodes only (nil counts every Node).
func schedulerReplay(ctx context.Context, settings SchedulerSettings, snap SchedulerSnapshot, c SchedulerCandidate, rng *rand.Rand, compared map[NodeReference]bool, capacity *capacityWindowLimit) (schedulerReplayResult, error) {
	if err := ctx.Err(); err != nil {
		return schedulerReplayResult{}, err
	}
	s := schedulerReplayState{settings: settings, snap: snap, c: c, rng: rng, horizon: float64(settings.ComparisonPeriod), compared: compared, capacity: capacity, out: schedulerReplayResult{samples: map[string][]float64{}, definitions: map[string]*Definition{}, outcomes: map[string]map[string]int{}}}
	s.initializeInstances()
	s.initializeActions()
	s.initializeOutstanding()
	if !s.initializeArrivals(ctx) || !s.initializeRunning() {
		return s.out, ctx.Err()
	}
	s.routeCredits = map[string][]int64{}
	s.routingApplied = c.Keep
	s.weights = make([]int32, len(s.instances))
	s.available = make([]bool, len(s.instances))
	for steps := 1; ; steps++ {
		if err := ctx.Err(); err != nil {
			return schedulerReplayResult{}, err
		}
		if steps > (len(s.requests)+1)*30+len(s.actions)*30+1000 {
			s.out.reasons = append(s.out.reasons, "ReplayEventBoundExceeded")
			return s.out, ctx.Err()
		}
		s.admitArrivals()
		// Complete physical execution before choosing another UID's nonpreemptive batch.
		s.expireRequests()
		s.completeExecution()
		if s.capacityWindowFailed() {
			return s.out, nil
		}
		if !s.completeActions() {
			return s.out, ctx.Err()
		}
		if !s.startActions() {
			return s.out, ctx.Err()
		}
		// Native queue entries never move when the desired configuration changes.
		s.routeRequests()
		s.receiveNative()
		if !s.chooseBatches(ctx) {
			return s.out, ctx.Err()
		}
		next, remaining := s.nextEvent()
		// Zero-duration route/drain can unlock an action at the same timestamp.
		if s.unlockedActions() {
			continue
		}
		if math.IsInf(next, 1) {
			if remaining {
				s.out.reasons = append(s.out.reasons, "NoReadyCapacityOrTransitionPrerequisite")
				return s.out, ctx.Err()
			}
			next = math.Max(s.now, s.horizon)
		}
		if !s.accumulateEnergy(math.Min(next, s.horizon)) {
			return s.out, ctx.Err()
		}
		if !remaining && next >= s.horizon {
			break
		}
		if next <= s.now {
			s.out.reasons = append(s.out.reasons, "ReplayNonAdvancingEvent")
			return s.out, ctx.Err()
		}
		s.now = next
	}
	if !schedulerFinite(s.out.energy) || s.out.energy < 0 {
		s.out.reasons = append(s.out.reasons, "ExpectedEnergyOutsidePhysicalNumericalRange")
	}
	return s.out, ctx.Err()
}

func (s *schedulerReplayState) initializeInstances() {
	s.instances = []*schedulerReplayInstance{}
	s.byID = map[string]*schedulerReplayInstance{}
	s.nodes = append([]SchedulerNode(nil), s.snap.Nodes...)
	s.goal = s.c
	s.applied, s.previousRoute = s.c, s.snap.Routing
	addInstance := func(i SchedulerInstance, loaded bool) {
		x := &schedulerReplayInstance{config: i, loaded: loaded, ready: loaded && i.Ready, stopped: i.Stopped, finish: math.Inf(1), wake: 0, arrivalTimes: map[string][]float64{}}
		if i.Profile != nil {
			x.policy = schedulerPolicy(i)
		}
		if o := dispatchOccupancy(s.snap.DispatchOccupancy, i); o != nil {
			x.occupancy = float64(o.PerRequest)
		}
		s.instances = append(s.instances, x)
		s.byID[i.ID] = x
	}
	for _, i := range s.snap.Instances {
		addInstance(i, true)
	}
	for _, actual := range s.snap.Instances {
		if actual.TargetID == "" {
			continue
		}
		for _, wanted := range s.goal.Instances {
			if schedulerSamePhysicalTarget(actual, wanted) && schedulerOwnerAllows(actual.OwnerWorkloadUID, wanted.WorkloadUID) {
				if previous := s.byID[actual.TargetID]; previous != nil && previous != s.byID[actual.ID] {
					s.out.reasons = appendUnique(s.out.reasons, "AmbiguousPreparedPhysicalTarget:"+actual.TargetID)
				} else {
					s.byID[actual.TargetID] = s.byID[actual.ID]
				}
			}
		}
	}
	for _, i := range s.goal.Instances {
		if s.byID[i.ID] == nil {
			addInstance(i, false)
			s.byID[i.ID].stopped = true
		}
	}
	sort.Slice(s.instances, func(i, j int) bool { return s.instances[i].config.ID < s.instances[j].config.ID })
}

func (s *schedulerReplayState) initializeActions() {
	s.actions = []*schedulerReplayAction{}
	s.actionByID = map[string]*schedulerReplayAction{}
	for _, a := range s.c.Actions {
		if a.Status == schedulerActionCancel {
			continue
		}
		x := &schedulerReplayAction{action: a, done: a.Status == schedulerActionComplete, finish: math.Inf(1)}
		s.actions = append(s.actions, x)
		s.actionByID[a.ID] = x
	}
	if s.c.Keep {
		for _, a := range s.snap.Actions {
			if a.Status == schedulerActionComplete || a.Status == schedulerActionCancel || s.actionByID[a.ID] != nil {
				continue
			}
			x := &schedulerReplayAction{action: a, finish: math.Inf(1)}
			s.actions = append(s.actions, x)
			s.actionByID[a.ID] = x
		}
	}
	for _, a := range s.snap.Actions {
		if a.Status == schedulerActionComplete && s.actionByID[a.ID] == nil {
			s.actionByID[a.ID] = &schedulerReplayAction{action: a, done: true}
		}
	}
}

func (s *schedulerReplayState) initializeOutstanding() {
	s.requests = []*schedulerReplayRequest{}
	outstanding := append([]SchedulerRequest(nil), s.snap.Outstanding...)
	sort.SliceStable(outstanding, func(i, j int) bool {
		a, b := outstanding[i], outstanding[j]
		ka, kb := a.Phase+"/"+a.Instance, b.Phase+"/"+b.Instance
		if a.Phase == phasePending {
			ka += "/" + a.Definition.UID
		}
		if b.Phase == phasePending {
			kb += "/" + b.Definition.UID
		}
		if ka != kb {
			return ka < kb
		}
		if a.QueueOrder != b.QueueOrder {
			return a.QueueOrder < b.QueueOrder
		}
		return a.ID < b.ID
	})
	for _, r := range outstanding {
		x := &schedulerReplayRequest{source: r, identity: r.Definition.Identity(), observed: true, arrival: 0, native: math.Inf(1), expires: math.Inf(1), queueAt: -float64(r.QueueAge), running: r.Phase == phaseRunning, deferred: r.Phase == phaseDeferred}
		if r.NativeSenderElapsedMS != nil {
			x.senderMS = *r.NativeSenderElapsedMS
		}
		if r.RemainingLifetime != nil {
			x.expires = float64(*r.RemainingLifetime)
		}
		s.requests = append(s.requests, x)
		s.out.definitions[x.identity] = r.Definition
		if r.NativeOutcome == firstfloor.OutcomeDeadlineDrop {
			x.dropped = true
			key := x.identity
			if s.out.outcomes[key] == nil {
				s.out.outcomes[key] = map[string]int{}
			}
			s.out.outcomes[key]["timeout"]++
		}
		if r.Phase == phaseAssigned || r.Phase == phaseQueued || r.Phase == phaseDeferred || r.Phase == phaseRunning || r.Phase == phaseReturning {
			x.dispatched = true
			i := s.byID[r.Instance]
			i.outstanding++
			switch r.Phase {
			case phaseAssigned:
				// In transit: it reaches the tail of the native queue at replay start.
				x.native = 0
				x.senderMS = float64(r.Elapsed) / 1e6
			case phaseQueued, phaseDeferred:
				i.queue = append(i.queue, x)
			case phaseRunning:
				i.batch = append(i.batch, x)
			case phaseReturning:
				x.returning = true
				// Native completion precedes the observed response age; the margin starts there.
				x.completion = math.Max(0, s.settings.CompletionMarginMS*1e6-float64(r.ReturnElapsed))
			}
		}
	}
}

func (s *schedulerReplayState) initializeArrivals(ctx context.Context) bool {
	window := float64(s.settings.ObservationWindow)
	start := s.snap.ObservationEnd.Add(-s.settings.ObservationWindow)
	arrivals := []SchedulerArrival{}
	for _, a := range s.snap.Arrivals {
		if ctx.Err() != nil {
			return false
		}
		if !a.At.Before(start) && a.At.Before(s.snap.ObservationEnd) && (a.Compute || a.Accepted) && a.Definition != nil {
			arrivals = append(arrivals, a)
		}
	}
	sort.SliceStable(arrivals, func(i, j int) bool {
		if arrivals[i].At.Equal(arrivals[j].At) {
			return arrivals[i].ID < arrivals[j].ID
		}
		return arrivals[i].At.Before(arrivals[j].At)
	})
	identities := map[*Definition]string{}
	for cycle := 0.0; len(arrivals) > 0 && cycle < s.horizon; cycle += window {
		for j, a := range arrivals {
			if ctx.Err() != nil {
				return false
			}
			at := cycle + float64(a.At.Sub(start))
			if at >= s.horizon {
				continue
			}
			r := SchedulerRequest{ID: fmt.Sprintf("replay-%g-%d", cycle, j), Definition: a.Definition, ReceivedAt: s.snap.At.Add(time.Duration(at)), Phase: phasePending}
			identity, ok := identities[a.Definition]
			if !ok {
				identity = a.Definition.Identity()
				identities[a.Definition] = identity
			}
			x := &schedulerReplayRequest{source: r, identity: identity, arrival: at, native: math.Inf(1), expires: math.Inf(1)}
			if a.Lifetime > 0 {
				x.expires = at + float64(a.Lifetime)
			}
			s.requests = append(s.requests, x)
			s.out.definitions[x.identity] = a.Definition
		}
	}
	return true
}

func (s *schedulerReplayState) targetHandles(r *schedulerReplayRequest) bool {
	for _, i := range s.goal.Instances {
		if i.WorkloadUID == r.source.Definition.UID && schedulerProfileMatches(r.source.Definition, i.Profile) {
			return true
		}
	}
	return false
}

func (s *schedulerReplayState) retainedIngress(i *schedulerReplayInstance) bool {
	if i == nil {
		return false
	}
	for _, r := range s.live {
		if r.observed && !r.expired && !r.dispatched && !r.completed && i.config.WorkloadUID == r.source.Definition.UID && schedulerProfileMatches(r.source.Definition, i.config.Profile) && !s.hasRoutedAlternative(r, i) {
			return true
		}
	}
	return false
}

func (s *schedulerReplayState) hasRoutedAlternative(r *schedulerReplayRequest, retiring *schedulerReplayInstance) bool {
	routing := s.snap.Routing
	if s.routingApplied {
		routing = s.applied.Routing
	}
	for _, other := range s.instances {
		if other == retiring || !other.loaded || !other.ready || other.stopped || other.config.WorkloadUID != r.source.Definition.UID || !schedulerProfileMatches(r.source.Definition, other.config.Profile) {
			continue
		}
		weight := other.config.Weight
		if weights, explicit := routing[r.source.Definition.UID]; explicit {
			weight = weights[other.config.ID]
		}
		if weight > 0 {
			return true
		}
	}
	return false
}

func (s *schedulerReplayState) initializeRunning() bool {
	for _, i := range s.instances {
		if len(i.batch) == 0 {
			continue
		}
		head := i.batch[0]
		for _, r := range i.batch {
			if r.source.BatchID != head.source.BatchID || r.identity != head.identity {
				s.out.reasons = append(s.out.reasons, "InconsistentRunningBatch:"+i.config.ID)
				return false
			}
		}
		b := schedulerBatch(i.config.Profile, len(i.batch))
		if b == nil {
			s.out.reasons = append(s.out.reasons, "RunningBatchOutsideProfile:"+i.config.ID)
			return false
		}
		residual := []float64{}
		elapsed := float64(head.source.RunningElapsed)
		for _, v := range b.DurationNanos {
			if v > elapsed {
				residual = append(residual, v-elapsed)
			}
		}
		if len(residual) == 0 {
			s.out.reasons = append(s.out.reasons, "RunningElapsedOutsideProfileRange:"+i.config.ID)
			return false
		}
		remaining := residual[s.rng.IntN(len(residual))]
		i.finish = remaining
		if s.counted(i.config.Node) {
			s.out.energy += b.EnergyPerInputJoules * float64(len(i.batch)) * math.Min(remaining, s.horizon) / (elapsed + remaining)
		}
		s.out.assumptions = appendUnique(s.out.assumptions, "running incremental joules spread uniformly over sampled conditioned service interval")
	}
	return true
}

// admitArrivals keeps s.live to the requests a step can touch, so a step costs
// the in-flight population rather than the whole window.
func (s *schedulerReplayState) admitArrivals() {
	live := s.live[:0]
	for _, r := range s.live {
		if !r.completed {
			live = append(live, r)
		}
	}
	clear(s.live[len(live):])
	for ; s.arrived < len(s.requests) && s.requests[s.arrived].arrival <= s.now; s.arrived++ {
		if r := s.requests[s.arrived]; !r.completed {
			live = append(live, r)
		}
	}
	s.live = live
}

func (s *schedulerReplayState) expireRequests() {
	for _, r := range s.live {
		if r.completed || r.expired || r.dropped || r.expires > s.now {
			continue
		}
		r.expired = true
		kind := "expired-pending"
		if r.dispatched {
			kind = "expired-queued"
		}
		if r.running {
			kind = "expired-running"
		} else if r.returning {
			kind = "expired-returning"
		}
		key := r.identity
		if s.out.outcomes[key] == nil {
			s.out.outcomes[key] = map[string]int{}
		}
		s.out.outcomes[key][kind]++
		if !r.running && !r.returning {
			r.returning = true
			r.completion = s.now
			if r.dispatched {
				i := s.byID[r.source.Instance]
				kept := i.queue[:0]
				for _, q := range i.queue {
					if q != r {
						kept = append(kept, q)
					}
				}
				i.queue = kept
				r.completion = s.now + s.settings.CompletionMarginMS*1e6
				if !math.IsInf(r.native, 1) {
					r.completion = math.Max(s.now, r.native) + s.settings.CompletionMarginMS*1e6
				}
				r.native = math.Inf(1)
			}
		}
	}
}

func (s *schedulerReplayState) completeExecution() {
	for _, i := range s.instances {
		if i.finish <= s.now {
			for _, r := range i.batch {
				r.running = false
				r.returning = true
				r.completion = s.now + s.settings.CompletionMarginMS*1e6
			}
			i.batch = nil
			i.finish = math.Inf(1)
			i.wake = s.now
		}
	}
	for _, r := range s.live {
		if r.returning && !r.completed && r.completion <= s.now {
			r.completed = true
			i := s.byID[r.source.Instance]
			if i != nil && r.dispatched {
				i.outstanding--
			}
			key := r.identity
			if !r.expired && !r.dropped {
				latency := float64(r.source.Elapsed) + r.completion - r.arrival
				s.out.samples[key] = append(s.out.samples[key], latency)
				if s.out.outcomes[key] == nil {
					s.out.outcomes[key] = map[string]int{}
				}
				s.out.outcomes[key]["completed"]++
			}
		}
	}
}

func (s *schedulerReplayState) completeActions() bool {
	for _, a := range s.actions {
		if a.started && !a.done && a.finish <= s.now {
			a.done = true
			switch a.action.Kind {
			case schedulerActionPrepare:
				i := s.byID[a.action.Instance]
				if i != nil {
					i.loaded = true
					i.ready = true
				}
			case schedulerActionRemove:
				if i := s.byID[a.action.Instance]; i != nil {
					i.loaded = false
					i.ready = false
				}
			case schedulerActionPower:
				for n := range s.nodes {
					if s.nodes[n].Node == a.action.Node {
						projected, known := s.nodes[n].AtMode(a.action.To)
						if !known {
							a.done = false
							s.out.reasons = appendUnique(s.out.reasons, NodePlacementInventoryMissing+":"+a.action.Node.UID)
							return false
						}
						s.nodes[n] = projected
					}
				}
			}
		}
	}
	return true
}

func (s *schedulerReplayState) dependenciesReady(a *schedulerReplayAction) bool {
	if a.action.Status != phaseRunning {
		for _, id := range a.action.DependsOn {
			dep := s.actionByID[id]
			if dep == nil || !dep.done {
				return false
			}
		}
	}
	return true
}

func (s *schedulerReplayState) applyRoute(target SchedulerCandidate) bool {
	s.previousRoute = s.snap.Routing
	if s.routingApplied {
		s.previousRoute = s.applied.Routing
	}
	s.applied = target
	invalidate := func(uid string) {
		if len(s.routeCredits[uid]) == len(s.instances) {
			return
		}
		for _, i := range s.instances {
			if s.snap.RoutingCredits[uid][i.config.ID] != 0 && s.retainedRouteCredit(uid, i.config, 1) == 0 {
				if s.creditInvalid == nil {
					s.creditInvalid = map[string]map[string]bool{}
				}
				if s.creditInvalid[uid] == nil {
					s.creditInvalid[uid] = map[string]bool{}
				}
				s.creditInvalid[uid][i.config.ID] = true
			}
		}
	}
	for uid := range target.Routing {
		invalidate(uid)
	}
	for uid := range s.previousRoute {
		if _, present := target.Routing[uid]; !present {
			invalidate(uid)
		}
	}
	for uid, credits := range s.routeCredits {
		for j, i := range s.instances {
			credits[j] = s.retainedRouteCredit(uid, i.config, credits[j])
		}
	}
	for _, i := range s.instances {
		for _, v := range target.Instances {
			weight := v.Weight
			if route, explicit := target.Routing[v.WorkloadUID]; explicit {
				weight = route[v.ID]
			}
			if weight <= 0 {
				continue
			}
			if s.byID[v.ID] == i {
				if !i.loaded || !i.ready {
					s.out.reasons = appendUnique(s.out.reasons, "NoReadyCapacityOrTransitionPrerequisite")
					return false
				}
				if !schedulerOwnerAllows(i.config.OwnerWorkloadUID, v.WorkloadUID) {
					s.out.reasons = appendUnique(s.out.reasons, "IncompatiblePreparedOwner:"+i.config.ID)
					return false
				}
				if i.config.Profile == nil || v.Profile == nil || i.config.Profile.ID != v.Profile.ID {
					s.out.reasons = append(s.out.reasons, "ImmutableNativeProfileMismatch:"+v.ID)
					return false
				}
				mode := ""
				if v.Profile.Condition.Firstfloor != nil {
					mode = v.Profile.Condition.Firstfloor.DeadlinePolicy
				}
				if i.policy.DeadlinePolicy != firstfloor.EffectiveDeadlinePolicy(mode) {
					s.out.reasons = append(s.out.reasons, "ImmutableNativeDeadlinePolicyMismatch:"+v.ID)
					return false
				}
				i.config = v
				i.policy.MaxBatchSize = v.MaxBatch
				i.policy.MaxBatchWaitMS = float64(v.MaxWait) / 1e6
				i.stopped = false
			}
		}
	}
	s.routingApplied = true
	return true
}

func (s *schedulerReplayState) startActions() bool {
	for _, a := range s.actions {
		if a.done || a.started {
			continue
		}
		if a.action.Status == schedulerActionFailed {
			s.out.reasons = append(s.out.reasons, "ActionFailed:"+a.action.ID+":"+a.action.Reason)
			return false
		}
		if !s.dependenciesReady(a) {
			continue
		}
		switch a.action.Kind {
		case schedulerActionRoute:
			if a.action.Status == phaseRunning {
				s.out.reasons = appendUnique(s.out.reasons, "InProgressRoutingAcknowledgementUnobserved:"+a.action.ID)
				return false
			}
			target := s.goal
			if a.action.RouteTarget != nil {
				target = *a.action.RouteTarget
			}
			if !s.applyRoute(target) {
				return false
			}
			a.done = true
		case schedulerActionStop:
			if i := s.byID[a.action.Instance]; i != nil {
				if s.retainedIngress(i) {
					continue
				}
				i.stopped = true
			}
			a.done = true
		case schedulerActionDrain:
			if i := s.byID[a.action.Instance]; i == nil || i.outstanding == 0 {
				a.done = true
			}
		default:
			if !s.startOperation(a) {
				return false
			}
		}
	}
	return true
}

func (s *schedulerReplayState) startOperation(a *schedulerReplayAction) bool {
	if a.action.Kind != schedulerActionPrepare && a.action.Kind != schedulerActionRemove && a.action.Kind != schedulerActionPower {
		s.out.reasons = append(s.out.reasons, "UnsupportedAction:"+a.action.Kind)
		return false
	}
	if a.action.Kind == schedulerActionPrepare && s.byID[a.action.Instance] == nil {
		s.out.reasons = append(s.out.reasons, "MissingActionInstanceState:"+a.action.ID)
		return false
	}
	if a.action.Kind == schedulerActionPrepare {
		target := s.byID[a.action.Instance]
		if target.config.Node != a.action.Node || schedulerState(target.config) != a.action.To {
			s.out.reasons = appendUnique(s.out.reasons, "PreparationTargetStateMismatch:"+a.action.ID)
			return false
		}
		active := []SchedulerInstance{}
		for _, i := range s.instances {
			if i.loaded || i == s.byID[a.action.Instance] {
				active = append(active, i.config)
			}
		}
		if reasons := schedulerValidateResources(s.settings, s.nodes, active); len(reasons) > 0 {
			s.out.reasons = append(s.out.reasons, "TemporaryReadinessResourceInfeasible")
			s.out.reasons = append(s.out.reasons, reasons...)
			return false
		}
	}
	cost := s.operationCost(a)
	if cost == nil {
		return false
	}
	if !s.sampleOperation(a, cost) || !s.adjustOperationBackground(a, cost) {
		return false
	}
	duration := float64(a.trial.Duration) - float64(a.action.Elapsed)
	a.started = true
	a.start = s.now
	a.finish = s.now + duration
	s.out.sources = appendUnique(s.out.sources, cost.ID)
	s.out.assumptions = appendUnique(s.out.assumptions, "operation trial wall joules replace baseline; paired trial mean wall power for continuation; every planned action completes within the horizon")
	return true
}

func (s *schedulerReplayState) operationCost(a *schedulerReplayAction) *OperationCost {
	cost := schedulerCost(s.snap.Costs, a.action.CostID)
	if cost == nil || cost.Meter == "" || cost.Scope != schedulerNodeWallScope || len(cost.Trials) == 0 {
		s.out.reasons = append(s.out.reasons, "MissingOperationCost:"+a.action.ID)
		return nil
	}
	if cost.Reference.Validate() != nil {
		s.out.reasons = append(s.out.reasons, "MissingOperationRawSource:"+a.action.ID)
		return nil
	}
	if cost.Node != a.action.Node || cost.Kind != a.action.Kind || schedulerCostState(cost.From) != schedulerCostState(a.action.From) ||
		schedulerCostState(cost.To) != schedulerCostState(a.action.To) {
		s.out.reasons = append(s.out.reasons, "OperationConditionMismatch:"+a.action.ID)
		return nil
	}
	if a.action.Kind == schedulerActionPower {
		state := []string{}
		for _, i := range s.instances {
			if i.loaded && i.config.Node == a.action.Node {
				state = append(state, schedulerState(i.config))
			}
		}
		if !schedulerEqualState(state, cost.BeforeState) || !schedulerEqualState(state, cost.TargetState) {
			s.out.reasons = append(s.out.reasons, "PowerLoadedWitnessMismatch:"+a.action.ID)
			return nil
		}
	} else if cost.PowerMode == "" || cost.PowerMode != schedulerNodeMode(s.nodes, a.action.Node) {
		s.out.reasons = append(s.out.reasons, "OperationHostModeMismatch:"+a.action.ID)
		return nil
	}
	return cost
}

func (s *schedulerReplayState) sampleOperation(a *schedulerReplayAction, cost *OperationCost) bool {
	trialOptions := []OperationTrial{}
	for _, t := range cost.Trials {
		if t.Duration <= 0 || !schedulerFinite(t.Joules) || !schedulerValidHalfWidth(t.EnergyUncertaintyJoules) || (cost.EnergyScope == "whole-wall" && t.Joules < 0) {
			continue
		}
		if a.action.Status == phaseRunning && t.Duration <= a.action.Elapsed {
			continue
		}
		trialOptions = append(trialOptions, t)
	}
	if len(trialOptions) == 0 {
		s.out.reasons = append(s.out.reasons, "OperationElapsedOutsideProfileRange:"+a.action.ID)
		return false
	}
	a.trial = trialOptions[s.rng.IntN(len(trialOptions))]
	duration := float64(a.trial.Duration)
	a.wallRate = a.trial.Joules / duration
	if cost.EnergyScope == "target-baseline-delta" {
		if cost.SubtractedWatts == nil || !schedulerFinite(*cost.SubtractedWatts) {
			s.out.reasons = append(s.out.reasons, "UnknownEtaSubtraction:"+a.action.ID)
			return false
		}
		a.wallRate += *cost.SubtractedWatts / 1e9
	} else if cost.EnergyScope != "whole-wall" {
		s.out.reasons = append(s.out.reasons, "UnknownOperationEnergyScope:"+a.action.ID)
		return false
	}
	return true
}

func (s *schedulerReplayState) adjustOperationBackground(a *schedulerReplayAction, cost *OperationCost) bool {
	state := []string{}
	active := []SchedulerInstance{}
	var node SchedulerNode
	for _, n := range s.nodes {
		if n.Node == a.action.Node {
			node = n
		}
	}
	for _, i := range s.instances {
		if i.loaded && i.config.Node == a.action.Node && ((a.action.Kind != schedulerActionPrepare && a.action.Kind != schedulerActionRemove) || i != s.byID[a.action.Instance]) {
			state = append(state, schedulerState(i.config))
			active = append(active, i.config)
		}
	}
	measured := append([]string(nil), cost.BeforeState...)
	removedFocal := ""
	if a.action.Kind == schedulerActionPrepare || a.action.Kind == schedulerActionRemove {
		focal := a.action.To
		if a.action.Kind == schedulerActionRemove {
			focal = a.action.From
		}
		for j, s := range measured {
			if schedulerCostState(s) == schedulerCostState(focal) {
				removedFocal = s
				measured = append(measured[:j], measured[j+1:]...)
				break
			}
		}
	}
	if !schedulerEqualState(state, measured) {
		if !s.adjustOperationBaseline(a, cost, node, active, removedFocal) {
			return false
		}
	}
	if a.wallRate < 0 {
		s.out.reasons = append(s.out.reasons, "InvalidOperationWallEnergy:"+a.action.ID)
		return false
	}
	return true
}

func (s *schedulerReplayState) adjustOperationBaseline(a *schedulerReplayAction, cost *OperationCost, node SchedulerNode, active []SchedulerInstance, removedFocal string) bool {
	if cost.BeforeBaselineWatts == nil || !schedulerFinite(*cost.BeforeBaselineWatts) || *cost.BeforeBaselineWatts < 0 {
		s.out.reasons = append(s.out.reasons, "MissingOperationBackgroundBaseline:"+a.action.ID)
		return false
	}
	before := *cost.BeforeBaselineWatts
	if removedFocal != "" {
		focal, ok := s.byID[a.action.Instance]
		if !ok || schedulerState(focal.config) != removedFocal {
			s.out.reasons = append(s.out.reasons, "MissingMeasuredOperationFocalState:"+a.action.ID)
			return false
		}
		loaded, meter, ss, as, reason := schedulerBaseline(s.snap.Baselines, node, []SchedulerInstance{focal.config}, s.snap.Options)
		unloaded, um, us, ua, ur := schedulerBaseline(s.snap.Baselines, node, nil, s.snap.Options)
		if reason != "" || ur != "" || meter != cost.Meter || um != meter {
			s.out.reasons = append(s.out.reasons, "OperationFocalBaselineScopeMismatch:"+a.action.ID)
			return false
		}
		before -= loaded - unloaded
		s.out.sources = appendUnique(s.out.sources, append(ss, us...)...)
		s.out.assumptions = appendUnique(s.out.assumptions, append(as, ua...)...)
	}
	watts, meter, sources, assumptions, reason := schedulerBaseline(s.snap.Baselines, node, active, s.snap.Options)
	if before < 0 || reason != "" || meter != cost.Meter {
		s.out.reasons = append(s.out.reasons, "OperationBackgroundScopeMismatch:"+a.action.ID+":"+reason)
		return false
	}
	a.wallRate += (watts - before) / 1e9
	s.out.sources = appendUnique(s.out.sources, sources...)
	s.out.assumptions = appendUnique(s.out.assumptions, assumptions...)
	s.out.assumptions = appendUnique(s.out.assumptions, "operation whole-wall adjusted by coexist background idle difference only; focal prepare/removal energy retained in measured trial, background interaction unmeasured")
	return true
}

// Runtime retains a route entry's credit only when its physical ID and weight
// are unchanged. Apply the same rule to existing and lazily seeded replay state.
func (s *schedulerReplayState) retainedRouteCredit(uid string, i SchedulerInstance, credit int64) int64 {
	before, after := i.Weight, i.Weight
	if route, ok := s.previousRoute[uid]; ok {
		before = route[i.ID]
	}
	if route, ok := s.applied.Routing[uid]; ok {
		after = route[i.ID]
	}
	if before != after {
		return 0
	}
	return credit
}

func (s *schedulerReplayState) routeRequests() {
	for _, r := range s.live {
		if r.completed || r.expired || r.dispatched || r.arrival > s.now {
			continue
		}
		uid := r.source.Definition.UID
		credits := s.routeCredits[uid]
		if len(credits) != len(s.instances) {
			credits = make([]int64, len(s.instances))
			for j, i := range s.instances {
				credits[j] = s.snap.RoutingCredits[uid][i.config.ID]
				if s.creditInvalid[uid][i.config.ID] {
					credits[j] = 0
				} else if s.routingApplied {
					credits[j] = s.retainedRouteCredit(uid, i.config, credits[j])
				}
			}
			s.routeCredits[uid] = credits
		}
		for j, i := range s.instances {
			s.weights[j] = i.config.Weight
			routes := s.snap.Routing
			if s.routingApplied {
				routes = s.applied.Routing
			}
			if route, ok := routes[r.source.Definition.UID]; ok {
				s.weights[j] = route[i.config.ID]
			}
			if r.observed && !s.targetHandles(r) {
				s.weights[j] = i.config.Weight
			}
			s.available[j] = i.loaded && i.ready && !i.stopped && i.outstanding < i.config.Capacity && i.config.WorkloadUID == uid && schedulerProfileMatches(r.source.Definition, i.config.Profile)
		}
		selected := SmoothWeightedRoute(s.weights, credits, s.available)
		if selected < 0 {
			continue
		}
		i := s.instances[selected]
		r.dispatched = true
		r.source.Instance = i.config.ID
		r.senderMS = math.Max(0, float64(r.source.Elapsed)+s.now-r.arrival) / 1e6
		r.native = s.now
		i.outstanding++
	}
}

func (s *schedulerReplayState) receiveNative() {
	for _, r := range s.live {
		if r.dispatched && !r.returning && !r.completed && r.native <= s.now {
			r.native = math.Inf(1)
			r.queueAt = s.now
			i := s.byID[r.source.Instance]
			i.queue = append(i.queue, r)
			uid := r.source.Definition.UID
			if _, known := i.arrivalTimes[uid]; !known && len(i.arrivalTimes) >= i.config.Capacity {
				oldest := ""
				at := math.Inf(1)
				for key, h := range i.arrivalTimes {
					if len(h) > 0 && (h[0] < at || h[0] == at && key < oldest) {
						oldest = key
						at = h[0]
					}
				}
				delete(i.arrivalTimes, oldest)
			}
			history := i.arrivalTimes[uid]
			if cap(history) < 2 {
				h := make([]float64, len(history), 2)
				copy(h, history)
				history = h
			}
			if len(history) == 0 {
				history = append(history, s.now)
			} else {
				if len(history) == 1 {
					history = append(history, history[0])
				} else {
					history[1] = history[0]
				}
				history[0] = s.now
			}
			i.arrivalTimes[uid] = history
			i.wake = s.now
		}
	}
}

func (s *schedulerReplayState) nativeRequestState(i *schedulerReplayInstance, r *schedulerReplayRequest) firstfloor.RequestState {
	d := r.source.Definition
	rate := r.source.ArrivalRate
	if history := i.arrivalTimes[d.UID]; len(history) == 2 {
		gap := history[0] - history[1]
		if gap > 0 {
			rate = 1e9 / gap
		} else {
			rate = 0
		}
	}
	phase := phaseQueued
	if r.running {
		phase = phaseRunning
	} else if r.deferred {
		phase = phaseDeferred
	}
	return firstfloor.RequestState{Context: firstfloor.RequestContext{RequestID: r.source.ID, WorkloadUID: d.UID, Definition: r.identity, ReceivedAt: r.source.ReceivedAt, SenderElapsedMS: r.senderMS, CompletionMarginMS: s.settings.CompletionMarginMS, SLO: firstfloor.SLO{Percentile: d.SLO.Percentile, TargetMS: float64(d.SLO.TargetLatency) / 1e6}}, Phase: phase, ElapsedMS: (s.now - r.queueAt) / 1e6, QueueMS: (s.now - r.queueAt) / 1e6, ArrivalRate: rate}
}

func (s *schedulerReplayState) chooseBatches(ctx context.Context) bool {
	for _, i := range s.instances {
		if !i.loaded || !i.ready || len(i.queue) == 0 {
			continue
		}
		entries, states := s.entries[:0], s.states[:0]
		for len(i.queue) > 0 {
			if ctx.Err() != nil {
				return false
			}
			// EDF keys are fixed at s.now: compute each once, then sort.
			entries = entries[:0]
			for _, r := range i.queue {
				entries = append(entries, schedulerReplayQueued{r: r, slo: firstfloor.RemainingSLOMS(s.nativeRequestState(i, r))})
			}
			// Match the actor's stable EDF normal queue and contiguous deferred FIFO.
			slices.SortStableFunc(entries, compareReplayQueued)
			states = states[:0]
			for j, e := range entries {
				i.queue[j] = e.r
				states = append(states, s.nativeRequestState(i, e.r))
			}
			for _, r := range i.batch {
				states = append(states, s.nativeRequestState(i, r))
			}
			s.entries, s.states = entries, states
			choice := firstfloor.ChooseBatch(i.policy, states)
			if choice.Reason == firstfloor.OutcomeDeadlineDrop || choice.Reason == firstfloor.ReasonDeadlineDefer {
				index := -1
				for j, r := range i.queue {
					if r.source.ID == choice.RequestID {
						index = j
						break
					}
				}
				if index < 0 {
					s.out.reasons = append(s.out.reasons, "BatchPolicyUnknownTransitionRequest:"+i.config.ID)
					return false
				}
				r := i.queue[index]
				copy(i.queue[index:], i.queue[index+1:])
				i.queue[len(i.queue)-1] = nil
				i.queue = i.queue[:len(i.queue)-1]
				if choice.Reason == firstfloor.ReasonDeadlineDefer {
					r.deferred = true
					i.queue = append(i.queue, r)
				} else {
					r.dropped = true
					r.completed = true
					i.outstanding--
					key := r.identity
					if s.out.outcomes[key] == nil {
						s.out.outcomes[key] = map[string]int{}
					}
					s.out.outcomes[key]["timeout"]++
					if s.capacity != nil {
						s.capacity.failed = true // Any timeout already fails a capacity window.
						return false
					}
				}
				i.wake = s.now
				continue
			}
			if len(i.batch) > 0 || i.wake > s.now {
				break
			}
			if choice.WaitMS > 0 {
				i.wake = s.now + choice.WaitMS*1e6
				break
			}
			if choice.Size <= 0 {
				s.out.reasons = append(s.out.reasons, "BatchPolicyNoExecutableChoice:"+i.config.ID)
				return false
			}
			deferred := i.queue[0].deferred
			rest := i.queue[:0]
			for _, r := range i.queue {
				if len(i.batch) < choice.Size && r.deferred == deferred && r.source.Definition.UID == choice.WorkloadUID && r.identity == choice.Definition {
					i.batch = append(i.batch, r)
				} else {
					rest = append(rest, r)
				}
			}
			i.queue = rest
			for _, r := range i.batch {
				r.running = true
			}
			b := schedulerBatch(i.config.Profile, len(i.batch))
			if b == nil {
				s.out.reasons = append(s.out.reasons, "BatchPolicyOutsideProfile:"+i.config.ID)
				return false
			}
			duration := b.DurationNanos[s.rng.IntN(len(b.DurationNanos))]
			// The batch completes, and the instance takes its next batch, after
			// the service sample plus the occupancy of each request.
			i.finish = s.now + duration + i.occupancy*float64(len(i.batch))
			s.addBatchEnergy(i, b, duration)
			break
		}
	}
	return true
}

// addBatchEnergy attributes a started batch's incremental joules within the
// horizon on a compared Node.
func (s *schedulerReplayState) addBatchEnergy(i *schedulerReplayInstance, b *BatchProfile, duration float64) {
	if s.now < s.horizon && s.counted(i.config.Node) {
		s.out.energy += b.EnergyPerInputJoules * float64(len(i.batch)) * math.Min(duration, s.horizon-s.now) / duration
	}
}

func (s *schedulerReplayState) nextEvent() (float64, bool) {
	next := math.Inf(1)
	// Unarrived requests expire after they arrive, so the earliest arrival bounds them all.
	remaining := s.arrived < len(s.requests)
	if remaining {
		next = s.requests[s.arrived].arrival
	}
	for _, r := range s.live {
		if r.completed {
			continue
		}
		remaining = true
		if !r.expired && r.expires > s.now {
			next = math.Min(next, r.expires)
		}
		if r.native > s.now {
			next = math.Min(next, r.native)
		}
		if r.returning && r.completion > s.now {
			next = math.Min(next, r.completion)
		}
	}
	for _, i := range s.instances {
		next = math.Min(next, i.finish)
		if i.policy.DeadlinePolicy == firstfloor.DeadlineDrop {
			for _, r := range i.queue {
				remaining := firstfloor.RemainingSLOMS(s.nativeRequestState(i, r))
				if remaining > 0 {
					next = math.Min(next, s.now+remaining*1e6)
				}
			}
		}
		if len(i.queue) > 0 && len(i.batch) == 0 && i.wake > s.now {
			next = math.Min(next, i.wake)
		}
	}
	for _, a := range s.actions {
		if a.started && !a.done {
			next = math.Min(next, a.finish)
		}
	}
	return next, remaining
}

func (s *schedulerReplayState) unlockedActions() bool {
	unlocked := false
	for _, a := range s.actions {
		if a.started || a.done {
			continue
		}
		ok := s.dependenciesReady(a)
		if a.action.Kind == schedulerActionStop && s.retainedIngress(s.byID[a.action.Instance]) {
			ok = false
		}
		if ok && (a.action.Kind != schedulerActionDrain || s.byID[a.action.Instance] == nil || s.byID[a.action.Instance].outstanding == 0) {
			unlocked = true
		}
	}
	return unlocked
}

func (s *schedulerReplayState) accumulateEnergy(until float64) bool {
	if until > s.now {
		meterSeen := map[string]string{}
		for _, n := range s.nodes {
			if !s.counted(n.Node) {
				continue
			}
			active := []SchedulerInstance{}
			for _, i := range s.instances {
				if i.loaded && i.config.Node == n.Node {
					active = append(active, i.config)
				}
			}
			watts, meter, sources, assumptions, reason := schedulerBaseline(s.snap.Baselines, n, active, s.snap.Options)
			if reason != "" {
				s.out.reasons = append(s.out.reasons, reason)
				return false
			}
			if owner, ok := meterSeen[meter]; ok && owner != n.Node.UID {
				s.out.reasons = append(s.out.reasons, "NonDistinctNodeMeter:"+meter)
				return false
			}
			meterSeen[meter] = n.Node.UID
			s.out.sources = appendUnique(s.out.sources, sources...)
			s.out.assumptions = appendUnique(s.out.assumptions, assumptions...)
			rate := watts / 1e9
			count := 0
			for _, a := range s.actions {
				if a.started && !a.done && a.action.Node == n.Node {
					cost := schedulerCost(s.snap.Costs, a.action.CostID)
					if cost.Meter != meter {
						s.out.reasons = append(s.out.reasons, "OperationMeterMismatch:"+a.action.ID)
						return false
					}
					rate = a.wallRate
					count++
				}
			}
			if count > 1 {
				s.out.reasons = append(s.out.reasons, "OverlappingNodeWallOperations:"+n.Node.UID)
				return false
			}
			s.out.energy += rate * (until - s.now)
		}
	}
	return true
}
func schedulerBatch(p *Profile, size int) *BatchProfile {
	if p != nil {
		for j := range p.Batches {
			if p.Batches[j].Batch == size {
				return &p.Batches[j]
			}
		}
	}
	return nil
}
func schedulerCost(costs []OperationCost, id string) *OperationCost {
	for j := range costs {
		if costs[j].ID == id {
			return &costs[j]
		}
	}
	return nil
}
func schedulerValidHalfWidth(value float64) bool {
	return schedulerFinite(value) && value >= 0
}

func schedulerBaseline(baselines []NodeBaseline, node SchedulerNode, instances []SchedulerInstance, options []SchedulerOption) (float64, string, []string, []string, string) {
	state := []string{}
	for _, i := range instances {
		state = append(state, schedulerState(i))
	}
	sort.Strings(state)
	var matched, unloaded *NodeBaseline
	for j := range baselines {
		b := &baselines[j]
		if b.Node != node.Node || b.PowerMode != node.PowerMode || b.Scope != schedulerNodeWallScope || b.Meter == "" || !schedulerFinite(b.Watts) || b.Watts < 0 || !schedulerValidHalfWidth(b.WattsUncertainty) || b.ID == "" || b.Reference.Validate() != nil {
			continue
		}
		bs := append([]string(nil), b.State...)
		sort.Strings(bs)
		if fmt.Sprint(bs) == fmt.Sprint(state) && (matched == nil || b.MeasuredAt.After(matched.MeasuredAt)) {
			matched = b
		}
		if len(bs) == 0 && (unloaded == nil || b.MeasuredAt.After(unloaded.MeasuredAt)) {
			unloaded = b
		}
	}
	if matched != nil {
		return matched.Watts, matched.Meter, []string{matched.ID}, nil, ""
	}
	if unloaded == nil {
		return 0, "", nil, nil, "MissingUnloadedNodeBaseline:" + node.Node.UID + ":" + node.PowerMode
	}
	watts := unloaded.Watts
	sources := []string{unloaded.ID}
	for _, i := range instances {
		effective, ok := schedulerIdleAtMode(i, node.PowerMode, options)
		if !ok {
			return 0, "", nil, nil, "MissingRetainedRunnerTargetPowerProfile:" + i.ID
		}
		p := effective.Profile
		if p == nil || !schedulerFinite(p.IdleBaselineWatts) || p.IdleBaselineWatts < 0 || len(p.Evidence) == 0 {
			return 0, "", nil, nil, "MissingExactIdleSubtraction:" + i.ID
		}
		if effective.IdleMeter != unloaded.Meter || effective.IdleScope != schedulerNodeWallScope {
			return 0, "", nil, nil, "IdleMeterScopeMismatch:" + i.ID
		}
		watts += p.IdleBaselineWatts - unloaded.Watts
		sources = appendUnique(sources, p.ID)
	}
	if !schedulerFinite(watts) || watts < 0 {
		return 0, "", nil, nil, "DerivedNodeBaselineOutsidePhysicalRange:" + node.Node.UID
	}
	return watts, unloaded.Meter, sources, []string{"derived unloaded node baseline + (profile estimator's exact loaded idle - unloaded) once per physical instance; coexistence interference unmeasured; retained immutable bundles at a changed host mode use only matching measured mode/resource/model idle evidence"}, ""
}

// schedulerEqualState compares loaded-state multisets under schedulerCostState.
func schedulerEqualState(a, b []string) bool {
	norm := func(states []string) []string {
		out := make([]string, len(states))
		for k, s := range states {
			out[k] = schedulerCostState(s)
		}
		sort.Strings(out)
		return out
	}
	return reflect.DeepEqual(norm(a), norm(b))
}
func schedulerIdleAtMode(i SchedulerInstance, mode string, options []SchedulerOption) (SchedulerInstance, bool) {
	if i.Profile == nil {
		return i, false
	}
	if i.Profile.Condition.PowerMode == mode {
		return i, true
	}
	p := i.Profile.Condition
	for _, o := range options {
		j := o.Instance
		if j.Profile == nil || j.Node != i.Node || !reflect.DeepEqual(i.Allocation, j.Allocation) {
			continue
		}
		q := j.Profile.Condition
		if q.PowerMode == mode && p.RuntimeFamily == q.RuntimeFamily && p.Backend == q.Backend && p.RuntimeImage == q.RuntimeImage && reflect.DeepEqual(p.Artifact, q.Artifact) && reflect.DeepEqual(p.Preprocessor, q.Preprocessor) && reflect.DeepEqual(p.ActiveProtocol.Dataset, q.ActiveProtocol.Dataset) && reflect.DeepEqual(p.CPU, q.CPU) && reflect.DeepEqual(p.Firstfloor, q.Firstfloor) && reflect.DeepEqual(p.RuntimeAccess, q.RuntimeAccess) && reflect.DeepEqual(p.PlannedBatchSizes, q.PlannedBatchSizes) {
			return j, true
		}
	}
	return i, false
}
