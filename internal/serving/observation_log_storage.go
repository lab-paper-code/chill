package serving

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// restore sets the floor of local observation and reopens assigned requests
// without a final row for RestartDrain.
func (l *ObservationLog) restore(hours []time.Time) error {
	l.floor = l.started
	if len(hours) > 0 {
		for _, stream := range observationStreams {
			files, err := l.hourFiles(hours[0], stream)
			if err != nil {
				return err
			}
			if len(files) > 0 && files[0].at.Before(l.floor) {
				l.floor = files[0].at
			}
		}
	}
	// Final tombstones retain IDs, not completed request payloads.
	rows := map[string]*RequestRow{}
	for _, hour := range hours {
		files, err := l.hourFiles(hour, observationRequestStream)
		if err != nil {
			return err
		}
		for _, file := range files {
			if err := scanObservationFile(file.path, -1, func(raw []byte) error {
				var row RequestRow
				if err := json.Unmarshal(raw, &row); err != nil {
					return err
				}
				restoreRequestRow(rows, &row, hour)
				return nil
			}); err != nil {
				return err
			}
		}
	}
	for _, row := range rows {
		if row != nil && row.Kind == requestRowAssigned {
			l.restored = append(l.restored, *row)
			open := *row
			open.Kind = ""
			if row.DispatchedAt != nil && !row.ReceivedAt.IsZero() {
				dispatched := row.DispatchedAt.Sub(row.ReceivedAt).Nanoseconds()
				open.DispatchedNanos = &dispatched
			}
			l.open[row.RequestID] = &open
		}
	}
	return nil
}

func restoreRequestRow(rows map[string]*RequestRow, row *RequestRow, hour time.Time) {
	if row.Kind == RequestRowNative {
		return // Physical supplements cannot replace HTTP assignments.
	}
	if row.Kind == requestRowFinal {
		rows[row.RequestID] = nil
		return
	}
	if previous, exists := rows[row.RequestID]; !exists || previous != nil {
		row.assignedHour = hour
		rows[row.RequestID] = row
	}
}

func truncateTornLine(name string) (err error) {
	file, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	var tail [4096]byte
	for end := info.Size(); end > 0; {
		start := max(int64(0), end-int64(len(tail)))
		data := tail[:end-start]
		if _, err := file.ReadAt(data, start); err != nil {
			return err
		}
		if index := bytes.LastIndexByte(data, '\n'); index >= 0 {
			if start+int64(index)+1 == info.Size() {
				return nil
			}
			return file.Truncate(start + int64(index) + 1)
		}
		end = start
	}
	return file.Truncate(0)
}

func observationHour(at time.Time) string { return at.UTC().Format(observationHourLayout) }

func (l *ObservationLog) segment(stream string, at time.Time) string {
	return filepath.Join(l.root, observationHour(at), stream+"."+at.UTC().Format("04")+".jsonl")
}

// hours lists retained hour directories, oldest first.
func (l *ObservationLog) hours() ([]time.Time, error) {
	entries, err := os.ReadDir(l.root)
	if err != nil {
		return nil, err
	}
	hours := []time.Time{}
	for _, entry := range entries {
		if hour, err := time.Parse(observationHourLayout, entry.Name()); err == nil && entry.IsDir() {
			hours = append(hours, hour)
		}
	}
	sort.Slice(hours, func(a, b int) bool { return hours[a].Before(hours[b]) })
	return hours, nil
}

type observationSegment struct {
	path   string
	at     time.Time
	stream string
	size   int64
}

// hourFiles lists one stream's minute files of an hour in minute order.
func (l *ObservationLog) hourFiles(hour time.Time, stream string) ([]observationSegment, error) {
	entries, err := os.ReadDir(filepath.Join(l.root, observationHour(hour)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []observationSegment
	for _, entry := range entries {
		minute, found := strings.CutPrefix(entry.Name(), stream+".")
		minute, jsonl := strings.CutSuffix(minute, ".jsonl")
		if m, err := strconv.Atoi(minute); found && jsonl && err == nil && len(minute) == 2 && m < 60 {
			files = append(files, observationSegment{
				path: filepath.Join(l.root, observationHour(hour), entry.Name()), at: hour.Add(time.Duration(m) * time.Minute),
			})
		}
	}
	sort.Slice(files, func(a, b int) bool { return files[a].at.Before(files[b].at) })
	return files, nil
}

// indexSegments runs only at startup, after torn tails have been repaired.
func (l *ObservationLog) indexSegments(hours []time.Time) error {
	l.segments = map[string]observationSegment{}
	for _, hour := range hours {
		for _, stream := range observationStreams {
			files, err := l.hourFiles(hour, stream)
			if err != nil {
				return err
			}
			for _, segment := range files {
				info, err := os.Stat(segment.path)
				if err != nil {
					return err
				}
				segment.stream, segment.size = stream, info.Size()
				l.segments[segment.path] = segment
			}
		}
	}
	return nil
}

func (l *ObservationLog) appendLocked(stream string, now time.Time, value any) error {
	if l.closed {
		return errors.New("observation log is closed")
	}
	l.mu.mark(DiagnosticMarshal, DiagnosticBegin)
	data, err := json.Marshal(value)
	l.mu.mark(DiagnosticMarshal, DiagnosticEnd)
	if err == nil {
		err = l.writeLocked(stream, now, append(data, '\n'))
	}
	if err != nil {
		l.lastError = err
	}
	return err
}

func (l *ObservationLog) writeLocked(stream string, now time.Time, data []byte) error {
	name := l.segment(stream, now)
	file := l.files[stream]
	if file == nil || file.Name() != name {
		if file != nil {
			delete(l.files, stream)
			if err := l.closeFileLocked(file); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		var err error
		l.mu.mark(DiagnosticFileOpen, DiagnosticBegin)
		file, err = os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		l.mu.mark(DiagnosticFileOpen, DiagnosticEnd)
		if err != nil {
			return err
		}
		l.files[stream] = file
	}
	err := l.appendWholeLocked(file, data)
	if size, known := l.offsets[file]; known {
		l.segments[name] = observationSegment{path: name, at: now.UTC().Truncate(time.Minute), stream: stream, size: size}
	}
	return err
}

// appendWholeLocked appends data or nothing: a short or failed write, such as
// ENOSPC while S3 is down, is cut back so the next line starts on a boundary.
func (l *ObservationLog) appendWholeLocked(file *os.File, data []byte) error {
	size, known := l.offsets[file]
	if !known {
		l.mu.mark(DiagnosticFileStat, DiagnosticBegin)
		info, err := file.Stat()
		l.mu.mark(DiagnosticFileStat, DiagnosticEnd)
		if err != nil {
			return err
		}
		size = info.Size()
		l.offsets[file] = size
	}
	l.mu.mark(DiagnosticFileWrite, DiagnosticBegin)
	n, err := file.Write(data)
	l.mu.mark(DiagnosticFileWrite, DiagnosticEnd)
	if err != nil {
		l.mu.mark(DiagnosticFileTruncate, DiagnosticBegin)
		truncateErr := file.Truncate(size)
		l.mu.mark(DiagnosticFileTruncate, DiagnosticEnd)
		return errors.Join(err, truncateErr)
	}
	l.offsets[file] = size + int64(n)
	return nil
}

// referenceLocked writes a catalog row when the hour first references key, or
// when its body differs from the last body this process wrote for it.
// Published Profiles and Definitions are immutable, so the same pointer
// within the hour skips the marshal.
func (l *ObservationLog) referenceLocked(kind, key string, at, now time.Time, body any) error {
	var source any
	switch body.(type) {
	case *Profile, *Definition:
		source = body
	}
	id, hour := kind+"/"+key, observationHour(now)
	entry, ok := l.catalog[id]
	if ok && entry.hour == hour && source != nil && entry.source == source {
		return nil
	}
	l.mu.mark(DiagnosticMarshal, DiagnosticBegin)
	data, err := json.Marshal(body)
	l.mu.mark(DiagnosticMarshal, DiagnosticEnd)
	if err != nil {
		return err
	}
	if ok && entry.hour == hour && bytes.Equal(entry.body, data) {
		entry.source = source
		l.catalog[id] = entry
		return nil
	}
	if err := l.appendLocked(observationCatalogStream, now, CatalogRow{Kind: kind, Key: key, At: at, Body: data}); err != nil {
		return err
	}
	l.catalog[id] = observationCatalogEntry{hour: hour, body: data, source: source}
	return nil
}

func (l *ObservationLog) syncFileLocked(file *os.File) error {
	l.mu.mark(DiagnosticFileSync, DiagnosticBegin)
	err := file.Sync()
	l.mu.mark(DiagnosticFileSync, DiagnosticEnd)
	return err
}

func (l *ObservationLog) closeFileLocked(file *os.File) error {
	l.mu.mark(DiagnosticFileClose, DiagnosticBegin)
	err := file.Close()
	l.mu.mark(DiagnosticFileClose, DiagnosticEnd)
	delete(l.offsets, file)
	return err
}
