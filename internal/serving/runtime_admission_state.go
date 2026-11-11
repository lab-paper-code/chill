package serving

import (
	"maps"
	"slices"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

type admissionCompletion struct {
	receivedAt, notBefore time.Time
	executed, unknown     bool
}

// Physical observation provenance and derived native spans have separate clocks.
func (i *runtimeInstance) nativeAdmissionAt() time.Time {
	if !i.State.NativeSpanAt.IsZero() {
		return i.State.NativeSpanAt
	}
	return i.State.ObservedAt
}

func (r *Runtime) completeAdmissionLocked(c *Call) {
	i := c.instance
	if i == nil || c.assignment == nil || c.assignment.Instance != i.State.ID || c.assignment.Node != i.State.Node {
		return
	}
	key := admissionRequestKey{c.receipt.ID, c.receipt.UID, c.identity}
	previous, found := i.admissionCompleted[key]
	if c.admissionComplete && !found {
		return // A later physical probe has already reconciled this request.
	}
	if found && !previous.unknown && !previous.executed {
		return // A confirmed no-execution proof cannot later execute.
	}
	if len(i.admissionCompleted) == 0 {
		i.admissionSourceIdleUnknown = i.State.AdmissionIdleUnknown
	}
	now := time.Now()
	received := now
	if found {
		received = previous.receivedAt
	}
	start := c.assignment.DispatchedAt
	elapsed, httpAttempted := c.receipt.ServerTiming.Elapsed(ServerWorkerHTTPStart)
	if httpAttempted {
		httpStart := c.receipt.ReceivedAt.Add(elapsed)
		if httpStart.After(start) {
			start = httpStart
		}
	}
	executed := c.progress != nil && c.progress.BatchID > 0 && c.progress.BatchSize > 0 &&
		(c.progress.BackendMS > 0 || c.progress.ServiceSample != nil && c.progress.ServiceSample.ServiceDurationNanos > 0)
	// The native client records HTTPStart on every attempted exchange; its
	// Finished-without-progress pre-exchange returns prove no submission.
	noExecution := c.admissionNoExecution || c.progress == nil && !httpAttempted || c.receipt.Rejection != nil ||
		c.progress != nil && c.progress.Phase == phaseFinished && c.progress.BatchID == 0 && c.progress.BatchSize == 0 && c.progress.ServiceMS == 0 && c.progress.BackendMS == 0
	validStart := !start.IsZero() && !start.After(received)
	fact := admissionCompletion{receivedAt: received, notBefore: start, executed: executed && validStart, unknown: !executed && !noExecution || executed && !validStart}
	if found && previous.executed {
		fact.executed, fact.unknown = true, false
	}
	if found && (!validStart || previous.notBefore.After(start)) {
		fact.notBefore = previous.notBefore
	}
	if found && fact == previous {
		return
	}
	c.admissionComplete = true
	if i.admissionCompleted == nil {
		i.admissionCompleted = make(map[admissionRequestKey]admissionCompletion)
	}
	i.admissionCompleted[key] = fact
	i.applyAdmissionCompletions(now)
}

// A probe can have started before completion and arrive afterwards. Keep exact
// request facts until a probe started after their receipt excludes those rows.
func (i *runtimeInstance) installAdmissionSnapshot() {
	i.admissionSourceIdleUnknown = i.State.AdmissionIdleUnknown
	if len(i.admissionCompleted) == 0 || i.State.Native == nil {
		return
	}
	seen := make(map[admissionRequestKey]bool, len(i.State.Native.Requests))
	for j := range i.State.Native.Requests {
		q := &i.State.Native.Requests[j]
		seen[admissionRequestKey{q.Context.RequestID, q.Context.WorkloadUID, q.Context.Definition}] = true
	}
	for key, fact := range i.admissionCompleted {
		if !seen[key] && !i.State.ObservedAt.Before(fact.receivedAt) {
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
	removed, running := 0, false
	for j := range native.Requests {
		q := &native.Requests[j]
		key := admissionRequestKey{q.Context.RequestID, q.Context.WorkloadUID, q.Context.Definition}
		fact, done := i.admissionCompleted[key]
		if done {
			removed++
			if !fact.executed && q.Phase == phaseRunning && q.BatchID > 0 && q.BatchSize > 0 && q.ExchangeElapsedNanos != nil &&
				!fact.notBefore.IsZero() && !fact.notBefore.After(fact.receivedAt) {
				fact.executed, fact.unknown = true, false
				i.admissionCompleted[key] = fact
			}
		} else {
			running = running || q.Phase == phaseRunning
		}
	}
	anchor := i.nativeAdmissionAt()
	var earliest, latest time.Time
	if native.LastServiceEndAgeNanos != nil {
		latest = anchor.Add(-time.Duration(*native.LastServiceEndAgeNanos))
		earliest = latest
		if i.State.LastServiceEndAgeUpperNanos != nil {
			earliest = anchor.Add(-time.Duration(*i.State.LastServiceEndAgeUpperNanos))
		}
	}
	beforeEarliest, beforeLatest := earliest, latest
	unknown := i.admissionSourceIdleUnknown
	for _, fact := range i.admissionCompleted {
		unknown = unknown || fact.unknown
		if !running && fact.executed {
			if earliest.IsZero() || fact.notBefore.After(earliest) {
				earliest = fact.notBefore
			}
			if latest.IsZero() || fact.receivedAt.After(latest) {
				latest = fact.receivedAt
			}
		}
	}
	if removed == 0 && earliest.Equal(beforeEarliest) && latest.Equal(beforeLatest) && unknown == i.State.AdmissionIdleUnknown {
		return
	}
	age := max(now.Sub(anchor), 0)
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
	if !latest.IsZero() {
		lower, upper := int64(max(now.Sub(latest), 0)), int64(max(now.Sub(earliest), 0))
		updated.LastServiceEndAgeNanos = &lower
		i.State.LastServiceEndAgeUpperNanos = nil
		if upper != lower {
			i.State.LastServiceEndAgeUpperNanos = &upper
		}
	}
	i.State.Native, i.State.NativeSpanAt, i.State.AdmissionIdleUnknown = &updated, now, unknown
}
