package serving

import (
	"maps"
	"slices"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// nativeAdmissionAt is a local span anchor. ObservedAt and the raw native At
// remain the physical probe's provenance; receiving completion is not a probe.
func (i *runtimeInstance) nativeAdmissionAt() time.Time {
	if !i.admissionAt.IsZero() {
		return i.admissionAt
	}
	return i.State.ObservedAt
}

func (r *Runtime) completeAdmissionLocked(c *Call) {
	i := c.instance
	if c.admissionComplete || i == nil || c.assignment == nil || c.assignment.Instance != i.State.ID || c.assignment.Node != i.State.Node {
		return
	}
	c.admissionComplete = true
	if i.admissionCompleted == nil {
		i.admissionCompleted = make(map[admissionRequestKey]time.Time)
	}
	key := admissionRequestKey{c.receipt.ID, c.receipt.UID, c.identity}
	now := time.Now()
	i.admissionCompleted[key] = now
	i.applyAdmissionCompletions(now)
}

// A probe can have started before completion and arrive afterwards. Keep exact
// request tombstones until a later probe itself excludes those requests.
func (i *runtimeInstance) installAdmissionSnapshot() {
	i.admissionAt = time.Time{}
	if len(i.admissionCompleted) == 0 || i.State.Native == nil {
		return
	}
	seen := make(map[admissionRequestKey]bool, len(i.State.Native.Requests))
	for j := range i.State.Native.Requests {
		q := &i.State.Native.Requests[j]
		seen[admissionRequestKey{q.Context.RequestID, q.Context.WorkloadUID, q.Context.Definition}] = true
	}
	for key, at := range i.admissionCompleted {
		if !seen[key] && !i.State.ObservedAt.Before(at) {
			delete(i.admissionCompleted, key)
		}
	}
	i.applyAdmissionCompletions(time.Now())
}

func (i *runtimeInstance) applyAdmissionCompletions(now time.Time) {
	native := i.State.Native
	if native == nil || len(i.admissionCompleted) == 0 {
		return
	}
	removed, removedRunning, running := 0, false, false
	for j := range native.Requests {
		q := &native.Requests[j]
		_, done := i.admissionCompleted[admissionRequestKey{q.Context.RequestID, q.Context.WorkloadUID, q.Context.Definition}]
		if done {
			removed++
			removedRunning = removedRunning || q.Phase == phaseRunning
		} else {
			running = running || q.Phase == phaseRunning
		}
	}
	if removed == 0 {
		return
	}
	age := max(now.Sub(i.nativeAdmissionAt()), 0)
	updated := *native
	updated.Requests = make([]firstfloor.RequestState, 0, len(native.Requests)-removed)
	for j := range native.Requests {
		observed := &native.Requests[j]
		if _, done := i.admissionCompleted[admissionRequestKey{observed.Context.RequestID, observed.Context.WorkloadUID, observed.Context.Definition}]; done {
			continue
		}
		q := *observed
		q.ElapsedMS += float64(age) / float64(time.Millisecond)
		if q.Phase == phaseQueued || q.Phase == phaseDeferred {
			q.QueueMS += float64(age) / float64(time.Millisecond)
		}
		if q.Phase == phaseRunning {
			q.ServiceElapsedMS += float64(age) / float64(time.Millisecond)
			if q.ExchangeElapsedNanos != nil {
				elapsed := *q.ExchangeElapsedNanos + int64(age)
				q.ExchangeElapsedNanos = &elapsed
			}
		}
		if q.RemainingLifetimeMS != nil {
			remaining := *q.RemainingLifetimeMS - float64(age)/float64(time.Millisecond)
			q.RemainingLifetimeMS = &remaining
		}
		updated.Requests = append(updated.Requests, q)
	}
	updated.ArrivalAgesMS = maps.Clone(native.ArrivalAgesMS)
	for uid, ages := range native.ArrivalAgesMS {
		ages = slices.Clone(ages)
		for j := range ages {
			ages[j] += float64(age) / float64(time.Millisecond)
		}
		updated.ArrivalAgesMS[uid] = ages
	}
	if native.LastServiceEndAgeNanos != nil {
		idle := *native.LastServiceEndAgeNanos + int64(age)
		updated.LastServiceEndAgeNanos = &idle
	}
	if removedRunning && !running {
		// Receipt is a confirmed upper bound on completion time. Like native's
		// decision idle age, this is an explicit lower-bound gap approximation.
		idle := int64(0)
		updated.LastServiceEndAgeNanos = &idle
		updated.DecisionIdleApproximate = true
	}
	i.State.Native, i.admissionAt = &updated, now
}
