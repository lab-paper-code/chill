package observation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
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
	l.writerMu.Lock()
	defer l.writerMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendLocked(observationClusterStream, l.appendTimeLocked(), row)
}

// RecordedEvent is the durable source for an event-lane recovery read. It never
// hydrates scheduler snapshots, measurement profiles or unrelated catalog rows.
type RecordedEvent struct {
	At      time.Time                   `json:"at"`
	Action  *SchedulerActionObservation `json:"action,omitempty"`
	Cluster *ClusterObservation         `json:"cluster,omitempty"`
}

// EventObservations reads a bounded append-time/event-count page. Continuations
// recover additional simultaneous facts and events appended in later hours.
func (l *ObservationLog) EventObservations(ctx context.Context, at time.Time, limit int, cursor string) ([]RecordedEvent, string, serving.ObservationCoverage, error) {
	return l.eventObservationPage(ctx, at, at.Add(time.Nanosecond), limit, cursor)
}

func (l *ObservationLog) eventObservationPage(ctx context.Context, eventFrom, eventTo time.Time, limit int, cursor string) ([]RecordedEvent, string, serving.ObservationCoverage, error) {
	coverage := serving.ObservationCoverage{Sources: []serving.ObservationSource{}, Gaps: []serving.ObservationGap{}}
	from := eventFrom.UTC().Truncate(time.Hour)
	now := l.now().UTC()
	if eventFrom.IsZero() || limit <= 0 {
		return nil, "", coverage, &serving.Error{Code: "InvalidQuery", Message: "an event timestamp and positive event bound are required"}
	}
	from, offsets, err := eventAppendCursor(from, now, cursor)
	if err != nil {
		return nil, "", coverage, err
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
	scanTo := to
	to = minTime(to, read.now)
	var scanner observationScanner
	for hour := from; hour.Before(scanTo); hour = hour.Add(time.Hour) {
		var consumed [2]int // cluster, scheduler; independent append positions.
		for streamIndex, stream := range []string{observationSchedulerStream, observationClusterStream} {
			index := 1 - streamIndex
			for _, source := range read.sources[stream] {
				if !source.hour.Equal(hour) {
					continue
				}
				err := read.scanSource(stream, source, &scanner, func(raw []byte) error {
					if consumed[index] < offsets[index] {
						consumed[index]++
						return nil
					}
					at, match, err := eventHeader(raw, stream, eventFrom, eventTo)
					if err != nil {
						return err
					}
					if !match {
						consumed[index]++
						return nil
					}
					if len(events) == limit {
						next = hour.Format(time.RFC3339Nano) + "/" + strconv.Itoa(max(consumed[0], offsets[0])) + "/" + strconv.Itoa(max(consumed[1], offsets[1]))
						return errObservationStop
					}
					consumed[index]++
					var event RecordedEvent
					if stream == observationSchedulerStream {
						err = json.Unmarshal(raw, &event)
					} else {
						err = json.Unmarshal(raw, &event.Cluster)
					}
					if err != nil {
						return err
					}
					event.At = at.UTC()
					events = append(events, event)
					return nil
				})
				if errors.Is(err, errObservationStop) {
					to = minTime(hour.Add(time.Hour), to)
					goto complete
				}
				if err != nil {
					return nil, "", coverage, err
				}
			}
			if offsets[index] > consumed[index] {
				return nil, "", coverage, &serving.Error{Code: "InvalidQuery", Message: "event cursor exceeds rows in its append stream"}
			}
		}
		offsets = [2]int{}
	}
complete:
	coverage = read.coverage(map[string][2]time.Time{observationSchedulerStream: {from, to}, observationClusterStream: {from, to}})
	// Unlike source extents, these bounds describe the attempted append scan,
	// including missing archive hours.
	coverage.From, coverage.To = &from, &to
	return events, next, coverage, ctx.Err()
}

// Stored scheduler rows lead with at. Avoid traversing large unrelated bodies;
// other field orders (including cluster rows) retain normal JSON decoding.
func observationTimestamp(raw []byte) (time.Time, error) {
	var header struct {
		At time.Time `json:"at"`
	}
	if bytes.HasPrefix(raw, []byte(`{"at":`)) {
		end := bytes.IndexByte(raw, ',')
		if end < 0 {
			end = len(raw) - 1
		}
		err := header.At.UnmarshalJSON(raw[len(`{"at":`):max(len(`{"at":`), end)])
		return header.At, err
	}
	err := json.Unmarshal(raw, &header)
	return header.At, err
}

func eventAppendCursor(from, now time.Time, cursor string) (time.Time, [2]int, error) {
	var offsets [2]int
	if cursor == "" {
		return from, offsets, nil
	}
	parts := strings.Split(cursor, "/")
	next, err := time.Parse(time.RFC3339Nano, parts[0])
	valid := err == nil && !next.Before(from) && !next.After(now) && next.Equal(next.Truncate(time.Hour))
	if len(parts) == 3 {
		for i, rows := range parts[1:] {
			offsets[i], err = strconv.Atoi(rows)
			valid = valid && err == nil && offsets[i] >= 0 && rows == strconv.Itoa(offsets[i])
		}
	} else {
		valid = valid && len(parts) == 1
	}
	if !valid {
		return from, offsets, &serving.Error{Code: "InvalidQuery", Message: "an append-hour event cursor with optional cluster/scheduler row offsets is required"}
	}
	return next.UTC(), offsets, nil
}

func eventHeader(raw []byte, stream string, from, to time.Time) (time.Time, bool, error) {
	at, err := observationTimestamp(raw)
	if err != nil || !serving.ObservationInInterval(at, from, to) {
		return at, false, err
	}
	if stream == observationSchedulerStream {
		var header struct{ Action *struct{} }
		err := json.Unmarshal(raw, &header)
		return at, header.Action != nil, err
	}
	return at, true, nil
}
