package observation

import (
	"context"
	"slices"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// EventObservationWindow bounds event-time queries, not the stored retention.
const EventObservationWindow = 24 * time.Hour

// EventObservationPage continues in append-hour/row order, like exact lookups.
// Results are event-time sorted within a page, not across continuation pages.
func (l *ObservationLog) EventObservationPage(ctx context.Context, from, to time.Time, limit int, cursor string) ([]RecordedEvent, string, serving.ObservationCoverage, error) {
	if from.IsZero() || !from.Before(to) || to.Sub(from) > EventObservationWindow || limit <= 0 {
		return nil, "", serving.ObservationCoverage{}, &serving.Error{Code: "InvalidQuery", Message: "bounded positive event interval and limit are required"}
	}
	events, next, coverage, err := l.eventObservationPage(ctx, from, to, limit, cursor)
	slices.SortStableFunc(events, func(a, b RecordedEvent) int {
		if n := a.At.Compare(b.At); n != 0 {
			return n
		}
		if a.Cluster != nil && b.Cluster == nil {
			return -1
		}
		if a.Cluster == nil && b.Cluster != nil {
			return 1
		}
		return 0
	})
	return events, next, coverage, err
}
