package gateway

import (
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// ObservationSources copies only the observation reader's source metadata and
// open rows. Directory is supplied by the composition root's read-only mounts.
func (c *Control) ObservationSources() []serving.RouterObservationSource {
	c.mu.Lock()
	defer c.mu.Unlock()
	sources := make([]serving.RouterObservationSource, 0, len(c.members))
	for _, owner := range c.members {
		source := serving.RouterObservationSource{RouterID: owner.RouterID, JoinedAt: owner.JoinedAt, LeftAt: owner.LeftAt, ObservedAt: owner.Report.ObservedAt}
		source.Open = make([]serving.RequestRow, len(owner.Report.Open))
		for i, row := range owner.Report.Open {
			if row.Node != nil {
				node := *row.Node
				row.Node = &node
			}
			for _, field := range []**int64{&row.ElapsedNanos, &row.ValidatedNanos, &row.AcceptedNanos, &row.DispatchedNanos, &row.QueuedNanos, &row.RunningNanos, &row.ExecutedNanos, &row.ExchangeNanos} {
				if *field != nil {
					value := **field
					*field = &value
				}
			}
			for _, field := range []**time.Time{&row.DispatchedAt, &row.NativeQueuedAt, &row.NativeRunningAt, &row.NativeFinishedAt} {
				if *field != nil {
					value := **field
					*field = &value
				}
			}
			if row.NativeElapsedMS != nil {
				value := *row.NativeElapsedMS
				row.NativeElapsedMS = &value
			}
			source.Open[i] = row
		}
		sources = append(sources, source)
	}
	return sources
}
