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
// request waits in the gateway queue. Depth is present while the instance is
// ready: it then holds its limit.
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

// recordAdmissionWaits records why c waits on each entry of route, as the
// decision chooseLocked made on route left them.
func (c *Call) recordAdmissionWaits(route inputRoute, state string, now time.Time) {
	if len(route.entries) == 0 {
		c.recordAdmissionWait(nil, state, now)
	}
	for j := range route.entries {
		c.recordAdmissionWait(&route.entries[j], state, now)
	}
}

func (c *Call) recordAdmissionWait(e *routeEntry, state string, now time.Time) {
	wait := AdmissionWait{At: now.UTC(), ElapsedNanos: int64(now.Sub(c.receipt.ReceivedAt)), State: state, Reason: AdmissionNoCapacity}
	if e != nil {
		wait.Instance, wait.Reason = e.instance.State.ID, e.reason
		if e.reason == "" {
			wait.Depth = &AdmissionDepth{InFlight: e.instance.outstanding, Limit: e.limit}
		}
	}
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
