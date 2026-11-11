package serving

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

const (
	schedulerStatusPending     = "Pending"
	schedulerStatusFeasible    = "Feasible"
	SchedulerStatusOverload    = "Overload"
	schedulerNodeWallScope     = "node-exclusive-wall"
	schedulerActionPrepare     = "prepare"
	schedulerActionRemove      = "remove"
	schedulerActionPower       = "power"
	schedulerActionRoute       = "route"
	schedulerActionStop        = "stop"
	schedulerActionDrain       = "drain"
	schedulerActionPlanned     = "planned"
	schedulerActionComplete    = "complete"
	schedulerActionFailed      = "failed"
	schedulerActionPartial     = "partial"
	schedulerActionCancel      = "cancel"
	schedulerOutcomeTimeout    = "timeout"
	schedulerOutcomeOnTime     = "onTime"
	schedulerOutcomeLate       = "late"
	schedulerOutcomeRejected   = "rejected"
	schedulerOutcomeFailed     = "failed"
	schedulerOutcomeUnfinished = "unfinished"
	nativeOutcomeQueueFull     = "queue_full"
)

type schedulerReplayRequest struct {
	source              SchedulerRequest
	identity            string // source.Definition.Identity(), formatted once
	arrival             float64
	native              float64
	queueAt             float64
	senderMS            float64
	completed           bool
	dispatched          bool
	returning           bool
	completion          float64
	observed            bool
	admitted            bool
	running             bool
	expired             bool
	deferred            bool
	gatewayDeferred     bool
	dropped             bool
	failed              bool
	expires             float64
	capacityMiss        bool    // counted once against the capacity window's fixed population
	preNanos            float64 // synthetic PRE, never added to accepted requests
	marginNanos         float64 // frozen POST
	remainingLifetimeMS float64
	nativeAgeNanos      float64 // captured native age; advances only after native receipt
	accountedAgeNanos   float64 // S+N for native requests, actual server age otherwise
	potential           float64 // conditional failure excess, never a completion time
	hasPotential        bool
	minimumDemand       bool
	admissionTarget     bool
}

type schedulerReplayInstance struct {
	config               SchedulerInstance
	loaded               bool
	ready                bool
	stopped              bool
	excluded             bool // retains physical occupancy and outstanding, never executable supply
	queue                []*schedulerReplayRequest
	batch                []*schedulerReplayRequest
	finish               float64
	nativeFinish         float64 // service ends before retained dispatch occupancy/path return
	lastNativeFinish     float64
	hasNativeFinish      bool
	gapNanos             int64 // reusable query storage; no per-dispatch allocation
	wake                 float64
	outstanding          int
	policy               firstfloor.Policy
	arrivalTimes         map[string][]float64
	arrivalsKnown        bool
	occupancyNanos       float64 // selected measured, DOM or configured extra ns/request
	batchStarted         float64
	batchElapsed         int64
	batchIdle            int64
	hasBatchIdle         bool
	exchangeKnown        bool
	exchangeElapsed      int64
	admissionObserved    []firstfloor.RequestState
	admissionAssignments []firstfloor.RequestState
	admissionCalls       []*schedulerReplayRequest
	admissionArrivals    map[string]admissionArrival
	admissionUnknown     bool
	admissionNormal      int
}

type schedulerReplayResult struct {
	energy           float64
	requestJoules    float64
	samples          map[string][]float64
	definitions      map[string]*Definition
	reasons          []string
	assumptions      []string
	sources          []string
	outcomes         map[string]map[string]int
	conditional      ConditionalServiceEvidence
	feedback         []FeedbackWorkloadScore
	observedArrivals map[string]int
}

// ConditionalServiceEvidence describes the support actually queried by replay.
// It is evidence of sparsity/extrapolation, not a conditional-tail safety gate.
type ConditionalServiceEvidence struct {
	Queries               int   `json:"queries"`
	StartupQueries        int   `json:"startupQueries"`
	MaximumDistanceNanos  int64 `json:"maximumDistanceNanos"`
	MinimumTrialCount     int   `json:"minimumTrialCount"`
	OutsideSupportQueries int   `json:"outsideSupportQueries"`

	RunningResidualBeyondSupport []RunningResidualBeyondSupport `json:"runningResidualBeyondSupport,omitempty"`
}

// RunningResidualBeyondSupport records an explicit restart substitute, not a bound.
type RunningResidualBeyondSupport struct {
	InstanceID      string `json:"instanceID"`
	BatchSize       int    `json:"batchSize"`
	ElapsedNanos    int64  `json:"elapsedNanos"`
	ProfileMaxNanos int64  `json:"profileMaxNanos"`
	Model           string `json:"model"`
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
	e.RunningResidualBeyondSupport = append(e.RunningResidualBeyondSupport, other.RunningResidualBeyondSupport...)
}

func (i *schedulerReplayInstance) idleNanos(now float64) *int64 {
	if !i.hasNativeFinish {
		return nil
	}
	i.gapNanos = int64(math.Max(0, now-i.lastNativeFinish))
	return &i.gapNanos
}

// schedulerReplayEstimate replays candidate c once with seed and records each
// definition's SLO-percentile estimate and predicted outcomes in c. Energy
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
	c.SLO, c.Feedback = feedbackReplayEstimates(snapshot, r, capacity == nil), r.feedback
	c.WorstExceedance = 0
	for _, estimate := range c.SLO {
		c.WorstExceedance = max(c.WorstExceedance, estimate.NormalizedExceedance)
		if estimate.NormalizedExceedance > 0 {
			c.Status = SchedulerStatusOverload
		}
		if estimate.ExpectedNanos == nil {
			for _, count := range estimate.PredictedOutcomes {
				if count > 0 {
					c.Status = SchedulerStatusOverload
					break
				}
			}
		}
	}
	return c, ctx.Err()
}

type schedulerReplayState struct {
	settings          SchedulerSettings
	snap              SchedulerSnapshot
	c                 SchedulerCandidate
	rng               *rand.Rand
	out               schedulerReplayResult
	horizon           float64
	instances         []*schedulerReplayInstance
	byID              map[string]*schedulerReplayInstance
	nodes             []SchedulerNode
	goal              SchedulerCandidate
	applied           SchedulerCandidate
	previousRoute     map[string]map[string]int32
	creditInvalid     map[string]map[string]bool
	actions           []*schedulerReplayAction
	actionByID        map[string]*schedulerReplayAction
	requests          []*schedulerReplayRequest // ordered by arrival
	live              []*schedulerReplayRequest // arrived and not completed, in requests order
	arrived           int                       // requests[:arrived] have arrived
	routeCredits      map[string][]int64        // Workload UID
	routingApplied    bool
	weights           []int32
	available         []bool
	queuedCounts      map[string]int
	dispatchUIDs      []string
	admissionPrepared bool
	admissionReady    bool
	admissionBlocked  bool
	admissionPending  map[string][]*schedulerReplayRequest
	admissionTargets  map[*Definition]bool
	now               float64
	// compared limits energy to these Nodes; nil counts every Node.
	compared map[NodeReference]bool
	// chooseBatches scratch, reused across events.
	entries            []schedulerReplayQueued
	states             []firstfloor.RequestState
	admissionScratch   admissionScratch
	capacity           *capacityWindowLimit // nil for ordinary candidate estimates
	finalizingFeedback bool
	potentialQueues    map[*schedulerReplayInstance]*feedbackQueueSummary // non-nil only during immutable final reduction
	potentialProfiles  map[feedbackProfileKey]bool                        // immutable Definition/Profile compatibility, never readiness or queue state
}

func (s *schedulerReplayState) counted(node NodeReference) bool {
	return s.compared == nil || s.compared[node]
}

// schedulerReplay replays candidate c over the horizon; energy counts the
// compared Nodes only (nil counts every Node).
func schedulerReplay(ctx context.Context, settings SchedulerSettings, snap SchedulerSnapshot, c SchedulerCandidate, rng *rand.Rand, compared map[NodeReference]bool, capacity *capacityWindowLimit) (result schedulerReplayResult, err error) {
	return schedulerReplayWithArrivals(ctx, settings, snap, c, rng, compared, capacity, nil)
}

func schedulerReplayWithArrivals(ctx context.Context, settings SchedulerSettings, snap SchedulerSnapshot, c SchedulerCandidate, rng *rand.Rand, compared map[NodeReference]bool, capacity *capacityWindowLimit, cohort *feedbackArrivalCohort) (result schedulerReplayResult, err error) {
	if err := ctx.Err(); err != nil {
		return schedulerReplayResult{}, err
	}
	snap = schedulerReplayScopedSnapshot(snap)
	s := schedulerReplayState{settings: settings, snap: snap, c: c, rng: rng, horizon: float64(settings.ComparisonPeriod), compared: compared, capacity: capacity, out: schedulerReplayResult{samples: map[string][]float64{}, definitions: map[string]*Definition{}, outcomes: map[string]map[string]int{}}}
	defer func() {
		if s.capacity == nil && err == nil {
			s.finishFeedback()
		} else if s.capacity != nil && !s.capacity.failed && err == nil {
			s.finishCapacityWindow()
		}
		result = s.out
	}()
	s.initializeInstances()
	s.initializeActions()
	s.initializeOutstanding()
	arrivalsReady := false
	if cohort != nil && capacity == nil {
		arrivalsReady = s.initializeArrivalCohort(ctx, cohort)
	} else {
		arrivalsReady = s.initializeArrivals(ctx)
	}
	if !arrivalsReady || len(s.out.reasons) != 0 || !s.initializeRunning() {
		return s.out, ctx.Err()
	}
	s.routeCredits = map[string][]int64{}
	s.routingApplied = c.Keep
	s.weights = make([]int32, len(s.instances))
	s.available = make([]bool, len(s.instances))
	err = s.run(ctx)
	return s.out, err
}

func (s *schedulerReplayState) run(ctx context.Context) error {
	for steps := 1; ; steps++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if steps > (len(s.requests)+1)*30+len(s.actions)*30+1000 {
			s.out.reasons = append(s.out.reasons, "ReplayEventBoundExceeded")
			return ctx.Err()
		}
		// Complete physical execution before choosing another UID's nonpreemptive batch.
		s.expireRequests()
		s.completeExecution()
		if s.capacityWindowFailed() {
			return nil
		}
		if !s.completeActions() {
			return ctx.Err()
		}
		if !s.startActions() {
			return ctx.Err()
		}
		if !s.admitArrivals(ctx) {
			return ctx.Err()
		}
		// Native queue entries never move when the desired configuration changes.
		if !s.routeRequests(ctx) {
			return ctx.Err()
		}
		if s.capacityWindowFailed() {
			return nil
		}
		s.receiveNative()
		if !s.chooseBatches(ctx) {
			return ctx.Err()
		}
		next, remaining := s.nextEvent()
		// Zero-duration route/drain can unlock an action at the same timestamp.
		if s.unlockedActions() {
			continue
		}
		if math.IsInf(next, 1) {
			if remaining && s.blockedReplayPrerequisite() {
				s.out.reasons = append(s.out.reasons, "NoReadyCapacityOrTransitionPrerequisite")
				return ctx.Err()
			}
			remaining = false // unassigned requests remain explicit unfinished outcomes
			next = math.Max(s.now, s.horizon)
		}
		if !s.accumulateEnergy(math.Min(next, s.horizon)) {
			return ctx.Err()
		}
		if !remaining && next >= s.horizon {
			break
		}
		if next <= s.now {
			s.out.reasons = append(s.out.reasons, "ReplayNonAdvancingEvent")
			return ctx.Err()
		}
		s.now = next
	}
	if !schedulerFinite(s.out.energy) || s.out.energy < 0 {
		s.out.reasons = append(s.out.reasons, "ExpectedEnergyOutsidePhysicalNumericalRange")
	}
	return ctx.Err()
}

func (s *schedulerReplayState) blockedReplayPrerequisite() bool {
	// Failed effects and their dependents cannot progress or satisfy dependencies.
	for changed := true; changed; {
		changed = false
		for _, action := range s.actions {
			if action.done || action.started || action.blocked || action.action.Status == phaseRunning {
				continue
			}
			blocked := action.action.Status == schedulerActionFailed
			for _, id := range action.action.DependsOn {
				if dep := s.actionByID[id]; dep != nil && dep.blocked {
					blocked = true
					break
				}
			}
			if blocked {
				action.blocked = true
				changed = true
			}
		}
	}
	for _, action := range s.actions {
		if !action.done && !action.blocked {
			return true
		}
	}
	for _, request := range s.live {
		if !request.completed && request.dispatched && (s.byID[request.source.Instance] == nil || !s.byID[request.source.Instance].excluded) {
			return true
		}
	}
	return false
}

func (s *schedulerReplayState) initializeInstances() {
	s.instances = []*schedulerReplayInstance{}
	s.byID = map[string]*schedulerReplayInstance{}
	s.nodes = append([]SchedulerNode(nil), s.snap.Nodes...)
	s.goal = s.c
	s.applied, s.previousRoute = s.c, s.snap.Routing
	scope := schedulerReplayInitialScope(s.snap)
	s.out.assumptions = appendUnique(s.out.assumptions, scope.evidence...)
	addInstance := func(i SchedulerInstance, loaded bool) {
		x := &schedulerReplayInstance{config: i, loaded: loaded, ready: loaded && i.Ready, stopped: i.Stopped, finish: math.Inf(1), wake: 0, arrivalTimes: map[string][]float64{}}
		x.excluded = scope.instances[i.ID] != ""
		x.arrivalsKnown = !loaded || i.ArrivalAgesMS != nil
		if loaded {
			for uid, ages := range i.ArrivalAgesMS {
				history := make([]float64, 0, min(2, len(ages)))
				for _, age := range ages[:min(2, len(ages))] {
					history = append(history, -age*1e6-float64(max(s.snap.At.Sub(i.ObservedAt), 0)))
				}
				x.arrivalTimes[uid] = history
			}
		}
		if loaded && i.LastServiceEndAgeNanos != nil {
			age := *i.LastServiceEndAgeNanos + max(int64(0), int64(s.snap.At.Sub(i.ObservedAt)))
			x.lastNativeFinish, x.hasNativeFinish = -float64(age), true
		}
		if i.Profile != nil {
			x.policy = schedulerPolicy(i)
		}
		if o := ResolveDispatchOccupancy(s.settings, s.snap.DispatchOccupancy, i); o != nil {
			x.occupancyNanos = float64(o.PerRequest)
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
		if previous := s.byID[actual.TargetID]; previous != nil && previous != s.byID[actual.ID] {
			s.out.reasons = appendUnique(s.out.reasons, "AmbiguousPreparedPhysicalTarget:"+actual.TargetID)
		} else {
			s.byID[actual.TargetID] = s.byID[actual.ID]
		}
	}
	for _, i := range schedulerReplayTargets(s.snap) {
		if s.byID[i.ID] == nil {
			addInstance(i, false)
			s.byID[i.ID].stopped = true
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
	for _, a := range s.snap.Actions {
		if a.Status == schedulerActionComplete || a.Status == schedulerActionCancel || s.actionByID[a.ID] != nil {
			continue
		}
		x := &schedulerReplayAction{action: a, finish: math.Inf(1)}
		s.actions = append(s.actions, x)
		s.actionByID[a.ID] = x
	}
	for _, a := range s.c.Actions {
		if a.Status == schedulerActionCancel || s.actionByID[a.ID] != nil {
			continue
		}
		x := &schedulerReplayAction{action: a, done: a.Status == schedulerActionComplete, finish: math.Inf(1)}
		s.actions = append(s.actions, x)
		s.actionByID[a.ID] = x
	}
	for _, a := range s.snap.Actions {
		if a.Status == schedulerActionComplete && s.actionByID[a.ID] == nil {
			s.actionByID[a.ID] = &schedulerReplayAction{action: a, done: true}
		}
	}
	scope := schedulerReplayInitialScope(s.snap)
	for _, a := range s.actions {
		a.blocked = scope.actions[a.action.ID] != ""
		if a.action.Kind == schedulerActionPrepare || a.action.Kind == schedulerActionRemove || a.action.Kind == schedulerActionPower {
			a.blocked = a.blocked || scope.nodes[a.action.Node]
		}
		if instance := s.byID[a.action.Instance]; instance != nil && instance.excluded {
			a.blocked = true
		}
	}
	s.blockedReplayPrerequisite()
}

func (s *schedulerReplayState) initializeOutstanding() {
	s.requests = []*schedulerReplayRequest{}
	clear(s.live)
	s.live, s.arrived = s.live[:0], 0
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
		x := s.outstandingRequest(r)
		s.requests = append(s.requests, x)
		s.live = append(s.live, x)
		s.out.definitions[x.identity] = r.Definition
		if r.NativeOutcome == firstfloor.OutcomeDeadlineDrop || r.NativeOutcome == nativeOutcomeQueueFull {
			x.dropped = true
			key := x.identity
			if s.out.outcomes[key] == nil {
				s.out.outcomes[key] = map[string]int{}
			}
			s.out.outcomes[key][schedulerOutcomeRejected]++
		} else if r.NativeOutcome == firstfloor.OutcomePreprocessFailed || r.NativeOutcome == firstfloor.OutcomeBatchError {
			x.failed = true
			if s.out.outcomes[x.identity] == nil {
				s.out.outcomes[x.identity] = map[string]int{}
			}
			s.out.outcomes[x.identity][schedulerOutcomeFailed]++
		}
		if r.Phase == phaseAssigned || r.Phase == phaseQueued || r.Phase == phaseDeferred && r.Instance != "" || r.Phase == phaseRunning || r.Phase == phaseReturning {
			s.restoreNativeOutstanding(x)
		}
	}
}

func (s *schedulerReplayState) outstandingRequest(r SchedulerRequest) *schedulerReplayRequest {
	x := &schedulerReplayRequest{source: r, identity: r.Definition.Identity(), observed: true, admitted: true, native: math.Inf(1), expires: math.Inf(1), queueAt: -float64(r.QueueAge), running: r.Phase == phaseRunning, deferred: r.Phase == phaseDeferred && r.Instance != "", gatewayDeferred: r.Phase == phaseDeferred && r.Instance == ""}
	x.marginNanos = s.settings.CompletionMarginMS * 1e6
	x.accountedAgeNanos = float64(r.Elapsed)
	if r.CompletionMarginMS != nil {
		x.marginNanos = *r.CompletionMarginMS * 1e6
	} else {
		s.out.sources = appendUnique(s.out.sources, "configured-initial-post")
		s.out.assumptions = appendUnique(s.out.assumptions, "accepted/historical request has no captured POST; configured CompletionMarginMS is an initial model assumption, not frozen evidence")
	}
	if r.NativeElapsedNanos != nil {
		x.nativeAgeNanos = *r.NativeElapsedNanos
		if r.NativeSenderElapsedMS != nil {
			x.accountedAgeNanos = *r.NativeSenderElapsedMS*1e6 + x.nativeAgeNanos
		}
	} else {
		x.nativeAgeNanos = float64(r.QueueAge)
		if r.NativeSenderElapsedMS != nil {
			x.nativeAgeNanos = max(0, float64(r.Elapsed)-*r.NativeSenderElapsedMS*1e6)
		}
	}
	if r.NativeSenderElapsedMS != nil {
		x.senderMS = *r.NativeSenderElapsedMS
	}
	if r.RemainingLifetime != nil {
		x.expires = float64(*r.RemainingLifetime)
	}
	return x
}

func (s *schedulerReplayState) restoreNativeOutstanding(x *schedulerReplayRequest) {
	r := x.source
	x.dispatched = true
	i := s.byID[r.Instance]
	if i == nil {
		s.out.reasons = appendUnique(s.out.reasons, "MissingOutstandingInstance:"+r.Instance)
		return
	}
	i.outstanding++
	switch r.Phase {
	case phaseAssigned:
		// In transit: it reaches the tail of the native queue at replay start.
		x.native = 0
		if r.NativeSenderElapsedMS == nil {
			x.senderMS = float64(r.Elapsed) / 1e6
		} else {
			x.accountedAgeNanos = x.senderMS * 1e6
		}
	case phaseQueued, phaseDeferred:
		i.queue = append(i.queue, x)
	case phaseRunning:
		i.batch = append(i.batch, x)
	case phaseReturning:
		x.returning = true
		if r.NativeElapsedNanos != nil && r.NativeSenderElapsedMS != nil {
			// POST is measured from S+N, including native return and marshalling.
			consumed := max(0, float64(r.Elapsed)-x.accountedAgeNanos)
			x.completion = max(0, x.marginNanos-consumed)
			x.accountedAgeNanos += x.marginNanos - x.completion
		} else {
			// Historical requests without captured S/N retain the explicit model.
			x.completion = max(0, x.marginNanos-float64(r.ReturnElapsed))
		}
	}
}

func (s *schedulerReplayState) initializeArrivals(ctx context.Context) bool {
	window := float64(s.settings.ObservationWindow)
	if window <= 0 {
		s.out.reasons = appendUnique(s.out.reasons, "InvalidReplayObservationWindow")
		return false
	}
	start := s.snap.ObservationEnd.Add(-s.settings.ObservationWindow)
	arrivals := []SchedulerArrival{}
	s.out.observedArrivals = map[string]int{}
	for _, a := range s.snap.Arrivals {
		if ctx.Err() != nil {
			return false
		}
		if !a.At.Before(start) && a.At.Before(s.snap.ObservationEnd) && (a.Compute || a.Accepted) && a.Definition != nil {
			arrivals = append(arrivals, a)
			s.out.observedArrivals[a.Definition.UID]++
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
			x := s.feedbackArrivalRequest(r, identity, at)
			if a.Lifetime > 0 {
				x.expires = at + float64(a.Lifetime)
			}
			s.requests = append(s.requests, x)
			s.out.definitions[x.identity] = a.Definition
		}
	}
	return s.addMinimumDemand(ctx)
}

func (s *schedulerReplayState) initializeRunning() bool {
	for _, i := range s.instances {
		if i.excluded || len(i.batch) == 0 {
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
		i.batchElapsed, i.exchangeKnown = elapsed, head.source.ExchangeElapsedNanos != nil
		if head.source.PrecedingIdleNanos != nil {
			i.batchIdle, i.hasBatchIdle = *head.source.PrecedingIdleNanos, true
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
		remaining := float64(sample.ServiceDurationNanos - elapsed)
		if !ok && elapsed >= 0 {
			sample, support, ok = b.ServiceDistribution.Sample(gap, s.rng)
			if ok {
				remaining = float64(sample.ServiceDurationNanos)
				profileMax := int64(0)
				for _, row := range b.ServiceSamples {
					profileMax = max(profileMax, row.ServiceDurationNanos)
				}
				s.out.conditional.RunningResidualBeyondSupport = append(s.out.conditional.RunningResidualBeyondSupport,
					RunningResidualBeyondSupport{
						InstanceID: i.config.ID, BatchSize: len(i.batch), ElapsedNanos: elapsed,
						ProfileMaxNanos: profileMax, Model: "same-gap-service-restart",
					})
				s.out.assumptions = appendUnique(s.out.assumptions,
					"RunningResidualBeyondSupport: remaining service uses same-gap-service-restart substitute; SLO and energy ranking bias is bidirectional, not a bound")
			}
		}
		s.out.conditional.record(gap, support)
		if !ok {
			s.out.reasons = append(s.out.reasons, "RunningElapsedOutsideProfileRange:"+i.config.ID)
			return false
		}
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
		for _, r := range i.queue {
			if r.source.Definition.Admission.Policy != AdmissionPolicyReject || r.deferred || i.policy.FixedBatchSize > 0 {
				continue
			}
			remaining := firstfloor.RemainingSLOMS(s.nativeRequestState(i, r))
			if remaining > 0 {
				next = math.Min(next, s.now+math.Max(1, math.Ceil(remaining*1e6)))
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
