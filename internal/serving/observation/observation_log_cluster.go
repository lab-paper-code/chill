package observation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// ClusterObservation preserves observed Pod identity and observer issue transitions.
// At is the server observation time, not a reconstructed Kubernetes event time.
type ClusterObservation struct {
	Kind           string      `json:"kind"`
	At             time.Time   `json:"at"`
	Event          string      `json:"event"`
	Pod            *ClusterPod `json:"pod,omitempty"`
	Source         string      `json:"source,omitempty"`
	Reason         string      `json:"reason,omitempty"`
	PreviousReason string      `json:"previousReason,omitempty"`
	// Startup-only fields stay absent in historical and ordinary Pod rows.
	QueueLimit      *int           `json:"queueLimit,omitempty"`
	SourceInterval  *time.Duration `json:"sourceIntervalNanos,omitempty"`
	ProfileInterval *time.Duration `json:"profileIntervalNanos,omitempty"`
}

type ClusterPod struct {
	Namespace      string             `json:"namespace,omitempty"`
	Name           string             `json:"name,omitempty"`
	UID            string             `json:"uid"`
	Node           string             `json:"node,omitempty"`
	CreatedAt      *time.Time         `json:"creationTimestamp,omitempty"`
	DeletedAt      *time.Time         `json:"deletionTimestamp,omitempty"`
	Phase          string             `json:"phase,omitempty"`
	Ready          *string            `json:"ready,omitempty"`
	InitContainers []ClusterContainer `json:"initContainers,omitempty"`
	Containers     []ClusterContainer `json:"containers,omitempty"`
}

type ClusterContainer struct {
	Name           string              `json:"name"`
	ContainerID    string              `json:"containerID,omitempty"`
	ImageID        string              `json:"imageID,omitempty"`
	RestartCount   *int32              `json:"restartCount,omitempty"`
	Terminated     *ClusterTermination `json:"terminated,omitempty"`
	LastTerminated *ClusterTermination `json:"lastTerminated,omitempty"`
}

type ClusterTermination struct {
	Reason     string     `json:"reason,omitempty"`
	ExitCode   int32      `json:"exitCode"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

func (l *ObservationLog) ClusterRecord(row ClusterObservation) error {
	if row.At.IsZero() {
		return errors.New("cluster observation time is required")
	}
	switch row.Kind {
	case "pod":
		if row.Pod == nil || row.Pod.UID == "" {
			return errors.New("cluster Pod UID is required")
		}
		switch row.Event {
		case "observed", "created", "changed", "deleted", "server-start":
		default:
			return errors.New("invalid cluster Pod event")
		}
	case "issue":
		if row.Source == "" || row.Reason == "" {
			return errors.New("cluster issue source and reason are required")
		}
		switch row.Event {
		case "raised", "changed", "resolved":
		default:
			return errors.New("invalid cluster issue event")
		}
	default:
		return errors.New("invalid cluster observation kind")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendLocked(observationClusterStream, l.now(), row)
}

// RecordedEvent is the durable source for an event-lane recovery read. It never
// hydrates scheduler snapshots, measurement profiles or unrelated catalog rows.
type RecordedEvent struct {
	At      time.Time                   `json:"at"`
	Action  *SchedulerActionObservation `json:"action,omitempty"`
	Cluster *ClusterObservation         `json:"cluster,omitempty"`
}

// EventObservations reads a bounded append-time page, preserving all simultaneous
// facts in that page. A continuation is returned even when a match was found:
// later hours may contain additional facts with the same event timestamp.
func (l *ObservationLog) EventObservations(ctx context.Context, at time.Time, limit int, cursor string) ([]RecordedEvent, string, serving.ObservationCoverage, error) {
	coverage := serving.ObservationCoverage{Sources: []serving.ObservationSource{}, Gaps: []serving.ObservationGap{}}
	from := at.UTC().Truncate(time.Hour)
	now := l.now().UTC()
	if at.IsZero() || limit <= 0 {
		return nil, "", coverage, &serving.Error{Code: "InvalidQuery", Message: "an event timestamp and positive event bound are required"}
	}
	if cursor != "" {
		next, err := time.Parse(time.RFC3339Nano, cursor)
		if err != nil || next.Before(from) || next.After(now) || !next.Equal(next.Truncate(time.Hour)) {
			return nil, "", coverage, &serving.Error{Code: "InvalidQuery", Message: "an append-hour event cursor is required"}
		}
		from = next.UTC()
	}
	coverage.From, coverage.To = &from, &from
	if from.After(now) {
		return nil, "", coverage, ctx.Err()
	}
	hours := min(l.config.EventLookupHours, int(now.Sub(from)/time.Hour)+1)
	to := from.Add(time.Duration(hours) * time.Hour)
	// prepare's upper bound is inclusive; do not open the next hour.
	bounds := [2]time.Time{from, to.Add(-time.Nanosecond)}
	read, err := l.prepare(ctx, map[string][2]time.Time{observationSchedulerStream: bounds, observationClusterStream: bounds})
	if err != nil {
		return nil, "", coverage, err
	}
	events := []RecordedEvent(nil)
	defer read.close()
	next := ""
	if !to.After(read.now) {
		next = to.Format(time.RFC3339Nano)
	}
	to = minTime(to, read.now)
	for _, stream := range []string{observationSchedulerStream, observationClusterStream} {
		err := l.scanStream(read, stream, func(raw []byte) error {
			var header struct {
				At time.Time `json:"at"`
			}
			// schedulerLine writes at first. Decode only that small field before
			// touching multi-megabyte snapshots or action evidence bodies.
			if stream == observationSchedulerStream && bytes.HasPrefix(raw, []byte(`{"at":`)) {
				end := bytes.IndexByte(raw, ',')
				if end < 0 {
					end = len(raw) - 1
				}
				if err := header.At.UnmarshalJSON(raw[len(`{"at":`):end]); err != nil {
					return err
				}
			} else if err := json.Unmarshal(raw, &header); err != nil {
				return err
			}
			if !header.At.Equal(at) {
				return nil
			}
			event := RecordedEvent{At: at}
			if stream == observationSchedulerStream {
				var row struct {
					Action *SchedulerActionObservation `json:"action"`
				}
				if err := json.Unmarshal(raw, &row); err != nil {
					return err
				}
				if row.Action == nil {
					return nil
				}
				event.Action = row.Action
			} else {
				if err := json.Unmarshal(raw, &event.Cluster); err != nil {
					return err
				}
			}
			if len(events) == limit {
				return &serving.Error{Code: "IncompleteObservation", Message: "simultaneous events exceed the configured event bound"}
			}
			events = append(events, event)
			return nil
		})
		if err != nil {
			return nil, "", coverage, err
		}
	}
	coverage = read.coverage(map[string][2]time.Time{observationSchedulerStream: {from, to}, observationClusterStream: {from, to}})
	// Unlike source extents, these bounds describe the attempted append scan,
	// including missing archive hours.
	coverage.From, coverage.To = &from, &to
	return events, next, coverage, ctx.Err()
}
