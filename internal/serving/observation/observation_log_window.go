package observation

import (
	"encoding/json"
	"io"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

type windowTimingKey struct{}

func (r *observationRead) decodeWindow(raw []byte, value any) error {
	if r.timing == nil {
		return json.Unmarshal(raw, value)
	}
	started := time.Now()
	err := json.Unmarshal(raw, value)
	r.decodeTime += time.Since(started)
	return err
}

// Records are immutable decoded versions of complete committed append boundaries.
// windowMu never nests a producer/writer lock or runs a visitor. Open requests
// still come from the same mixed capture as every other observation reader.
type windowRecord struct {
	end     int64
	request *serving.RequestRow
	client  *clientObservationLine
	err     error
}

type windowSegment struct {
	at      time.Time
	size    int64
	records []windowRecord
}

func (r *observationRead) decodeWindowRecord(stream string, raw []byte) windowRecord {
	var record windowRecord
	if stream == observationRequestStream {
		record.request = &serving.RequestRow{}
		record.err = r.decodeWindow(raw, record.request)
	} else {
		record.client = &clientObservationLine{}
		record.err = r.decodeWindow(raw, record.client)
	}
	return record
}

// commitWindowAppend runs after a successful whole append, before its barrier
// can complete. Uncached old segments are restored from the log on first use.
func (l *ObservationLog) commitWindowAppend(row *observationAppend, size int64) {
	if row.stream != observationRequestStream && row.stream != observationClientStream {
		return
	}
	l.windowMu.Lock()
	defer l.windowMu.Unlock()
	if l.windowSegments == nil {
		return
	}
	name := l.segment(row.stream, row.at)
	segment := l.windowSegments[name]
	if segment == nil {
		l.pruneWindowLocked(row.at)
		if size != int64(len(row.data)) {
			return
		}
		segment = &windowSegment{at: row.at.Truncate(time.Minute)}
		l.windowSegments[name] = segment
	}
	if size <= segment.size {
		return
	} // A cold capture may already contain it.
	record := (&observationRead{}).decodeWindowRecord(row.stream, row.data)
	record.end = size
	segment.records = append(segment.records, record)
	segment.size = size
}

func (l *ObservationLog) retainWindow(from time.Time, occupancy bool, now time.Time) {
	l.windowMu.Lock()
	defer l.windowMu.Unlock()
	if l.windowSegments == nil {
		l.windowSegments = map[string]*windowSegment{}
	}
	span := min(max(now.Sub(from), 0), l.config.Retention)
	if occupancy {
		l.occupancySpan = span
	} else {
		l.windowSpan = span
	}
	l.pruneWindowLocked(now)
}

// Prune on writer rotation too: disabling the consumers must not turn this
// rolling window into an ever-growing second copy of the observation log.
func (l *ObservationLog) pruneWindowLocked(now time.Time) {
	floor := now.Add(-max(l.windowSpan, l.occupancySpan)).Truncate(time.Minute)
	for name, segment := range l.windowSegments {
		if segment.at.Before(floor) {
			delete(l.windowSegments, name)
		}
	}
}

func (l *ObservationLog) windowFile(read *observationRead, stream string, file observationOpenFile) ([]windowRecord, error) {
	l.windowMu.Lock()
	defer l.windowMu.Unlock()
	if l.windowSegments == nil {
		l.windowSegments = map[string]*windowSegment{}
	}
	segment := l.windowSegments[file.file.Name()]
	if segment == nil {
		segment = &windowSegment{at: file.at}
	}
	if segment.size < file.size {
		if _, err := file.file.Seek(segment.size, io.SeekStart); err != nil {
			return nil, err
		}
		var scanner observationScanner
		end := segment.size
		records := segment.records
		started, decodeBefore := time.Now(), read.decodeTime
		err := scanner.scan(observationContextReader{ctx: read.ctx, source: file.file}, file.size-segment.size, func(raw []byte) error {
			record := read.decodeWindowRecord(stream, raw)
			end += int64(len(raw)) + 1
			record.end = end
			records = append(records, record)
			return record.err
		})
		if read.timing != nil {
			read.scanTime += time.Since(started) - (read.decodeTime - decodeBefore)
		}
		if err != nil {
			return nil, err
		}
		segment.records, segment.size = records, file.size
		l.windowSegments[file.file.Name()] = segment
	}
	// The writer may have advanced beyond this read's frozen complete prefix.
	records := segment.records
	for len(records) > 0 && records[len(records)-1].end > file.size {
		records = records[:len(records)-1]
	}
	return records, nil
}

func (l *ObservationLog) scanWindowRows(read *observationRead, stream string, visit func(windowRecord) error) error {
	var scanner observationScanner
	for _, source := range read.sources[stream] {
		read.scanningHour = source.hour
		if source.object != nil {
			started, decodeBefore := time.Now(), read.decodeTime
			var visiting time.Duration
			err := read.scanSource(stream, source, &scanner, func(raw []byte) error {
				record := read.decodeWindowRecord(stream, raw)
				if record.err != nil {
					return record.err
				}
				if read.timing == nil {
					return visit(record)
				}
				at := time.Now()
				err := visit(record)
				visiting += time.Since(at)
				return err
			})
			if read.timing != nil {
				read.scanTime += time.Since(started) - (read.decodeTime - decodeBefore) - visiting
			}
			if err != nil {
				return err
			}
			continue
		}
		for _, file := range read.files[stream] {
			if !file.at.Truncate(time.Hour).Equal(source.hour) {
				continue
			}
			records, err := l.windowFile(read, stream, file)
			if err != nil {
				return err
			}
			for _, record := range records {
				if err := read.ctx.Err(); err != nil {
					return err
				}
				if record.err != nil {
					return record.err
				}
				if err := visit(record); err != nil {
					return err
				}
			}
		}
	}
	return read.ctx.Err()
}

func (l *ObservationLog) windowRequests(read *observationRead, nativeRows map[string]serving.RequestRow, from, to time.Time) ([]*serving.RequestRow, error) {
	read.definitionsOnly = true
	rows := map[string]*serving.RequestRow{}
	err := l.scanWindowRows(read, observationRequestStream, func(record windowRecord) error {
		row := record.request
		if row.Kind == serving.RequestRowNative {
			previousRow := rows[row.RequestID]
			if previousRow != nil && serving.ObservationInInterval(previousRow.ReceivedAt, from, to) && (row.WorkloadUID == "" || row.WorkloadUID == previousRow.WorkloadUID) {
				native := *row
				previous := nativeRows[row.RequestID]
				if native.NativeFinishedAt != nil {
					native.ServerTiming.Merge(previous.ServerTiming)
					nativeRows[row.RequestID] = native
				} else {
					previous.ServerTiming.Merge(native.ServerTiming)
					nativeRows[row.RequestID] = previous
				}
			}
			return nil
		}
		if row.Kind == serving.RequestRowFinal || rows[row.RequestID] == nil {
			rows[row.RequestID] = row
		}
		selected := rows[row.RequestID]
		if selected.DefinitionID != "" && read.definitions[selected.DefinitionID] == nil {
			if err := l.readCatalog(read, read.scanningHour); err != nil {
				return err
			}
			if read.definitions[selected.DefinitionID] == nil {
				return &serving.Error{Code: "IncompleteObservation", Message: "catalog has no definition " + selected.DefinitionID}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for id, row := range read.open {
		if rows[id] == nil || rows[id].Kind != serving.RequestRowFinal {
			rows[id] = row
		}
	}
	result := make([]*serving.RequestRow, 0, len(rows))
	for _, row := range rows {
		result = append(result, row)
	}
	return result, read.ctx.Err()
}
