package serving

import (
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

const (
	AdmissionPreIPCRunning        = "PreIPCRunning"
	AdmissionProfileMismatch      = "ProfileMismatch"
	AdmissionMissingPolicyOrBatch = "MissingPolicyOrBatch"
	AdmissionResidualUnsupported  = "ResidualUnsupported"
	AdmissionMissingObservedAt    = "MissingObservedAt"
	AdmissionNoCapacity           = "NoCapacity"
	AdmissionStopped              = "AdmissionStopped"
)

// AdmissionSnapshot identifies the exact physical input used by the gateway.
// ACKPolicy excludes measured populations; ProfileID identifies those immutable
// distributions. NativeSpanAt dates completion-reconciled spans independently
// of the original physical probe. Running retains the observed IPC boundaries.
type AdmissionSnapshot struct {
	Instance              string                    `json:"instance"`
	ContainerID           string                    `json:"containerID,omitempty"`
	Node                  NodeReference             `json:"node"`
	ProfileID             string                    `json:"profileID,omitempty"`
	ObservedAt            time.Time                 `json:"observedAt,omitzero"`
	NativeAt              time.Time                 `json:"nativeAt,omitzero"`
	NativeSpanAt          time.Time                 `json:"nativeSpanAt,omitzero"`
	SnapshotAgeNanos      *int64                    `json:"snapshotAgeNanos"`
	ObservedAgeNanos      *int64                    `json:"observedAgeNanos"`
	ACKPolicy             *firstfloor.Policy        `json:"ackPolicy,omitempty"`
	UnobservedAssignments int                       `json:"unobservedAssignments"`
	Outstanding           int                       `json:"outstanding"`
	Capacity              int                       `json:"capacity"`
	NormalQueued          int                       `json:"normalQueued"`
	DeferredQueued        int                       `json:"deferredQueued"`
	Running               []firstfloor.RequestState `json:"running,omitempty"`
}

// AdmissionPrediction is the selected executor's estimate at admission, not the
// cheapest estimate on another route. Fixed-batch baseline makes no prediction.
type AdmissionPrediction struct {
	At                       time.Time         `json:"at"`
	PredictedCompletionNanos *int64            `json:"predictedCompletionNanos,omitempty"`
	RemainingSLONanos        int64             `json:"remainingSLONanos"`
	FixedBatch               bool              `json:"fixedBatch,omitempty"`
	OutsideSupport           bool              `json:"outsideSupport,omitempty"`
	Input                    AdmissionSnapshot `json:"input"`
}

// AdmissionWait records a change of candidate eligibility or queue class. An
// empty reason retains a code path with no more specific supported diagnosis.
type AdmissionWait struct {
	At           time.Time          `json:"at"`
	ElapsedNanos int64              `json:"elapsedNanos"`
	State        string             `json:"state"`
	Reason       string             `json:"reason,omitempty"`
	Detail       string             `json:"detail,omitempty"`
	Input        *AdmissionSnapshot `json:"input,omitempty"`
}

type admissionWaitIdentity struct {
	state, reason, policy string
	observedAt, nativeAt  time.Time
}

func admissionUnavailableReason(i *runtimeInstance) string {
	if i.State.Stopped {
		return AdmissionStopped
	}
	if !admissionInstanceAvailable(i) {
		return AdmissionNoCapacity
	}
	if i.State.Native == nil || i.Profile == nil {
		return AdmissionMissingPolicyOrBatch
	}
	return ""
}

func (r *Runtime) admissionSnapshot(i *runtimeInstance, now time.Time) AdmissionSnapshot {
	s := AdmissionSnapshot{Instance: i.State.ID, Node: i.State.Node, ProfileID: i.State.Profile,
		ObservedAt: i.State.ObservedAt, NativeSpanAt: i.State.NativeSpanAt,
		Outstanding: i.outstanding, Capacity: i.Capacity, UnobservedAssignments: i.admissionUnobserved,
		NormalQueued: i.admissionNormal, DeferredQueued: i.admissionDeferred}
	if _, container, found := strings.Cut(i.State.ID, "/"); found {
		s.ContainerID = container
	}
	if !i.nativeAdmissionAt().IsZero() {
		age := int64(max(now.Sub(i.nativeAdmissionAt()), 0))
		s.SnapshotAgeNanos = &age
	}
	if !i.State.ObservedAt.IsZero() {
		age := int64(max(now.Sub(i.State.ObservedAt), 0))
		s.ObservedAgeNanos = &age
	}
	if i.Profile != nil {
		s.ProfileID = i.Profile.ID
	}
	if native := i.State.Native; native != nil {
		s.NativeAt = native.At
		policy := native.Metadata.Policy
		policy.Batches = nil
		s.ACKPolicy = &policy
		for _, q := range native.Requests {
			if q.Phase == phaseRunning {
				q.ExchangeElapsedNanos = copyNanos(q.ExchangeElapsedNanos)
				q.PrecedingIdleNanos = copyNanos(q.PrecedingIdleNanos)
				if q.RemainingLifetimeMS != nil {
					value := *q.RemainingLifetimeMS
					q.RemainingLifetimeMS = &value
				}
				s.Running = append(s.Running, q)
			}
		}
	}
	return s
}

func (r *Runtime) recordAdmissionWaitLocked(c *Call, route inputRoute, choice admissionChoice, now time.Time) {
	if choice.entry != nil {
		return
	}
	if len(route.entries) == 0 {
		r.recordAdmissionCandidateWait(c, nil, choice.state, AdmissionNoCapacity, now)
		return
	}
	for j := range route.entries {
		entry := &route.entries[j]
		reason := entry.admissionReason
		state := choice.state
		if c.deferred && reason == "" && r.normalUsesInstanceLocked(entry.instance) {
			state = "NormalPriority"
		}
		r.recordAdmissionCandidateWait(c, entry.instance, state, reason, now)
	}
}

func (r *Runtime) recordAdmissionCandidateWait(c *Call, i *runtimeInstance, state, reason string, now time.Time) {
	key := ""
	identity := admissionWaitIdentity{state: state, reason: reason}
	if i != nil {
		key = i.State.ID
		identity.observedAt = i.State.ObservedAt
		if i.State.Native != nil {
			identity.nativeAt = i.State.Native.At
			identity.policy = i.State.Native.Metadata.Policy.DecisionID
		}
	}
	if previous, ok := c.admissionWaitIdentities[key]; ok && previous == identity {
		return
	}
	if c.admissionWaitIdentities == nil {
		c.admissionWaitIdentities = make(map[string]admissionWaitIdentity)
	}
	c.admissionWaitIdentities[key] = identity
	wait := AdmissionWait{At: now.UTC(), ElapsedNanos: int64(now.Sub(c.receipt.ReceivedAt)), State: state, Reason: reason}
	if i != nil {
		snapshot := r.admissionSnapshot(i, now)
		wait.Input = &snapshot
	}
	c.admissionWaits = append(c.admissionWaits, wait)
}

func (r *Runtime) recordAdmissionPredictionLocked(c *Call, entry *routeEntry, at time.Time) {
	estimate := entry.admissionEstimate
	p := &AdmissionPrediction{At: at.UTC(), RemainingSLONanos: int64(c.definition.SLO.TargetLatency - at.Sub(c.receipt.ReceivedAt)),
		FixedBatch: estimate.FixedBatch, OutsideSupport: estimate.OutsideSupport, Input: r.admissionSnapshot(entry.instance, at)}
	if estimate.Known {
		completion := int64(estimate.Completion)
		p.PredictedCompletionNanos = &completion
	}
	c.admissionPrediction = p
}
