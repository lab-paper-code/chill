package serving

import "time"

const (
	AdmissionNoCapacity = "NoCapacity"
	AdmissionStopped    = "AdmissionStopped"
)

// AdmissionDepth is a routed instance's requests the gateway has dispatched
// and not yet received a reply for, and its limit (admissionLimit).
type AdmissionDepth struct {
	InFlight int `json:"inFlight"`
	Limit    int `json:"limit"`
}

// AdmissionWait records a change of a routed instance's eligibility while a
// request waits in the gateway queue. State is QueueFull while a ready routed
// instance holds its limit, NoCapacity while none is ready. Reason is the
// instance's own: QueueFull with its Depth, NoCapacity, or AdmissionStopped.
type AdmissionWait struct {
	At           time.Time       `json:"at"`
	ElapsedNanos int64           `json:"elapsedNanos"`
	State        string          `json:"state"`
	Reason       string          `json:"reason,omitempty"`
	Detail       string          `json:"detail,omitempty"`
	Instance     string          `json:"instance,omitempty"`
	Depth        *AdmissionDepth `json:"depth,omitempty"`
}

type admissionWaitIdentity struct{ state, reason string }

// admissionUnreadyReason is why instance i cannot take a request at all;
// empty when it is ready and only its depth decides.
func admissionUnreadyReason(i *runtimeInstance) string {
	switch {
	case i.State.Stopped:
		return AdmissionStopped
	case !i.State.Ready || i.State.Terminated || i.State.RecoveryUnknown:
		return AdmissionNoCapacity
	}
	return ""
}

// admissionDepths is each ready routed instance's depth at the decision
// chooseLocked made on route, by instance ID.
func admissionDepths(route inputRoute) map[string]AdmissionDepth {
	depths := make(map[string]AdmissionDepth, len(route.entries))
	for _, e := range route.entries {
		if e.reason == "" {
			depths[e.instance.State.ID] = AdmissionDepth{InFlight: e.instance.outstanding, Limit: e.limit}
		}
	}
	return depths
}

// admissionLimits copies the limits chooseLocked set on route's entries for
// the request it decided; deciding a request of another Definition rewrites
// them, so a waiting request keeps its own copy.
func admissionLimits(route inputRoute) []int {
	limits := make([]int, len(route.entries))
	for j, e := range route.entries {
		limits[j] = e.limit
	}
	return limits
}

// recordAdmissionWaits records why c waits on each entry of route, with the
// limits of c's own decision.
func (c *Call) recordAdmissionWaits(route inputRoute, state string, limits []int, now time.Time) {
	if len(route.entries) == 0 {
		c.recordAdmissionWait("", AdmissionNoCapacity, nil, state, now)
	}
	for j, e := range route.entries {
		if e.reason != "" {
			c.recordAdmissionWait(e.instance.State.ID, e.reason, nil, state, now)
		} else {
			c.recordAdmissionWait(e.instance.State.ID, CodeQueueFull, &AdmissionDepth{InFlight: e.instance.outstanding, Limit: limits[j]}, state, now)
		}
	}
}

func (c *Call) recordAdmissionWait(instance, reason string, depth *AdmissionDepth, state string, now time.Time) {
	wait := AdmissionWait{At: now.UTC(), ElapsedNanos: int64(now.Sub(c.receipt.ReceivedAt)), State: state, Reason: reason, Instance: instance, Depth: depth}
	identity := admissionWaitIdentity{state: state, reason: wait.Reason}
	if previous, ok := c.admissionWaitIdentities[wait.Instance]; ok && previous == identity {
		return
	}
	if c.admissionWaitIdentities == nil {
		c.admissionWaitIdentities = make(map[string]admissionWaitIdentity)
	}
	c.admissionWaitIdentities[wait.Instance] = identity
	c.admissionWaits = append(c.admissionWaits, wait)
}
