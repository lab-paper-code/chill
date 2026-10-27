package gateway

import (
	"fmt"
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

type arrivalKey struct {
	at  time.Time
	uid string
}
type DefinitionPeriod struct {
	JoinedAt time.Time `json:"joinedAt"`
	LeftAt   time.Time `json:"leftAt,omitempty"`
}

func (c *Control) definitionPeriodsLocked(source *member, report serving.GatewayReport) {
	if source.Definitions != nil && source.Report.PlanVersion == report.PlanVersion {
		return
	}
	if source.Definitions == nil {
		source.Definitions = map[string][]DefinitionPeriod{}
	}
	present := make(map[string]bool, len(c.plan.Definitions))
	for _, definition := range c.plan.Definitions {
		present[definition.UID] = true
	}
	for uid, periods := range source.Definitions {
		if len(periods) > 0 && !present[uid] && periods[len(periods)-1].LeftAt.IsZero() {
			periods[len(periods)-1].LeftAt = report.AppliedAt
		}
	}
	for uid := range present {
		periods := source.Definitions[uid]
		if len(periods) == 0 || !periods[len(periods)-1].LeftAt.IsZero() {
			source.Definitions[uid] = append(periods, DefinitionPeriod{JoinedAt: report.AppliedAt})
		}
	}
	source.ArrivalWindow = c.plan.ObservationWindow
}
func incompleteArrivals(reason string) error {
	return &serving.Error{Code: "IncompleteObservation", Message: reason}
}

// ArrivalCounts is the fast counter reader, not the scheduler's raw replay
// population. Each complete (router generation, UTC second, UID) is counted once
// from its latest full report; no stream-derived arrivals are added here.
func (c *Control) ArrivalCounts(from, to time.Time) ([]serving.GatewayArrivals, error) {
	if from.IsZero() || !from.Before(to) || !from.Equal(from.Truncate(time.Second)) || !to.Equal(to.Truncate(time.Second)) {
		return nil, incompleteArrivals("arrival counts require a nonempty whole-second window")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	totals := map[string]serving.GatewayArrivals{}
	covered := false
	for _, source := range c.members {
		if !source.LeftAt.IsZero() && !source.LeftAt.After(from) || !source.JoinedAt.Before(to) {
			continue
		}
		covered = true
		if source.JoinedAt.IsZero() || source.Report.ObservedAt.IsZero() || source.Connected && time.Since(source.LastSeen) > FreshnessTimeout {
			return nil, incompleteArrivals(fmt.Sprintf("router %s has no fresh arrival coverage", source.RouterID))
		}
		begin, end := from, to
		if begin.Before(source.JoinedAt) {
			begin = source.JoinedAt
		}
		if !source.LeftAt.IsZero() && end.After(source.LeftAt) {
			end = source.LeftAt
		}
		if !begin.Equal(begin.Truncate(time.Second)) || !end.Equal(end.Truncate(time.Second)) || end.After(source.Report.ObservedAt) {
			return nil, incompleteArrivals(fmt.Sprintf("router %s has a partial or unreported second", source.RouterID))
		}
		if source.ArrivalWindow <= 0 || begin.Before(source.Report.ObservedAt.Add(-source.ArrivalWindow-10*time.Second)) {
			return nil, incompleteArrivals(fmt.Sprintf("router %s lacks retained arrival history", source.RouterID))
		}
		windows := make(map[arrivalKey]serving.GatewayArrivals, len(source.Report.Arrivals))
		if source.Definitions == nil {
			return nil, incompleteArrivals(fmt.Sprintf("router %s lacks workload observation membership", source.RouterID))
		}
		for _, window := range source.Report.Arrivals {
			windows[arrivalKey{window.From.UTC(), window.WorkloadUID}] = window
		}
		for at := begin; at.Before(end); at = at.Add(time.Second) {
			for uid, periods := range source.Definitions {
				expected := false
				for _, period := range periods {
					if period.JoinedAt.Before(at.Add(time.Second)) && (period.LeftAt.IsZero() || period.LeftAt.After(at)) {
						expected = true
						break
					}
				}
				if !expected {
					continue
				}
				window, ok := windows[arrivalKey{at.UTC(), uid}]
				if !ok {
					return nil, incompleteArrivals(fmt.Sprintf("router %s workload %s lacks a complete arrival second", source.RouterID, uid))
				}
				total := totals[uid]
				total.From, total.To, total.WorkloadUID = from, to, uid
				if ^uint64(0)-total.Received < window.Received || ^uint64(0)-total.Validated < window.Validated {
					return nil, incompleteArrivals("arrival counter sum overflow")
				}
				total.Received += window.Received
				total.Validated += window.Validated
				totals[uid] = total
			}
		}
	}
	if !covered {
		return nil, incompleteArrivals("no router source covers the arrival window")
	}
	result := make([]serving.GatewayArrivals, 0, len(totals))
	for _, total := range totals {
		result = append(result, total)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].WorkloadUID < result[j].WorkloadUID })
	return result, nil
}
func validateArrivals(report serving.GatewayReport) error {
	seen := make(map[arrivalKey]bool, len(report.Arrivals))
	for _, window := range report.Arrivals {
		key := arrivalKey{window.From.UTC(), window.WorkloadUID}
		if window.WorkloadUID == "" || window.From.IsZero() || !window.From.Equal(window.From.Truncate(time.Second)) || !window.To.Equal(window.From.Add(time.Second)) || window.To.After(report.ObservedAt) || window.Validated > window.Received || seen[key] {
			return incompleteArrivals("invalid or duplicate complete arrival window")
		}
		seen[key] = true
	}
	return nil
}
