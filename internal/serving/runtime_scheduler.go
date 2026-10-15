package serving

import (
	"sort"
	"strconv"
	"time"
)

// EnableSchedulerRouting freezes the current distribution before preparation.
// Discovery of a ready target does not make it part of the applied distribution.
func (r *Runtime) EnableSchedulerRouting() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.routingControlled {
		return
	}
	r.routeTargets = make(map[string]map[string]int32, len(r.workloads))
	for uid, workload := range r.workloads {
		targets := map[string]int32{}
		for _, route := range workload.routes {
			for _, entry := range route.entries {
				targets[entry.instance.State.ID] = entry.instance.Weight
			}
		}
		r.routeTargets[uid] = targets
	}
	r.routingControlled = true
}

// DisableSchedulerRouting restores the observed static Pod distribution.
func (r *Runtime) DisableSchedulerRouting() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routingControlled = false
	r.routeTargets = nil
	r.rebuildLocked()
	r.dispatchLocked()
	r.signal()
}

func (r *Runtime) routeWeightLocked(uid string, instance *runtimeInstance) int32 {
	if r.routingControlled {
		return r.routeTargets[uid][instance.State.ID]
	}
	return instance.Weight
}

// ApplySchedulerTarget is an atomic distribution change after actual readiness.
// Logical preparation slots resolve to physical identities; requests retain the
// physical identity that owns their execution, including during later removal.
func (r *Runtime) ApplySchedulerTarget(candidate SchedulerCandidate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.applySchedulerTargetLocked(candidate, false)
}

// ValidateSchedulerTarget checks the same current UID, ownership and readiness
// fence without changing policy or routing.
func (r *Runtime) ValidateSchedulerTarget(candidate SchedulerCandidate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.applySchedulerTargetLocked(candidate, true)
}

func (r *Runtime) applySchedulerTargetLocked(candidate SchedulerCandidate, validateOnly bool) error {
	targets := make(map[string]map[string]int32, len(r.workloads))
	if err := r.validateSchedulerRoutingLocked(candidate); err != nil {
		return err
	}
	for _, wanted := range candidate.Instances {
		routeNeeded := false
		for _, uid := range wanted.WorkloadUIDs {
			if candidate.Routing[uid][wanted.ID] < 0 {
				return failure("InvalidDistribution", "negative target weight")
			}
			routeNeeded = routeNeeded || candidate.Routing[uid][wanted.ID] > 0
		}
		if !routeNeeded {
			continue
		}
		actual, err := r.schedulerSlotLocked(wanted.ID)
		if err != nil {
			return err
		}
		if actual == nil || !actual.State.Ready || actual.Profile == nil || wanted.Profile == nil || actual.Profile.ID != wanted.Profile.ID || actual.State.Node != wanted.Node || actual.State.Pool != wanted.Pool {
			return failure("TargetNotReady", "target %q lacks matching actual readiness", wanted.ID)
		}
		for _, uid := range wanted.WorkloadUIDs {
			weight := candidate.Routing[uid][wanted.ID]
			if weight == 0 {
				continue
			}
			workload := r.workloads[uid]
			if workload == nil || workload.detached || profilePath(workload.definition, actual.Profile) == nil || (actual.OwnerWorkloadUID != "" && actual.OwnerWorkloadUID != uid) {
				return failure("DefinitionChanged", "target %q does not match current workload %q", wanted.ID, uid)
			}
			if targets[uid] == nil {
				targets[uid] = map[string]int32{}
			}
			if _, duplicate := targets[uid][actual.State.ID]; duplicate {
				return failure("InvalidDistribution", "duplicate physical target for workload")
			}
			targets[uid][actual.State.ID] = weight
		}
	}
	if validateOnly {
		return nil
	}
	// Accepted calls keep their original execution contract through route changes.
	// Retain an old route only while the new distribution cannot execute them.
	r.retainAcceptedRoutesLocked(targets)
	for _, distribution := range targets {
		for id, weight := range distribution {
			if weight > 0 {
				r.instances[id].State.Stopped = false
			}
		}
	}
	r.routeTargets = targets
	r.routingControlled = true
	r.rebuildLocked()
	r.dispatchLocked()
	r.signal()
	return nil
}

// Validate logical route declarations before resolving their physical runners.
func (r *Runtime) validateSchedulerRoutingLocked(candidate SchedulerCandidate) error {
	for uid, workload := range r.workloads {
		if _, present := candidate.Routing[uid]; !workload.detached && !present {
			return failure("InvalidDistribution", "target lacks distribution for workload %q", uid)
		}
	}
	for uid, distribution := range candidate.Routing {
		workload := r.workloads[uid]
		if workload == nil || workload.detached {
			return failure("DefinitionChanged", "target has stale workload %q", uid)
		}
		for id, weight := range distribution {
			if weight < 0 {
				return failure("InvalidDistribution", "negative target weight")
			}
			found := false
			for _, wanted := range candidate.Instances {
				if wanted.ID == id && schedulerHas(wanted.WorkloadUIDs, uid) {
					found = true
					break
				}
			}
			if !found {
				return failure("InvalidDistribution", "target %q is not declared for workload %q", id, uid)
			}
		}
	}
	return nil
}

func (r *Runtime) schedulerSlotLocked(id string) (*runtimeInstance, error) {
	var actual *runtimeInstance
	for _, instance := range r.instances {
		if instance.State.ID != id && instance.State.TargetID != id {
			continue
		}
		if actual != nil {
			return nil, failure("InvalidDistribution", "target slot has multiple physical instances")
		}
		actual = instance
	}
	return actual, nil
}

func (r *Runtime) retainAcceptedRoutesLocked(targets map[string]map[string]int32) {
	for uid, workload := range r.workloads {
		for _, call := range workload.queue {
			executable := false
			for id, weight := range targets[uid] {
				if weight > 0 && r.instanceServesCallLocked(r.instances[id], call) {
					executable = true
					break
				}
			}
			if executable {
				continue
			}
			for _, entry := range workload.routes[call.input.path].entries {
				if !r.instanceServesCallLocked(entry.instance, call) {
					continue
				}
				if targets[uid] == nil {
					targets[uid] = map[string]int32{}
				}
				targets[uid][entry.instance.State.ID] = r.routeWeightLocked(uid, entry.instance)
			}
		}
	}
}

func (r *Runtime) instanceServesCallLocked(instance *runtimeInstance, call *Call) bool {
	if instance == nil || !instance.State.Ready || instance.State.Stopped || (instance.OwnerWorkloadUID != "" && instance.OwnerWorkloadUID != call.receipt.UID) {
		return false
	}
	path := profilePath(call.definition, instance.Profile)
	return path != nil && sameInputs(call.definition.Model.Paths[call.input.path].Tensors, path.Tensors)
}

// StopSchedulerInstance closes new routing after accepted ingress can execute
// elsewhere. Native execution ownership remains until actual drain proof.
func (r *Runtime) StopSchedulerInstance(id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	retiring := r.instances[id]
	if retiring == nil {
		return true, nil
	}
	for _, workload := range r.workloads {
		for _, call := range workload.queue {
			if !r.instanceServesCallLocked(retiring, call) {
				continue
			}
			alternative := false
			for _, entry := range workload.routes[call.input.path].entries {
				if entry.instance.State.ID != id && r.instanceServesCallLocked(entry.instance, call) {
					alternative = true
					break
				}
			}
			if !alternative {
				r.dispatchLocked()
				return false, nil
			}
		}
	}
	retiring.State.Stopped = true
	retiring.State.Reason = "SchedulerDraining"
	for _, targets := range r.routeTargets {
		delete(targets, id)
	}
	r.rebuildLocked()
	r.dispatchLocked()
	r.signal()
	return true, nil
}

// DetachInstance requires every original call's execution, transport and delivery
// proof. A failed probe or HTTP exchange alone cannot free physical ownership.
func (r *Runtime) DetachInstance(id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, call := range r.calls {
		if call.assignment != nil && call.assignment.Instance == id && (!call.backendDone || !call.transportDone || !call.responseRecorded) {
			return false, nil
		}
	}
	if instance := r.instances[id]; instance != nil {
		instance.State.Ready = false
		instance.State.Reason = "InstanceDrained"
		clear(instance.bindings)
	}
	for _, targets := range r.routeTargets {
		delete(targets, id)
	}
	r.rebuildLocked()
	r.dispatchLocked()
	r.signal()
	return true, nil
}

func (r *Runtime) SchedulerDefinitions() []*Definition {
	r.mu.Lock()
	defer r.mu.Unlock()
	definitions := make([]*Definition, 0, len(r.workloads))
	for _, workload := range r.workloads {
		if !workload.detached {
			definitions = append(definitions, workload.definition)
		}
	}
	sort.Slice(definitions, func(a, b int) bool { return definitions[a].UID < definitions[b].UID })
	return definitions
}

// SchedulerOutstanding captures original contexts and uses local observation
// receipt time only for extending worker-reported monotonic durations.
func (r *Runtime) SchedulerOutstanding(now time.Time) []SchedulerRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	queueOrder := make(map[*Call]int)
	for _, workload := range r.workloads {
		for order, call := range workload.queue {
			queueOrder[call] = order
		}
	}
	result := make([]SchedulerRequest, 0, len(r.calls))
	for _, call := range r.calls {
		if call.definition == nil || (call.backendDone && call.transportDone && call.responseRecorded) {
			continue
		}
		request := SchedulerRequest{ID: call.receipt.ID, Definition: call.definition, InputPath: call.input.path, QueueOrder: queueOrder[call], ReceivedAt: call.receipt.ReceivedAt, Elapsed: now.Sub(call.receipt.ReceivedAt), Phase: phasePending}
		if request.Elapsed < 0 {
			request.Elapsed = 0
		}
		if !call.receipt.Deadline.IsZero() {
			remaining := call.receipt.Deadline.Sub(now)
			if remaining < 0 {
				remaining = 0
			}
			request.RemainingLifetime = &remaining
		}
		request.QueueAge = request.Elapsed
		if call.assignment != nil {
			request.Instance = call.assignment.Instance
			request.Phase = phaseAssigned
			request.QueueAge = 0
			r.extendNativeOutstandingLocked(&request, call, now)
		}
		if call.backendDone {
			if !call.exchangeFinishedAt.IsZero() {
				request.Phase = phaseReturning
			}
			if call.progress != nil {
				request.NativeOutcome = call.progress.Outcome
			}
			if !call.exchangeFinishedAt.IsZero() {
				request.ReturnElapsed = now.Sub(call.exchangeFinishedAt)
				exchangeElapsed := call.exchangeFinishedAt.Sub(call.receipt.ReceivedAt)
				if p := call.progress; p != nil && p.Phase == "finished" && p.ExecutionComplete && call.clientSpan > 0 && call.clientSpan <= exchangeElapsed && schedulerFinite(p.NativeElapsedMS) && p.NativeElapsedMS >= 0 {
					nativeNanos := p.NativeElapsedMS * float64(time.Millisecond)
					if nativeNanos <= float64(call.clientSpan) && request.ReturnElapsed >= 0 {
						residual := exchangeElapsed - time.Duration(nativeNanos)
						request.ExchangeResidual = &residual
					}
				}
			}
		}
		result = append(result, request)
	}
	sort.Slice(result, func(a, b int) bool {
		if result[a].ReceivedAt.Equal(result[b].ReceivedAt) {
			return result[a].ID < result[b].ID
		}
		return result[a].ReceivedAt.Before(result[b].ReceivedAt)
	})
	return result
}

func (r *Runtime) extendNativeOutstandingLocked(request *SchedulerRequest, call *Call, now time.Time) {
	if instance := r.instances[request.Instance]; instance != nil && instance.State.Native != nil {
		for order := range instance.State.Native.Requests {
			native := &instance.State.Native.Requests[order]
			if native.Context.RequestID != request.ID || native.Context.WorkloadUID != call.receipt.UID || native.Context.Definition != call.identity {
				continue
			}
			request.Phase = native.Phase
			request.QueueOrder = order
			request.QueueAge = time.Duration(native.QueueMS * float64(time.Millisecond))
			request.RunningElapsed = time.Duration(native.ServiceElapsedMS * float64(time.Millisecond))
			request.ArrivalRate = native.ArrivalRate
			request.NativeSenderElapsedMS = &native.Context.SenderElapsedMS
			request.BatchID = strconv.FormatUint(native.BatchID, 10)
			age := now.Sub(instance.State.ObservedAt)
			if age > 0 {
				if request.Phase == phaseRunning {
					request.RunningElapsed += age
				} else if request.Phase == phaseQueued || request.Phase == phaseDeferred {
					request.QueueAge += age
				}
			}
			if native.RemainingLifetimeMS != nil {
				remaining := time.Duration(*native.RemainingLifetimeMS * float64(time.Millisecond))
				if age > 0 {
					remaining -= age
				}
				if remaining < 0 {
					remaining = 0
				}
				if request.RemainingLifetime == nil || remaining < *request.RemainingLifetime {
					request.RemainingLifetime = &remaining
				}
			} else if native.Context.LifetimeMS > 0 && (request.Phase == phaseQueued || request.Phase == phaseDeferred) {
				request.Phase = phaseAssigned
			}
			break
		}
	}
}

func (r *Runtime) SchedulerRouting() (map[string]map[string]int32, map[string]map[int]map[string]int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	weights := map[string]map[string]int32{}
	credits := map[string]map[int]map[string]int64{}
	for uid, workload := range r.workloads {
		weights[uid] = map[string]int32{}
		credits[uid] = map[int]map[string]int64{}
		for path, route := range workload.routes {
			credits[uid][path] = map[string]int64{}
			for index, entry := range route.entries {
				weights[uid][entry.instance.State.ID] = route.weights[index]
				credits[uid][path][entry.instance.State.ID] = route.current[index]
			}
		}
	}
	return weights, credits
}
