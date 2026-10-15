package serving

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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

// observationSnapshot pins complete append prefixes, not decoded history. All
// streams in one query share a single capture boundary; readers never hold the
// writer mutex while reading bytes, decoding records or reducing observations.
type observationSnapshot struct {
	journal *Journal
	windows []ObservationWindow
	streams map[string][]observationStreamPrefix
}

type observationStreamPrefix struct {
	window int
	path   string
	bytes  int64
}

func (j *Journal) snapshot(streams ...string) (*observationSnapshot, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := &observationSnapshot{
		journal: j, windows: append([]ObservationWindow(nil), j.windows...),
		streams: make(map[string][]observationStreamPrefix, len(streams)),
	}
	for _, stream := range streams {
		prefixes := make([]observationStreamPrefix, 0, len(s.windows))
		for index, window := range s.windows {
			name := filepath.Join(j.config.Directory, window.ID, stream+".jsonl")
			info, err := os.Stat(name)
			if err != nil {
				j.lastError = err
				return nil, err
			}
			prefixes = append(prefixes, observationStreamPrefix{window: index, path: name, bytes: info.Size()})
		}
		s.streams[stream] = prefixes
	}
	return s, nil
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
			s.journal.mu.Lock()
			s.journal.lastError = err
			s.journal.mu.Unlock()
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
	s.journal.mu.Lock()
	defer s.journal.mu.Unlock()
	// Windows are retained in append order, including across rotation.
	s.journal.windows[index].Incomplete = true
	s.journal.lastError = errors.New("incomplete observation JSONL record")
}
