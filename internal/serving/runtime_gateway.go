package serving

import (
	"errors"
	"maps"
	"math"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

// A call retains its definition and SWRR distribution. Physical owners are
// shared with the current snapshot so retirement closes old reservation gates.
type gatewaySnapshot struct {
	version           string
	appliedAt         time.Time
	definitions       map[string]*Definition
	routes            map[string]inputRoute
	targets           map[string]GatewayTarget
	versions          map[string]bool
	observationWindow time.Duration
}

type gatewayArrivalKey struct {
	at  time.Time
	uid string
}

type gatewayArrivalWindow struct {
	GatewayArrivals
	complete bool
}

// SetGatewayConnected selects split routing and closes admission until both a
// complete plan and a synchronized channel exist. Disconnect never cancels work.
func (r *Runtime) SetGatewayConnected(connected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gatewayMode = true
	now := time.Now().UTC()
	r.advanceGatewayArrivalsLocked(now)
	if !connected {
		r.gatewayArrivalCursor = time.Time{}
	} else if !r.gatewayConnected {
		r.gatewayArrivalCursor = now.Truncate(time.Second).Add(time.Second)
	}
	r.gatewayConnected = connected
	r.dispatchLocked()
	r.signal()
}

// SetGatewayFreshUntil publishes channel freshness independently of the owner
// lock, so stalled processing cannot hide an expired channel from readiness.
// A zero deadline is unbounded for runtimes without a channel owner.
func (r *Runtime) SetGatewayFreshUntil(deadline time.Time) {
	if deadline.IsZero() {
		r.gatewayFreshUntil.Store(0)
		return
	}
	r.gatewayFreshUntil.Store(deadline.UnixNano())
}

func (r *Runtime) gatewayFresh() bool {
	deadline := r.gatewayFreshUntil.Load()
	return deadline == 0 || time.Now().UnixNano() < deadline
}

func (r *Runtime) gatewayReadyLocked() bool {
	return r.gatewayFresh() && r.gatewayConnected && r.gateway != nil && !r.shuttingDown && r.observationError == nil
}

func (r *Runtime) GatewayReady() bool {
	if !r.gatewayFresh() {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gatewayReadyLocked()
}

// AdvanceGatewayObservation makes the owned observation prefix explicit before
// a full report or readiness confirmation. Filesystem work never owns Runtime.mu.
func (r *Runtime) AdvanceGatewayObservation() error {
	if err := r.config.ObservationLog.AdvanceRouterObservation(); err != nil {
		r.SetGatewayConnected(false)
		return err
	}
	return nil
}

// ApplyGatewayPlan validates the entire snapshot before changing any serving
// state. The final swap and first reservation use the same owner lock. Repeated
// identities only confirm the original application, never replace its body.
func (r *Runtime) ApplyGatewayPlan(plan GatewayPlan, targets []ReadyInstance) error {
	r.mu.Lock()
	if r.gateway != nil && r.gateway.version == plan.PlanVersion {
		r.mu.Unlock()
		return nil
	}
	if r.gateway != nil && r.gateway.versions[plan.PlanVersion] {
		r.mu.Unlock()
		return failure("StaleGatewayPlan", "a previously replaced plan cannot be applied again")
	}
	r.mu.Unlock()
	snapshot, ready, routing, profiles, err := validateGatewayPlan(plan, targets)
	if err != nil {
		return err
	}
	r.mu.Lock()
	err = r.validateGatewaySwapLocked(snapshot, ready, routing)
	r.mu.Unlock()
	if err != nil {
		return err
	}
	// Catalog I/O is outside the reservation lock. A concurrent acceptance is
	// checked again before installation; failed applies never change routing.
	for _, d := range snapshot.definitions {
		if err := r.config.ObservationLog.DefinitionRecord(d); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gateway != nil && r.gateway.version == plan.PlanVersion {
		return nil
	}
	if r.gateway != nil && r.gateway.versions[plan.PlanVersion] {
		return failure("StaleGatewayPlan", "a previously replaced plan cannot be applied again")
	}
	if err := r.validateGatewaySwapLocked(snapshot, ready, routing); err != nil {
		return err
	}
	for _, instance := range r.instances {
		instance.State.Ready = false
		instance.State.Reason = "GatewayRetired"
		instance.Routing = nil
	}
	for id, candidate := range ready {
		instance := r.instances[id]
		if instance == nil {
			instance = &runtimeInstance{bindings: map[string]bool{}}
			r.instances[id] = instance
		}
		instance.ReadyInstance = candidate
		instance.podRouting = maps.Clone(candidate.Routing)
	}
	now := time.Now().UTC()
	r.advanceGatewayArrivalsLocked(now)
	sameWorkloads := r.gateway != nil && len(r.gateway.definitions) == len(snapshot.definitions)
	if sameWorkloads {
		for uid := range snapshot.definitions {
			if r.gateway.definitions[uid] == nil {
				sameWorkloads = false
				break
			}
		}
	}
	if r.gatewayConnected && !sameWorkloads {
		r.gatewayArrivalCursor = now.Truncate(time.Second).Add(time.Second)
	}
	clear(r.names)
	for uid, workload := range r.workloads {
		if snapshot.definitions[uid] == nil {
			workload.stopped, workload.detached, workload.reason = true, true, "GatewayRetired"
		}
	}
	for uid, d := range snapshot.definitions {
		workload := r.workloads[uid]
		if workload == nil {
			workload = &runtimeWorkload{calls: map[string]*Call{}}
			r.workloads[uid] = workload
		}
		workload.definition, workload.identity = d, d.Identity()
		workload.stopped, workload.detached, workload.reason = false, false, ""
		workload.stopped = !plan.Admission[uid]
		if workload.stopped {
			workload.reason = "GatewayAdmissionStopped"
		}
		r.names[d.Name], r.definitions[d.Identity()] = uid, d
	}
	r.profiles, r.routeTargets, r.routingControlled = profiles, routing, true
	r.gatewayMode = true
	r.rebuildLocked()
	for uid := range snapshot.definitions {
		snapshot.routes[uid] = r.workloads[uid].route
	}
	if r.gateway != nil {
		snapshot.versions = r.gateway.versions
	} else {
		snapshot.versions = map[string]bool{}
	}
	snapshot.versions[snapshot.version] = true
	snapshot.appliedAt = now
	r.gateway = snapshot
	r.dispatchLocked()
	r.signal()
	return nil
}

// ObserveGatewayInstance applies exact-source native state without installing a
// partial route. In particular, it reuses the native absence proof for an
// uncertain transport that never produced a progress record.
func (r *Runtime) ObserveGatewayInstance(state InstanceState) (err error) {
	r.mu.Lock()
	defer func() { err = errors.Join(err, r.unlock()) }()
	instance := r.instances[state.ID]
	if !r.gatewayMode || instance == nil || state.PodUID == "" || state.ContainerID == "" || state.ID != state.PodUID+"/"+state.ContainerID ||
		instance.State.PodUID != state.PodUID || instance.State.ContainerID != state.ContainerID || instance.State.Node != state.Node || instance.State.Profile != state.Profile {
		return failure("BackendContextMismatch", "native state does not match a retained physical source")
	}
	if state.Stopped {
		return r.confirmInstanceStoppedLocked(state.ID)
	}
	if state.Native == nil || state.Native.Metadata.ProfileID != state.Profile || state.ObservedAt.IsZero() || state.Native.At.IsZero() {
		return failure("BackendContextMismatch", "native state has no matching physical observation")
	}
	if state.ObservedAt.Before(instance.State.ObservedAt) {
		return nil
	}
	instance.State.Native, instance.State.ObservedAt = state.Native, state.ObservedAt
	if !state.Ready || !state.Native.Ready {
		instance.State.Ready = false
		instance.State.Reason = "NativeNotReady"
		r.rebuildLocked()
	}
	r.finishAbsentLocked(state)
	r.dispatchLocked()
	r.signal()
	return nil
}

func validateGatewayPlan(plan GatewayPlan, candidates []ReadyInstance) (*gatewaySnapshot, map[string]ReadyInstance, map[string]map[string]int32, []Profile, error) {
	invalid := func() (*gatewaySnapshot, map[string]ReadyInstance, map[string]map[string]int32, []Profile, error) {
		return nil, nil, nil, nil, failure("InvalidGatewayPlan", "complete, unique definitions, physical targets, routing and conservative capacity are required")
	}
	if strings.TrimSpace(plan.PlanVersion) == "" || plan.CreatedAt.IsZero() || plan.Participants < 1 || plan.ObservationWindow <= 0 || plan.ObservationWindow > time.Duration(1<<63-1)-10*time.Second || len(candidates) != len(plan.Targets) {
		return invalid()
	}
	snapshot := &gatewaySnapshot{version: plan.PlanVersion, definitions: make(map[string]*Definition, len(plan.Definitions)), routes: make(map[string]inputRoute, len(plan.Definitions)), targets: make(map[string]GatewayTarget, len(plan.Targets))}
	snapshot.observationWindow = plan.ObservationWindow
	names := map[string]bool{}
	for _, d := range plan.Definitions {
		if !validGatewayDefinition(d) || snapshot.definitions[d.UID] != nil || names[d.Name] {
			return invalid()
		}
		snapshot.definitions[d.UID], names[d.Name] = d, true
	}
	if len(plan.Admission) != len(snapshot.definitions) {
		return invalid()
	}
	for uid := range plan.Admission {
		if snapshot.definitions[uid] == nil {
			return invalid()
		}
	}
	ready := make(map[string]ReadyInstance, len(candidates))
	for _, candidate := range candidates {
		if candidate.State.ID == "" || ready[candidate.State.ID].Execute != nil || candidate.Execute == nil {
			return invalid()
		}
		ready[candidate.State.ID] = candidate
	}
	profiles := make([]Profile, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		endpoint, err := url.Parse(target.Endpoint)
		p := target.Profile
		if target.PodUID == "" || strings.Contains(target.PodUID, "/") || target.ContainerID == "" || target.InstanceID != target.PodUID+"/"+target.ContainerID || snapshot.targets[target.InstanceID].InstanceID != "" ||
			target.Node.Name == "" || target.Node.UID == "" || err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || target.ModelName == "" || target.DecisionID == "" || target.ExpectedRuntime == nil ||
			p == nil || p.ID == "" || p.NodeName != target.Node.Name || p.Condition.NodeUID != target.Node.UID || p.Condition.Firstfloor == nil || target.Capacity < 1 || target.Capacity != int(p.Condition.Firstfloor.MaxQueueSize) || target.RouterLimit != target.Capacity/plan.Participants ||
			(target.OwnerWorkloadUID != "" && snapshot.definitions[target.OwnerWorkloadUID] == nil) {
			return invalid()
		}
		candidate, found := ready[target.InstanceID]
		if !found || !candidate.State.Ready || candidate.State.Stopped || candidate.State.PodUID != target.PodUID || candidate.State.ContainerID != target.ContainerID || candidate.State.Node != target.Node || candidate.State.Profile != p.ID || candidate.Profile == nil || candidate.Profile.ID != p.ID || candidate.OwnerWorkloadUID != target.OwnerWorkloadUID || candidate.Capacity != target.RouterLimit {
			return invalid()
		}
		if candidate.State.Native != nil && candidate.State.Native.Metadata.DecisionID != target.DecisionID {
			return invalid()
		}
		candidate.Profile, candidate.Routing = p, map[string]int32{}
		ready[target.InstanceID] = candidate
		snapshot.targets[target.InstanceID] = target
		profiles = append(profiles, *p)
	}
	if len(plan.Routing) != len(snapshot.definitions) {
		return invalid()
	}
	routing := make(map[string]map[string]int32, len(plan.Routing))
	for uid, distribution := range plan.Routing {
		d := snapshot.definitions[uid]
		if d == nil {
			return invalid()
		}
		routing[uid] = maps.Clone(distribution)
		for id, weight := range distribution {
			candidate, found := ready[id]
			if weight < 0 || !found || profilePath(d, candidate.Profile) == nil || (candidate.OwnerWorkloadUID != "" && candidate.OwnerWorkloadUID != uid) {
				return invalid()
			}
			candidate.Routing[uid] = weight
		}
	}
	return snapshot, ready, routing, profiles, nil
}

func validGatewayDefinition(d *Definition) bool {
	if d == nil || d.Name == "" || d.UID == "" || d.Generation < 1 || d.Model.Name == "" || d.Model.UID == "" || d.Model.Generation < 1 || d.Preprocessor.Name == "" || d.Preprocessor.UID == "" || d.Preprocessor.Generation < 1 ||
		len(d.Model.Paths) == 0 || len(d.Preprocessor.Input) == 0 || len(d.Preprocessor.Output) == 0 || d.SLO.Percentile <= 0 || d.SLO.Percentile > 100 || math.IsNaN(d.SLO.Percentile) || d.SLO.TargetLatency <= 0 || (d.RequestLifetime != nil && *d.RequestLifetime <= 0) || (d.Dataset != nil && (d.Dataset.Name == "" || d.Dataset.UID == "")) {
		return false
	}
	for _, fields := range [][]RawField{d.Preprocessor.Input, d.Preprocessor.Output} {
		for i, field := range fields {
			if field.Name == "" || field.Format == "" {
				return false
			}
			for _, prior := range fields[:i] {
				if prior.Name == field.Name {
					return false
				}
			}
		}
	}
	for _, field := range d.Preprocessor.Input {
		if field.MaxBytes <= 0 {
			return false
		}
	}
	return true
}

func (r *Runtime) validateGatewaySwapLocked(next *gatewaySnapshot, ready map[string]ReadyInstance, routing map[string]map[string]int32) error {
	if r.shuttingDown {
		return failure(reasonShuttingDown, "router is shutting down")
	}
	for id, candidate := range ready {
		if current := r.instances[id]; current != nil && current.outstanding > 0 && (current.State.Node != candidate.State.Node || current.State.PodUID != candidate.State.PodUID || current.State.ContainerID != candidate.State.ContainerID || current.State.Profile != candidate.State.Profile || current.OwnerWorkloadUID != candidate.OwnerWorkloadUID) {
			return failure("DrainIncomplete", "physical target %q changed before completion", id)
		}
	}
	for uid, workload := range r.workloads {
		for _, call := range workload.queue {
			if next.definitions[uid] == nil {
				return failure("DrainIncomplete", "accepted workload %q is still queued", uid)
			}
			route := workload.route
			if call.receipt.gateway != nil {
				route = call.receipt.gateway.routes[uid]
			}
			executable := false
			for _, entry := range route.entries {
				candidate, found := ready[entry.instance.State.ID]
				if found && candidate.Capacity > 0 && routing[uid][candidate.State.ID] > 0 && candidate.State.Profile == entry.instance.State.Profile && profilePath(call.definition, candidate.Profile) != nil {
					executable = true
					break
				}
			}
			if !executable {
				return failure("DrainIncomplete", "accepted request %q has no safe pinned target", call.receipt.ID)
			}
		}
	}
	return nil
}

func (r *Runtime) gatewayDrainedLocked(uid, instance, node string) bool {
	return r.config.GatewayDrained == nil || r.config.GatewayDrained(uid, instance, node)
}

func (r *Runtime) recordGatewayArrivalLocked(receipt Receipt, validated bool) {
	if r.gateway == nil || r.gateway.definitions[receipt.UID] == nil {
		return
	}
	if r.gatewayArrivals == nil {
		r.gatewayArrivals = map[gatewayArrivalKey]*gatewayArrivalWindow{}
	}
	key := gatewayArrivalKey{at: receipt.ReceivedAt.UTC().Truncate(time.Second), uid: receipt.UID}
	window := r.gatewayArrivals[key]
	if window == nil {
		r.pruneGatewayArrivalsLocked(time.Now())
		window = &gatewayArrivalWindow{GatewayArrivals: GatewayArrivals{From: key.at, To: key.at.Add(time.Second), WorkloadUID: key.uid}}
		r.gatewayArrivals[key] = window
	}
	if validated {
		window.Validated++
	} else {
		window.Received++
	}
}

// Only whole seconds observed continuously by this process can prove zero.
// Partial startup/reconnection seconds remain gaps, not synthetic no-arrivals.
func (r *Runtime) advanceGatewayArrivalsLocked(now time.Time) {
	if !r.gatewayReadyLocked() || r.gatewayArrivalCursor.IsZero() {
		return
	}
	if r.gatewayArrivals == nil {
		r.gatewayArrivals = map[gatewayArrivalKey]*gatewayArrivalWindow{}
	}
	floor := now.Add(-r.gateway.observationWindow - 10*time.Second).UTC().Truncate(time.Second)
	if r.gatewayArrivalCursor.Before(floor) {
		r.gatewayArrivalCursor = floor
	}
	end := now.UTC().Truncate(time.Second)
	for r.gatewayArrivalCursor.Before(end) {
		for uid := range r.gateway.definitions {
			key := gatewayArrivalKey{at: r.gatewayArrivalCursor, uid: uid}
			window := r.gatewayArrivals[key]
			if window == nil {
				window = &gatewayArrivalWindow{GatewayArrivals: GatewayArrivals{From: key.at, To: key.at.Add(time.Second), WorkloadUID: uid}}
				r.gatewayArrivals[key] = window
			}
			window.complete = true
		}
		r.gatewayArrivalCursor = r.gatewayArrivalCursor.Add(time.Second)
	}
}

func (r *Runtime) pruneGatewayArrivalsLocked(now time.Time) {
	if r.gateway == nil {
		return
	}
	// Two 5-second channel heartbeats retain retransmittable fixed windows.
	cutoff := now.Add(-r.gateway.observationWindow - 10*time.Second)
	for key, window := range r.gatewayArrivals {
		if !window.To.After(cutoff) {
			delete(r.gatewayArrivals, key)
		}
	}
}

// GatewayReport includes reservations before their assigned append/native send,
// and every retained physical identity, even when a newer plan removes it.
// appliedAt is retained for callers that also report a channel synchronization
// time; an installed snapshot always reports its original actual application.
func (r *Runtime) GatewayReport(routerID string, appliedAt time.Time) GatewayReport {
	r.mu.Lock()
	now := time.Now().UTC()
	r.advanceGatewayArrivalsLocked(now)
	r.pruneGatewayArrivalsLocked(now)
	report := GatewayReport{RouterID: routerID, AppliedAt: appliedAt, ObservedAt: now, Requests: r.schedulerOutstandingLocked(now)}
	if r.gateway != nil {
		report.PlanVersion, report.AppliedAt = r.gateway.version, r.gateway.appliedAt
	}
	positions := make(map[*runtimeInstance]int, len(r.instances))
	for id, instance := range r.instances {
		positions[instance] = len(report.Outstanding)
		report.Outstanding = append(report.Outstanding, GatewayOutstanding{InstanceID: id, PodUID: instance.State.PodUID, ContainerID: instance.State.ContainerID, Count: instance.outstanding})
	}
	for requestID, call := range r.calls {
		if call.instance != nil {
			index := positions[call.instance]
			report.Outstanding[index].RequestIDs = append(report.Outstanding[index].RequestIDs, requestID)
		}
	}
	for i := range report.Outstanding {
		slices.Sort(report.Outstanding[i].RequestIDs)
	}
	for _, window := range r.gatewayArrivals {
		if window.complete {
			report.Arrivals = append(report.Arrivals, window.GatewayArrivals)
		}
	}
	r.mu.Unlock()
	sort.Slice(report.Outstanding, func(i, j int) bool { return report.Outstanding[i].InstanceID < report.Outstanding[j].InstanceID })
	sort.Slice(report.Arrivals, func(i, j int) bool {
		if report.Arrivals[i].From.Equal(report.Arrivals[j].From) {
			return report.Arrivals[i].WorkloadUID < report.Arrivals[j].WorkloadUID
		}
		return report.Arrivals[i].From.Before(report.Arrivals[j].From)
	})
	report.Open = r.config.ObservationLog.OpenRows()
	return report
}
