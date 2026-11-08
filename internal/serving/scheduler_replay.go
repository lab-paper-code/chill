package serving

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
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
	postMS       float64 // frozen native external margin
	returnMS     float64 // TOTAL synthetic charge, POST for observed outstanding
	senderReserveMS float64 // modeled allocation only, not an observed pre quantile
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
	config           SchedulerInstance
	loaded           bool
	ready            bool
	stopped          bool
	queue            []*schedulerReplayRequest
	batch            []*schedulerReplayRequest
	finish           float64
	nativeFinish     float64 // service ends before retained dispatch occupancy/path return
	lastNativeFinish float64
	hasNativeFinish  bool
	gapNanos         int64 // reusable query storage; no per-dispatch allocation
	wake             float64
	outstanding      int
	policy           firstfloor.Policy
	arrivalTimes     map[string][]float64
	occupancy        float64 // selected measured, DOM or configured extra ns/request
}

type schedulerReplayResult struct {
	energy        float64
	requestJoules float64
	samples       map[string][]float64
	definitions   map[string]*Definition
	reasons       []string
	assumptions   []string
	sources       []string
	outcomes      map[string]map[string]int
	conditional   ConditionalServiceEvidence
}

// ConditionalServiceEvidence describes the support actually queried by replay.
// It is evidence of sparsity/extrapolation, not a conditional-tail safety gate.
type ConditionalServiceEvidence struct {
	Queries               int   `json:"queries"`
	StartupQueries        int   `json:"startupQueries"`
	MaximumDistanceNanos  int64 `json:"maximumDistanceNanos"`
	MinimumTrialCount     int   `json:"minimumTrialCount"`
	OutsideSupportQueries int   `json:"outsideSupportQueries"`
}

func (e *ConditionalServiceEvidence) record(gap *int64, support firstfloor.ConditionalSupport) {
	if e.Queries == 0 {
		e.MinimumTrialCount = support.TrialCount
	} else {
		e.MinimumTrialCount = min(e.MinimumTrialCount, support.TrialCount)
	}
	e.Queries++
	if gap == nil {
		e.StartupQueries++
	}
	e.MaximumDistanceNanos = max(e.MaximumDistanceNanos, support.DistanceNanos)
	if support.OutsideSupport {
		e.OutsideSupportQueries++
	}
}

func (e *ConditionalServiceEvidence) merge(other ConditionalServiceEvidence) {
	if other.Queries == 0 {
		return
	}
	if e.Queries == 0 {
		e.MinimumTrialCount = other.MinimumTrialCount
	} else {
		e.MinimumTrialCount = min(e.MinimumTrialCount, other.MinimumTrialCount)
	}
	e.Queries += other.Queries
	e.StartupQueries += other.StartupQueries
	e.MaximumDistanceNanos = max(e.MaximumDistanceNanos, other.MaximumDistanceNanos)
	e.OutsideSupportQueries += other.OutsideSupportQueries
}

func (i *schedulerReplayInstance) idleNanos(now float64) *int64 {
	if !i.hasNativeFinish {
		return nil
	}
	i.gapNanos = int64(math.Max(0, now-i.lastNativeFinish))
	return &i.gapNanos
}

// schedulerReplayEstimate replays candidate c once with seed and records each
// definition's judging-percentile estimate and predicted outcomes in c. Energy
// counts the compared Nodes only (nil counts every Node). Reasons name the
// snapshot, resource or replay facts that block the replay.
func schedulerReplayEstimate(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, c SchedulerCandidate, seed uint64, compared map[NodeReference]bool, capacity *capacityWindowLimit) (SchedulerCandidate, error) {
	c.Status = schedulerStatusPending
	if capacity == nil {
		c.Reasons = append(c.Reasons, snapshot.Reasons...)
		c.Reasons = append(c.Reasons, schedulerValidateSnapshot(snapshot)...)
		c.Reasons = append(c.Reasons, schedulerValidateResources(settings, c.Nodes, c.Instances)...)
	} else {
		// Capacity windows contain measured service and simulated arrivals, not
		// a current Node snapshot. Placement admission belongs to the planner.
		for _, instance := range c.Instances {
			c.Reasons, _ = schedulerValidateService(instance, c.Reasons)
		}
	}
	if len(c.Reasons) != 0 {
		return c, ctx.Err()
	}
	r, err := schedulerReplay(ctx, settings, snapshot, c, rand.New(rand.NewPCG(seed, 0)), compared, capacity)
	if err != nil {
		return c, err
	}
	c.ConditionalService = &r.conditional
	if capacity != nil {
		capacity.requestJoules = r.requestJoules
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
		budget := ResolvePathBudget(settings, snapshot.PathBudgets, d)
		e := SchedulerEstimate{UID: d.UID, Definition: key, Samples: len(samples), Percentile: budget.JudgmentPercentile, TargetNanos: int64(d.SLO.TargetLatency), PredictedOutcomes: r.outcomes[key]}
		if len(samples) > 0 {
			sort.Float64s(samples)
			q := stat.Quantile(budget.JudgmentPercentile/100, stat.Empirical, samples, nil)
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
		if loaded && i.LastServiceEndAgeNanos != nil {
			age := *i.LastServiceEndAgeNanos + max(int64(0), int64(s.snap.At.Sub(i.ObservedAt)))
			x.lastNativeFinish, x.hasNativeFinish = -float64(age), true
		}
		if i.Profile != nil {
			x.policy = schedulerPolicy(i)
		}
		if o := ResolveDispatchOccupancy(s.settings, s.snap.DispatchOccupancy, i); o != nil {
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
		// Pending and not-yet-frozen assigned calls still owe pre-F work.
		// QueueAge is server slot wait here, so subtract only consumed path.
		x.postMS = s.settings.CompletionMarginMS
		if r.CompletionMarginMS != nil {
			x.postMS = *r.CompletionMarginMS
		}
		x.returnMS = x.postMS
		if (r.Phase == phasePending || r.Phase == phaseAssigned) && r.NativeSenderElapsedMS == nil && r.TotalMarginMS != nil {
			consumedMS := max(0, float64(r.Elapsed-r.QueueAge)/1e6)
			x.senderReserveMS = max(0, *r.TotalMarginMS-x.postMS-consumedMS)
			x.returnMS += x.senderReserveMS
		}
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
				if r.NativeSenderElapsedMS == nil {
					x.senderMS = float64(r.Elapsed)/1e6 + x.senderReserveMS
				}
			case phaseQueued, phaseDeferred:
				i.queue = append(i.queue, x)
			case phaseRunning:
				i.batch = append(i.batch, x)
			case phaseReturning:
				x.returning = true
				// Native completion precedes the observed response age; the margin starts there.
				x.completion = math.Max(0, x.returnMS*1e6-float64(r.ReturnElapsed))
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
	budgets := map[*Definition]PathBudget{}
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
				budgets[a.Definition] = ResolvePathBudget(s.settings, s.snap.PathBudgets, a.Definition)
				if budgets[a.Definition].Source == "configured" {
					s.out.assumptions = appendUnique(s.out.assumptions, "configured completion margin may reserve pre-F cost again in native sender plus margin")
				} else {
					s.out.assumptions = appendUnique(s.out.assumptions, "synthetic sender reserve max(TOTAL-POST,0) allocates modeled TOTAL; it is not an observed pre-F quantile")
				}
			}
			x := &schedulerReplayRequest{source: r, identity: identity, arrival: at, native: math.Inf(1), expires: math.Inf(1)}
			budget := budgets[a.Definition]
			x.postMS = budget.PostMS
			x.senderReserveMS, x.returnMS = replayPathMargins(budget)
			if a.Lifetime > 0 {
				x.expires = at + float64(a.Lifetime)
			}
			s.requests = append(s.requests, x)
			s.out.definitions[x.identity] = a.Definition
		}
	}
	return true
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
		elapsed := int64(0)
		if head.source.ExchangeElapsedNanos != nil {
			elapsed = *head.source.ExchangeElapsedNanos
		}
		gap := head.source.PrecedingIdleNanos
		if head.source.ExchangeElapsedNanos == nil {
			// Actor-running begins before packing/IPC. No native S has elapsed;
			// current idle age is a lower-bound decision approximation of G.
			gap = i.idleNanos(s.now)
			if gap != nil {
				s.out.assumptions = appendUnique(s.out.assumptions,
					"pending native exchange uses current idle-age lower-bound approximation before IPC")
			}
		}
		sample, support, ok := b.ServiceDistribution.SampleResidual(gap, elapsed, s.rng)
		s.out.conditional.record(gap, support)
		if !ok {
			s.out.reasons = append(s.out.reasons, "RunningElapsedOutsideProfileRange:"+i.config.ID)
			return false
		}
		remaining := float64(sample.ServiceDurationNanos - elapsed)
		i.nativeFinish, i.finish = remaining, remaining
		joules := b.EnergyPerInputJoules * float64(len(i.batch)) * math.Min(remaining, s.horizon) / (float64(elapsed) + remaining)
		s.out.requestJoules += joules
		if s.counted(i.config.Node) {
			s.out.energy += joules
		}
		s.out.assumptions = appendUnique(s.out.assumptions, "running incremental joules spread uniformly over sampled conditioned service interval")
	}
	return true
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
			watts, meter, sources, assumptions := schedulerBaseline(s.settings, s.snap.Baselines, n, active, s.snap.Options)
			if owner, ok := meterSeen[meter]; ok && owner != n.Node.UID {
				s.out.reasons = append(s.out.reasons, "NonDistinctNodeMeter:"+meter)
				return false
			}
			meterSeen[meter] = n.Node.UID
			s.out.sources = appendUnique(s.out.sources, sources.References...)
			s.out.assumptions = appendUnique(s.out.assumptions, assumptions...)
			rate := watts / 1e9
			count := 0
			for _, a := range s.actions {
				if a.started && !a.done && a.action.Node == n.Node {
					cost := schedulerCost(s.snap.Costs, a.action.CostID)
					if a.action.CostProvenance != nil && a.action.CostProvenance.Source == operationSourceExact && cost.Meter != meter {
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
