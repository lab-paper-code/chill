package serving

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

var errObservationRecordIdentity = errors.New("invalid observation record identity")

// jsoniter's skipped-number fast path is not a complete syntax validator.
// Keep the standard syntax boundary without materializing ignored payloads.
func observationJSONSyntaxError(raw []byte) error {
	if json.Valid(raw) {
		return nil
	}
	var invalid json.RawMessage
	return json.Unmarshal(raw, &invalid)
}

// Census bounds describe actual event times, never append-window wall times.
// They summarize only the validated/counted byte prefix.
type observationTimeBounds struct {
	min, max time.Time
	set      bool
}

func (b *observationTimeBounds) add(at time.Time) {
	if !b.set || at.Before(b.min) {
		b.min = at
	}
	if !b.set || at.After(b.max) {
		b.max = at
	}
	b.set = true
}

func (b observationTimeBounds) intersects(from, to time.Time) bool {
	return b.set && !b.max.Before(from) && b.min.Before(to)
}

type schedulerWindowCensus struct {
	prefixBytes              int64
	trusted                  bool
	schedulerAt, snapshotAt  observationTimeBounds
	sampleAt                 observationTimeBounds
	minStarted, maxEnded     time.Time
	maxBoundaryDistance      time.Duration
	hasPower, unboundedPower bool
}

func (c *schedulerWindowCensus) add(event *SchedulerObservation) {
	if event == nil {
		return
	}
	c.schedulerAt.add(event.At)
	if event.Snapshot != nil && !event.Snapshot.At.IsZero() {
		c.snapshotAt.add(event.Snapshot.At)
	}
	if power := event.Power; power != nil {
		if !c.hasPower || power.StartedAt.Before(c.minStarted) {
			c.minStarted = power.StartedAt
		}
		if !c.hasPower || power.EndedAt.After(c.maxEnded) {
			c.maxEnded = power.EndedAt
		}
		c.hasPower = true
		if event.PowerCoverage == nil {
			c.unboundedPower = true
		} else {
			c.maxBoundaryDistance = max(c.maxBoundaryDistance, event.PowerCoverage.MaximumBoundaryDistance)
		}
		for _, sample := range power.Samples {
			c.sampleAt.add(sample.ObservedAt)
		}
	}
}

type requestWindowCensus struct {
	prefixBytes                    int64
	trusted                        bool
	eventAt, receiptAt, receivedAt observationTimeBounds
	definitionPrefixBytes          int64
	definitionsTrusted             bool
	hasDefinitions                 bool
}

func (c *requestWindowCensus) add(event *RequestEvent) {
	if event == nil {
		return
	}
	at := event.At
	if event.Kind == phaseReceived {
		if !event.ReceivedAt.IsZero() {
			at = event.ReceivedAt
		}
		c.receiptAt.add(at)
	}
	c.eventAt.add(at)
	c.receivedAt.add(event.ReceivedAt)
}

// observationSnapshot pins complete append prefixes, not decoded history. All
// streams in one query share a single capture boundary; readers never hold the
// writer mutex while reading bytes, decoding records or reducing observations.
type observationSnapshot struct {
	observationLog *ObservationLog
	windows        []ObservationWindow
	streams        map[string][]observationStreamPrefix
}

type observationStreamPrefix struct {
	window        int
	path          string
	bytes         int64
	census        schedulerWindowCensus
	requestCensus requestWindowCensus
}

func (observationLog *ObservationLog) snapshot(streams ...string) (*observationSnapshot, error) {
	observationLog.mu.Lock()
	defer observationLog.mu.Unlock()
	s := &observationSnapshot{
		observationLog: observationLog, windows: append([]ObservationWindow(nil), observationLog.windows...),
		streams: make(map[string][]observationStreamPrefix, len(streams)),
	}
	for _, stream := range streams {
		prefixes := make([]observationStreamPrefix, 0, len(s.windows))
		for index, window := range s.windows {
			name := filepath.Join(observationLog.config.Directory, window.ID, stream+".jsonl")
			info, err := os.Stat(name)
			if err != nil {
				observationLog.lastError = err
				return nil, err
			}
			prefix := observationStreamPrefix{window: index, path: name, bytes: info.Size()}
			if stream == observationDefinitionStream {
				census := observationLog.requestCensus[window.ID]
				if census.definitionsTrusted && prefix.bytes == census.definitionPrefixBytes && !census.hasDefinitions {
					continue
				}
			}
			if stream == observationSchedulerStream {
				prefix.census = observationLog.schedulerCensus[window.ID]
				prefix.census.trusted = prefix.census.trusted && prefix.bytes == prefix.census.prefixBytes
			}
			if stream == observationRequestStream {
				prefix.requestCensus = observationLog.requestCensus[window.ID]
				prefix.requestCensus.trusted = prefix.requestCensus.trusted && prefix.bytes == prefix.requestCensus.prefixBytes
			}
			prefixes = append(prefixes, prefix)
		}
		s.streams[stream] = prefixes
	}
	return s, nil
}

// Keep all later facts after any possible selected receipt: frozen context and
// outcomes may arrive in later windows without ReceivedAt.
func (s *observationSnapshot) selectLoadWindow(from, to time.Time) {
	prefixes := s.streams[observationRequestStream][:0]
	keepFollowingFacts := false
	for _, prefix := range s.streams[observationRequestStream] {
		census := prefix.requestCensus
		if !census.trusted || census.receiptAt.intersects(from, to) {
			keepFollowingFacts = true
		}
		if keepFollowingFacts || census.eventAt.intersects(from, to) {
			prefixes = append(prefixes, prefix)
		}
	}
	s.streams[observationRequestStream] = prefixes
}

func (s *observationSnapshot) scan(stream string, visit func(observationRecord)) error {
	return s.scanRaw(stream, func(window string, line []byte) (bool, error) {
		var record observationRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return false, err
		}
		if record.SchemaVersion != observationSchema || record.Window != window {
			return false, errObservationRecordIdentity
		}
		visit(record)
		return false, nil
	})
}

// scanRaw preserves retained append order. A visitor may stop after witnessing
// the next page; event times need not fall in their append window's wall times.
// Visitor bytes are borrowed for that call; retained results must own their data.
func (s *observationSnapshot) scanRaw(stream string, visit func(string, []byte) (bool, error)) error {
	var buffer []byte
	for _, prefix := range s.streams[stream] {
		stop, err := s.scanPrefix(prefix, &buffer, visit)
		if err != nil {
			s.observationLog.mu.Lock()
			s.observationLog.lastError = err
			s.observationLog.mu.Unlock()
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}

func (s *observationSnapshot) scanPrefix(prefix observationStreamPrefix, buffer *[]byte, visit func(string, []byte) (bool, error)) (stop bool, result error) {
	file, err := os.Open(prefix.path)
	if err != nil {
		return false, err
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	bounded := &io.LimitedReader{R: file, N: prefix.bytes}
	scanner := bufio.NewScanner(bounded)
	// Keep the largest borrowed backing buffer across all files in this scan.
	// Retain the existing addressable record-size limit and raw newline framing.
	scanner.Buffer(*buffer, int(^uint(0)>>1))
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if cap(data) > cap(*buffer) {
			*buffer = data[:0]
		}
		if end := bytes.IndexByte(data, '\n'); end >= 0 {
			return end + 1, data[:end+1], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	window := &s.windows[prefix.window]
	first := true
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) > 0 {
			if line[len(line)-1] != '\n' {
				s.incomplete(prefix.window)
			} else if first {
				var header ObservationWindow
				if err := json.Unmarshal(line, &header); err != nil {
					return false, err
				}
				if header.SchemaVersion != observationSchema || header.ID != window.ID {
					return false, errors.New("invalid observation stream header")
				}
				first = false
			} else {
				stop, err := visit(window.ID, line)
				if err != nil || stop {
					return stop, err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	if bounded.N != 0 {
		return false, io.ErrUnexpectedEOF
	}
	return false, nil
}

func (s *observationSnapshot) incomplete(index int) {
	s.windows[index].Incomplete = true
	s.observationLog.mu.Lock()
	defer s.observationLog.mu.Unlock()
	// Windows are retained in append order, including across rotation.
	s.observationLog.windows[index].Incomplete = true
	s.observationLog.lastError = errors.New("incomplete observation JSONL record")
}
