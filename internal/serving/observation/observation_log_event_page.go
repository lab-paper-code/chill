package observation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// EventObservationWindow bounds event-time queries, not the stored retention.
const EventObservationWindow = 24 * time.Hour

type eventPosition struct {
	Hour time.Time `json:"hour"`
	Row  int       `json:"row"`
}

type eventPageKey struct {
	At     time.Time `json:"at"`
	Stream string    `json:"stream"`
	eventPosition
}

type eventPageCursor struct {
	From  time.Time                `json:"from"`
	To    time.Time                `json:"to"`
	Last  eventPageKey             `json:"last"`
	Heads map[string]eventPosition `json:"heads"`
}

type eventPageEntry struct {
	key eventPageKey
	raw json.RawMessage
}

func compareEventKey(a, b eventPageKey) int {
	if n := a.At.Compare(b.At); n != 0 {
		return n
	}
	if a.Stream < b.Stream {
		return -1
	}
	if a.Stream > b.Stream {
		return 1
	}
	if n := a.Hour.Compare(b.Hour); n != 0 {
		return n
	}
	return a.Row - b.Row
}

// EventObservationPage uses event time followed by immutable stream/hour/row
// identity. Cursor heads freeze committed prefixes, including simultaneous and
// delayed events, without retaining server-side pagination state.
func (l *ObservationLog) EventObservationPage(ctx context.Context, from, to time.Time, limit int, cursor string) ([]RecordedEvent, string, serving.ObservationCoverage, error) {
	checkpoint, err := parseEventPageCursor(from, to, limit, cursor)
	if err != nil {
		return nil, "", serving.ObservationCoverage{}, err
	}
	streams := []string{observationSchedulerStream, observationClusterStream}
	// Files follow append time: delayed facts may be committed after to.
	ranges := map[string][2]time.Time{}
	for _, stream := range streams {
		ranges[stream] = [2]time.Time{from.UTC().Truncate(time.Hour), {}}
	}
	read, err := l.prepare(ctx, ranges)
	if err != nil {
		return nil, "", serving.ObservationCoverage{}, err
	}
	defer read.close()
	entries := make([]eventPageEntry, 0, limit+1)
	for _, stream := range streams {
		position := eventPosition{}
		err = l.scanStream(read, stream, func(raw []byte) error {
			if !position.Hour.Equal(read.scanningHour) {
				position = eventPosition{Hour: read.scanningHour}
			}
			position.Row++
			if cursor != "" {
				head := checkpoint.Heads[stream]
				if position.Hour.After(head.Hour) || position.Hour.Equal(head.Hour) && position.Row > head.Row {
					return errObservationStop
				}
			}
			var header struct {
				At     time.Time       `json:"at"`
				Action json.RawMessage `json:"action"`
			}
			if err := json.Unmarshal(raw, &header); err != nil {
				return err
			}
			if !serving.ObservationInInterval(header.At, from, to) || stream == observationSchedulerStream && (len(header.Action) == 0 || string(header.Action) == "null") {
				return nil
			}
			key := eventPageKey{At: header.At, Stream: stream, eventPosition: position}
			if cursor != "" && compareEventKey(key, checkpoint.Last) <= 0 {
				return nil
			}
			index, _ := slices.BinarySearchFunc(entries, key, func(entry eventPageEntry, key eventPageKey) int { return compareEventKey(entry.key, key) })
			if index > limit {
				return nil
			}
			if len(entries) == limit+1 {
				entries = entries[:limit]
			}
			entries = slices.Insert(entries, index, eventPageEntry{key: key, raw: slices.Clone(raw)})
			return nil
		})
		if err != nil {
			return nil, "", serving.ObservationCoverage{}, err
		}
		if cursor == "" {
			checkpoint.Heads[stream] = position
		}
	}
	next := ""
	if len(entries) > limit {
		entries = entries[:limit]
		checkpoint.Last = entries[len(entries)-1].key
		raw, err := json.Marshal(checkpoint)
		if err != nil {
			return nil, "", serving.ObservationCoverage{}, err
		}
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	events := make([]RecordedEvent, 0, len(entries))
	for _, entry := range entries {
		event := RecordedEvent{At: entry.key.At}
		if entry.key.Stream == observationSchedulerStream {
			if err := json.Unmarshal(entry.raw, &event); err != nil {
				return nil, "", serving.ObservationCoverage{}, err
			}
		} else if err := json.Unmarshal(entry.raw, &event.Cluster); err != nil {
			return nil, "", serving.ObservationCoverage{}, err
		}
		events = append(events, event)
	}
	coverage := read.coverage(map[string][2]time.Time{observationSchedulerStream: {from, to}, observationClusterStream: {from, to}})
	return events, next, coverage, ctx.Err()
}

func parseEventPageCursor(from, to time.Time, limit int, cursor string) (eventPageCursor, error) {
	checkpoint := eventPageCursor{From: from, To: to, Heads: map[string]eventPosition{}}
	invalid := &serving.Error{Code: "InvalidQuery", Message: "bounded positive interval, limit and valid event cursor are required"}
	if from.IsZero() || !from.Before(to) || to.Sub(from) > EventObservationWindow || limit <= 0 || len(cursor) > 4096 {
		return checkpoint, invalid
	}
	if cursor == "" {
		return checkpoint, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || json.Unmarshal(raw, &checkpoint) != nil || !checkpoint.From.Equal(from) || !checkpoint.To.Equal(to) || len(checkpoint.Heads) != 2 || !serving.ObservationInInterval(checkpoint.Last.At, from, to) || checkpoint.Last.Row <= 0 {
		return checkpoint, invalid
	}
	if checkpoint.Last.Stream != observationSchedulerStream && checkpoint.Last.Stream != observationClusterStream {
		return checkpoint, invalid
	}
	for _, stream := range []string{observationSchedulerStream, observationClusterStream} {
		if head, ok := checkpoint.Heads[stream]; !ok || head.Row < 0 || head.Hour.IsZero() && head.Row != 0 {
			return checkpoint, invalid
		}
	}
	return checkpoint, nil
}
