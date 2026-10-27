package gateway

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/live"
)

func validRouterID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func generationPod(id string) string {
	// Pod UIDs contain hyphens: only the final random 128-bit suffix is generation.
	index := strings.LastIndexByte(id, '-')
	if index < 1 || !validRouterID(id) {
		return ""
	}
	generation := id[index+1:]
	if len(generation) != 32 {
		return ""
	}
	if _, err := hex.DecodeString(generation); err != nil {
		return ""
	}
	return id[:index]
}
func samePodGeneration(a, b string) bool {
	pod := generationPod(a)
	return pod != "" && pod == generationPod(b)
}
func validateOutstanding(report serving.GatewayReport) error {
	if report.RouterID == "" || report.ObservedAt.IsZero() {
		return live.ErrProtocol
	}
	seen := map[string]bool{}
	for _, o := range report.Outstanding {
		if o.PodUID == "" || o.ContainerID == "" || o.InstanceID != o.PodUID+"/"+o.ContainerID || seen[o.InstanceID] || o.Count < 0 || o.Count != len(o.RequestIDs) {
			return live.ErrProtocol
		}
		seen[o.InstanceID] = true
		ids := map[string]bool{}
		for _, id := range o.RequestIDs {
			if id == "" || ids[id] {
				return live.ErrProtocol
			}
			ids[id] = true
		}
	}
	return nil
}

// registerRecoveryLocked accepts only the same-Pod generations held by the
// binary's exclusive per-Pod observation lock. They never become participants
// and do not ACK the new process's routing plan.
func (c *Control) registerRecoveryLocked(id string, reports []serving.GatewayReport) error {
	seen := map[string]bool{}
	for _, report := range reports {
		if report.RouterID == id || !samePodGeneration(id, report.RouterID) || seen[report.RouterID] {
			return live.ErrProtocol
		}
		seen[report.RouterID] = true
		if err := validateOutstanding(report); err != nil {
			return err
		}
		old := c.members[report.RouterID]
		if old != nil && old.Connected {
			return errors.New("recovery source still has a live connection")
		}
		if old != nil && old.Custodian != "" && old.Custodian != id {
			if custodian := c.members[old.Custodian]; custodian != nil && custodian.Connected {
				return errors.New("recovery source already has a live custodian")
			}
		}
	}
	for _, report := range reports {
		old := c.members[report.RouterID]
		if old == nil {
			old = &member{RouterState: RouterState{RouterID: report.RouterID, JoinedAt: report.ObservedAt, LeftAt: report.ObservedAt, Exposed: map[string]bool{}, Sources: map[string]Source{}}}
			c.members[report.RouterID] = old
		}
		if report.Arrivals == nil {
			report.Arrivals = old.Report.Arrivals
		}
		old.Custodian = id
		old.Report = report
		old.LastSeen = time.Now().UTC()
		if old.LeftAt.IsZero() {
			old.LeftAt = time.Now().UTC()
			if c.membershipRecorder != nil {
				if err := c.membershipRecorder(old.RouterState); err != nil {
					return c.failLocked(err)
				}
			}
		}
	}
	return nil
}
func (c *Control) updateRecoveryLocked(owner *member, msg message) error {
	seen := map[string]bool{}
	for _, report := range msg.Recovery {
		old := c.members[report.RouterID]
		if old == nil || old.Custodian != owner.RouterID || old.CustodyGeneration != owner.Generation || seen[report.RouterID] {
			return live.ErrProtocol
		}
		seen[report.RouterID] = true
		if err := validateOutstanding(report); err != nil {
			return err
		}
		if report.Arrivals == nil {
			report.Arrivals = old.Report.Arrivals
		}
		old.Report = report
		old.LastSeen = time.Now().UTC()
		// Only the full, generation-checked post-resync custody report seals the
		// dead process's assignment gate. Registration/ingress exclusion is not zero.
		old.Exposed = map[string]bool{}
		pending := old.Pending[:0]
		for _, e := range old.Pending {
			if e.Generation == owner.Generation && msg.Checkpoint >= e.Seq && (e.State != nil || !reportOwns(report, e.Instance, e.Progress.RequestID)) {
				continue
			}
			reflectEvidence(&old.Report, e)
			pending = append(pending, e)
		}
		old.Pending = pending
	}
	for _, old := range c.members {
		if old.Custodian == owner.RouterID && old.CustodyGeneration == owner.Generation && !seen[old.RouterID] {
			return live.ErrProtocol
		}
	}
	return nil
}
func (c *Control) deliveryLocked(owner *member) *member {
	if owner.Connected {
		return owner
	}
	custodian := c.members[owner.Custodian]
	if custodian != nil && custodian.Connected && custodian.Generation == owner.CustodyGeneration {
		return custodian
	}
	return nil
}
func (c *Control) slotAvailableLocked(owner *member) bool {
	for _, old := range c.members {
		if old.Custodian != owner.RouterID || old.CustodyGeneration != owner.Generation {
			continue
		}
		for _, o := range old.Report.Outstanding {
			if o.Count < 1 {
				continue
			}
			for _, t := range c.plan.Targets {
				if o.InstanceID == t.InstanceID {
					return false
				}
			}
		}
	}
	return true
}

// RequestsAt ages detached scheduler values from their owner's observation time,
// without cloning unrelated rows/profiles or mutating the stored snapshot.
func (c *Control) RequestsAt(now time.Time) []serving.SchedulerRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	var requests []serving.SchedulerRequest
	for _, owner := range c.members {
		age := now.Sub(owner.Report.ObservedAt)
		if age < 0 {
			age = 0
		}
		for _, request := range owner.Report.Requests {
			request.Elapsed += age
			switch request.Phase {
			case "pending", "queued":
				request.QueueAge += age
			case "running":
				request.RunningElapsed += age
			case "returning":
				request.ReturnElapsed += age
			}
			if request.RemainingLifetime != nil {
				remaining := *request.RemainingLifetime - age
				if remaining < 0 {
					remaining = 0
				}
				request.RemainingLifetime = &remaining
			}
			requests = append(requests, request)
		}
	}
	return requests
}
