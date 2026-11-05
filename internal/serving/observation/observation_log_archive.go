package observation

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	serving "github.com/lab-paper-code/chill/internal/serving"
)

const observationArchivePrefix = "serving/observations." + observationVersion + "/"

// archive uploads each past hour as one gzip object per stream. Equal input
// makes equal bytes, so a repeated upload is Existing. A past hour no longer
// changes, so an hour whose streams all succeeded is marked and not sent
// again; after a partial failure every stream of the hour is sent again. An
// hour is deleted once it is marked and its start is past the retention period.
// The oldest hour holding the assigned row of an unsettled request, and every later
// hour, stay for RestartDrain.
func (l *ObservationLog) archive(ctx context.Context) error {
	l.publishMu.Lock()
	defer l.publishMu.Unlock()
	// Seal the captured append frontier only after its entire FIFO prefix is
	// written. A stalled old-hour batch must not be uploaded or retained away.
	now, err := l.barrier(ctx, true, false, true)
	if err != nil {
		return err
	}
	deleting := true
	hours, err := l.hours()
	if err != nil {
		return err
	}
	current := now.Truncate(time.Hour)
	var result error
	for _, hour := range hours {
		if !hour.Before(current) {
			continue
		}
		var failed error
		if !l.archived[hour] {
			for _, stream := range observationStreams {
				failed = errors.Join(failed, l.upload(ctx, hour, stream))
			}
			l.archived[hour] = failed == nil
		}
		deleting = deleting && failed == nil && !now.Before(hour.Add(l.config.Retention))
		if deleting {
			deleting, failed = l.removeHour(hour)
		}
		result = errors.Join(result, failed)
	}
	return result
}

// removeHour excludes capture opens, but never holds an append lock over removal.
// archive has already crossed the append frontier, so this hour cannot change.
func (l *ObservationLog) removeHour(hour time.Time) (bool, error) {
	l.captureMu.Lock()
	defer l.captureMu.Unlock()
	l.mu.Lock()
	// No new assignment can select this already-past append hour.
	l.requestMu.Lock()
	for _, row := range l.open {
		if !row.AssignedHour.IsZero() && !row.AssignedHour.After(hour) {
			l.requestMu.Unlock()
			l.mu.Unlock()
			return false, nil
		}
	}
	l.requestMu.Unlock()
	l.mu.Unlock()
	l.config.Diagnostics.Mark(serving.DiagnosticRemove, serving.DiagnosticBegin, "", 0)
	err := l.removeAll(filepath.Join(l.root, observationHour(hour)))
	l.config.Diagnostics.Mark(serving.DiagnosticRemove, serving.DiagnosticEnd, "", 0)
	if err != nil {
		return false, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for path, segment := range l.segments {
		if segment.at.Truncate(time.Hour).Equal(hour) {
			delete(l.segments, path)
		}
	}
	if end := hour.Add(time.Hour); end.After(l.floor) {
		l.floor = end
	}
	return true, nil
}

func (l *ObservationLog) upload(ctx context.Context, hour time.Time, stream string) error {
	if l.config.Diagnostics.Active() {
		l.config.Diagnostics.Mark(serving.DiagnosticArchive, serving.DiagnosticBegin, "", 0)
		defer l.config.Diagnostics.Mark(serving.DiagnosticArchive, serving.DiagnosticEnd, "", 0)
	}
	files, err := l.hourFiles(hour, stream)
	if err != nil || len(files) == 0 {
		return err
	}
	location := evidencestore.ObjectLocation{
		Store: l.config.Store, Key: observationArchivePrefix + observationHour(hour) + "/" + stream + ".jsonl.gz",
	}
	l.config.Diagnostics.Mark(serving.DiagnosticUpload, serving.DiagnosticBegin, "", 0)
	_, err = l.config.Writer.StreamImmutable(ctx, location, func(output io.Writer) error {
		l.config.Diagnostics.Mark(serving.DiagnosticCompress, serving.DiagnosticBegin, "", 0)
		defer l.config.Diagnostics.Mark(serving.DiagnosticCompress, serving.DiagnosticEnd, "", 0)
		writer := gzip.NewWriter(output) // Zero header time: equal input, equal bytes.
		for _, segment := range files {
			if err := ctx.Err(); err != nil {
				return err
			}
			file, err := os.Open(segment.path)
			if err != nil {
				return err
			}
			_, err = io.Copy(writer, file)
			if err = errors.Join(err, file.Close()); err != nil {
				return err
			}
		}
		return writer.Close()
	})
	l.config.Diagnostics.Mark(serving.DiagnosticUpload, serving.DiagnosticEnd, "", 0)
	return err
}
