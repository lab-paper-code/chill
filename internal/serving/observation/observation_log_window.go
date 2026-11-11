package observation

import (
	"encoding/json"
	"io"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

type windowTimingKey struct{}

// Ceiling: 32,768 decoded rows across request/client segments. Larger windows
// use the exact log reader; this bounds cache retention without limiting demand.
const windowCacheMaxRecords = 32 * 1024

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
	bypass  bool
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

func (l *ObservationLog) windowAppendSizeLocked(row *observationAppend) int64 {
	if row.stream != observationRequestStream && row.stream != observationClientStream {
		return 0
	}
	return l.segments[l.segment(row.stream, row.at)].size
}

// commitWindowAppend runs after a successful whole append, before its barrier
// can complete. Uncached old segments are restored from the log on first use.
func (l *ObservationLog) commitWindowAppend(row *observationAppend, size int64) {
	if row.stream != observationRequestStream && row.stream != observationClientStream {
		return
	}
	name := l.segment(row.stream, row.at)
	l.windowMu.Lock()
	if row.at.After(l.windowNow) {
		rotated := row.at.Truncate(time.Minute).After(l.windowNow.Truncate(time.Minute))
		l.windowNow = row.at
		if rotated {
			l.pruneWindowLocked()
		}
	}
	if !l.windowCacheEligibleLocked(row.at.Truncate(time.Minute)) {
		l.windowMu.Unlock()
		return
	}
	segment := l.windowSegments[name]
	if segment != nil && (segment.bypass || size <= segment.size) {
		l.windowMu.Unlock()
		return
	}
	if segment == nil && size != int64(len(row.data)) {
		l.windowMu.Unlock()
		return
	}
	if l.windowRecords >= windowCacheMaxRecords {
		l.bypassWindowLocked(name, row.at, size)
		l.windowMu.Unlock()
		return
	}
	l.windowMu.Unlock()

	// JSON and all file work stay outside the cache lock, including the writer.
	record := (&observationRead{}).decodeWindowRecord(row.stream, row.data)
	record.end = size
	l.windowMu.Lock()
	defer l.windowMu.Unlock()
	if !l.windowCacheEligibleLocked(row.at.Truncate(time.Minute)) {
		return
	}
	segment = l.windowSegments[name]
	if segment != nil && (segment.bypass || size <= segment.size) {
		return
	}
	previous := int64(0)
	if segment != nil {
		previous = segment.size
	}
	if previous != size-int64(len(row.data)) {
		return // Keep the contiguous prefix; the next read restores the missing suffix.
	}
	if l.windowRecords >= windowCacheMaxRecords {
		l.bypassWindowLocked(name, row.at, size)
		return
	}
	if segment == nil {
		segment = &windowSegment{at: row.at.Truncate(time.Minute)}
		l.windowSegments[name] = segment
	}
	segment.records = append(segment.records, record)
	segment.size = size
	l.windowRecords++
}

func (l *ObservationLog) retainWindow(from, to time.Time, occupancy bool, now time.Time) {
	l.windowMu.Lock()
	defer l.windowMu.Unlock()
	if now.After(l.windowNow) {
		l.windowNow = now
	}
	// Historical queries neither enlarge nor enable the rolling writer cache.
	if !to.Before(now.Truncate(time.Minute)) {
		if l.windowSegments == nil {
			l.windowSegments = map[string]*windowSegment{}
		}
		span := min(max(to.Sub(from), 0), l.config.Retention)
		if occupancy {
			l.occupancySpan = span
		} else {
			l.windowSpan = span
		}
	}
	l.pruneWindowLocked()
}

func (l *ObservationLog) windowCacheEligibleLocked(at time.Time) bool {
	floor := l.windowNow.Add(-max(l.windowSpan, l.occupancySpan)).Truncate(time.Minute)
	return l.windowSegments != nil && !at.Before(floor) && !at.After(l.windowNow.Truncate(time.Minute))
}

// Prune on writer rotation too: stopped consumers cannot retain the whole log.
func (l *ObservationLog) pruneWindowLocked() {
	for name, segment := range l.windowSegments {
		if !l.windowCacheEligibleLocked(segment.at) {
			l.windowRecords -= len(segment.records)
			delete(l.windowSegments, name)
		}
	}
}

func (l *ObservationLog) bypassWindowLocked(name string, at time.Time, size int64) {
	if segment := l.windowSegments[name]; segment != nil {
		l.windowRecords -= len(segment.records)
		size = max(size, segment.size)
	}
	l.windowSegments[name] = &windowSegment{at: at.Truncate(time.Minute), size: size, bypass: true}
}

func (l *ObservationLog) publishWindowFile(file observationOpenFile, records []windowRecord, budget bool) bool {
	l.windowMu.Lock()
	defer l.windowMu.Unlock()
	if !l.windowCacheEligibleLocked(file.at) {
		return budget
	}
	current := l.windowSegments[file.file.Name()]
	if current != nil && current.bypass {
		return true
	}
	if current != nil && current.size >= file.size {
		return budget // A concurrent writer/reader already published this prefix.
	}
	previous := 0
	if current != nil {
		previous = len(current.records)
	}
	if budget || l.windowRecords-previous+len(records) > windowCacheMaxRecords {
		l.bypassWindowLocked(file.file.Name(), file.at, file.size)
		return true
	}
	l.windowRecords += len(records) - previous
	l.windowSegments[file.file.Name()] = &windowSegment{at: file.at, size: file.size, records: records}
	return false
}

func (l *ObservationLog) windowFile(read *observationRead, stream string, file observationOpenFile, visit func(windowRecord) error) error {
	l.windowMu.Lock()
	var snapshot windowSegment
	if segment := l.windowSegments[file.file.Name()]; segment != nil {
		snapshot = *segment
	}
	retain := read.cacheWindow && l.windowCacheEligibleLocked(file.at) && !snapshot.bypass
	available := windowCacheMaxRecords - l.windowRecords + len(snapshot.records)
	l.windowMu.Unlock()
	budget := snapshot.bypass
	defer func() {
		if budget && read.timing != nil {
			read.timing.WindowBudgetFallbacks++
		}
	}()
	records := snapshot.records
	for len(records) > 0 && records[len(records)-1].end > file.size {
		records = records[:len(records)-1]
	}
	var visiting time.Duration
	checked := func(record windowRecord) error {
		if err := read.ctx.Err(); err != nil {
			return err
		}
		if record.err != nil {
			return record.err
		}
		if read.timing == nil {
			return visit(record)
		}
		started := time.Now()
		err := visit(record)
		visiting += time.Since(started)
		return err
	}
	if err := func() error {
		started := time.Now()
		defer func() {
			if read.timing != nil {
				read.cacheTime += time.Since(started) - visiting
			}
		}()
		for _, record := range records {
			if err := checked(record); err != nil {
				return err
			}
		}
		return nil
	}(); err != nil {
		return err
	}
	var end int64
	if len(records) > 0 {
		end = records[len(records)-1].end
	}
	if end >= file.size {
		return nil
	}
	// Build a private complete prefix. Neither a visitor nor any I/O runs under
	// windowMu, and a bounded occupancy cold miss does not populate the cache.
	var candidate []windowRecord
	if retain {
		candidate = append(candidate, records...)
	}
	if _, err := file.file.Seek(end, io.SeekStart); err != nil {
		return err
	}
	var scanner observationScanner
	started, decodeBefore, visitingBefore := time.Now(), read.decodeTime, visiting
	err := scanner.scan(observationContextReader{ctx: read.ctx, source: file.file}, file.size-end, func(raw []byte) error {
		record := read.decodeWindowRecord(stream, raw)
		end += int64(len(raw)) + 1
		record.end = end
		if err := checked(record); err != nil {
			return err
		}
		if retain {
			if len(candidate) >= available {
				budget, retain, candidate = true, false, nil
			} else {
				candidate = append(candidate, record)
			}
		}
		return nil
	})
	if read.timing != nil {
		read.scanTime += time.Since(started) - (read.decodeTime - decodeBefore) - (visiting - visitingBefore)
	}
	if err != nil {
		return err
	}
	if read.cacheWindow && (retain || budget) {
		budget = l.publishWindowFile(file, candidate, budget)
	}
	return nil
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
			if err := l.windowFile(read, stream, file, visit); err != nil {
				return err
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
