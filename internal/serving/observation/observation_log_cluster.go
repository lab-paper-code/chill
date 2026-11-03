package observation

import (
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

func (l *ObservationLog) EventObservations(ctx context.Context, at time.Time, limit int) ([]RecordedEvent, error) {
	events := []RecordedEvent{}
	if at.IsZero() || limit <= 0 {
		return nil, &serving.Error{Code: "InvalidQuery", Message: "an event timestamp and positive event bound are required"}
	}
	// Segments follow append time, so delayed events can be in any later hour.
	bounds := [2]time.Time{at.UTC().Truncate(time.Hour), {}}
	read, err := l.prepare(ctx, map[string][2]time.Time{observationSchedulerStream: bounds, observationClusterStream: bounds})
	if err != nil {
		return nil, err
	}
	defer read.close()
	for _, stream := range []string{observationSchedulerStream, observationClusterStream} {
		err := l.scanStream(read, stream, func(raw []byte) error {
			var header struct {
				At time.Time `json:"at"`
			}
			if err := json.Unmarshal(raw, &header); err != nil {
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
			return nil, err
		}
	}
	return events, ctx.Err()
}
