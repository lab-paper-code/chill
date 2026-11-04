package observation

import (
	"path/filepath"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

const observationArchivePrefix = "serving/observations." + observationVersion + "/"

// MaintenanceInterval is the existing local maintenance and sealed-hour scan cadence.
const MaintenanceInterval = time.Minute

// removeHour excludes capture opens, but never holds an append lock over removal.
// maintenance has already crossed the append frontier, so this hour cannot change.
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
