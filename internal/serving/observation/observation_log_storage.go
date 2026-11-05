package observation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
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
	rows := map[string]*serving.RequestRow{}
	for _, hour := range hours {
		files, err := l.hourFiles(hour, observationRequestStream)
		if err != nil {
			return err
		}
		for _, file := range files {
			if err := scanObservationFile(file.path, func(raw []byte) error {
				var row serving.RequestRow
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
		if row != nil && row.Kind == serving.RequestRowAssigned {
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

func restoreRequestRow(rows map[string]*serving.RequestRow, row *serving.RequestRow, hour time.Time) {
	if row.Kind == serving.RequestRowNative {
		return // Physical supplements cannot replace HTTP assignments.
	}
	if row.Kind == serving.RequestRowFinal {
		rows[row.RequestID] = nil
		return
	}
	if previous, exists := rows[row.RequestID]; !exists || previous != nil {
		row.AssignedHour = hour
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

func (l *ObservationLog) writeLocked(stream string, now time.Time, data []byte) error {
	name := l.segment(stream, now)
	file := l.files[stream]
	if file == nil || file.Name() != name {
		// writerMu keeps append/rotation order while mu remains available to
		// request folding and committed-prefix capture.
		l.mu.Unlock()
		next, err := l.rotateFile(file, name)
		l.mu.Lock()
		if err != nil {
			return err
		}
		if file != nil {
			delete(l.offsets, file)
		}
		file = next
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
	l.mu.Unlock()
	if !known {
		l.writerMu.Mark(serving.DiagnosticFileStat, serving.DiagnosticBegin)
		info, err := file.Stat()
		l.writerMu.Mark(serving.DiagnosticFileStat, serving.DiagnosticEnd)
		if err != nil {
			l.mu.Lock()
			return err
		}
		size = info.Size()
	}
	l.writerMu.Mark(serving.DiagnosticFileWrite, serving.DiagnosticBegin)
	n, err := l.writeFile(file, data)
	l.writerMu.Mark(serving.DiagnosticFileWrite, serving.DiagnosticEnd)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	var truncateErr error
	if err != nil {
		l.writerMu.Mark(serving.DiagnosticFileTruncate, serving.DiagnosticBegin)
		truncateErr = file.Truncate(size)
		l.writerMu.Mark(serving.DiagnosticFileTruncate, serving.DiagnosticEnd)
	}
	l.mu.Lock()
	l.offsets[file] = size
	if truncateErr != nil {
		l.fatalBoundary = true
	}
	if err != nil {
		return errors.Join(err, truncateErr)
	}
	l.offsets[file] = size + int64(n)
	return nil
}

func (l *ObservationLog) syncFileOutsideLock(file *os.File) error {
	l.config.Diagnostics.Mark(serving.DiagnosticFileSync, serving.DiagnosticBegin, "", 0)
	err := l.syncFile(file)
	l.config.Diagnostics.Mark(serving.DiagnosticFileSync, serving.DiagnosticEnd, "", 0)
	return err
}

// rotateFile runs under writerMu only. Readers retain committed segment sizes
// until the first append publishes the replacement file.
func (l *ObservationLog) rotateFile(previous *os.File, name string) (*os.File, error) {
	if previous != nil {
		if err := l.syncFileOutsideLock(previous); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return nil, err
	}
	l.config.Diagnostics.Mark(serving.DiagnosticFileOpen, serving.DiagnosticBegin, "", 0)
	file, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	l.config.Diagnostics.Mark(serving.DiagnosticFileOpen, serving.DiagnosticEnd, "", 0)
	if err != nil {
		return nil, err
	}
	if previous != nil {
		l.config.Diagnostics.Mark(serving.DiagnosticFileClose, serving.DiagnosticBegin, "", 0)
		err = previous.Close()
		l.config.Diagnostics.Mark(serving.DiagnosticFileClose, serving.DiagnosticEnd, "", 0)
		if err != nil {
			l.fatalBoundary = true
			return nil, errors.Join(err, file.Close())
		}
	}
	return file, nil
}
