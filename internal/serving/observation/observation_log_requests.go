package observation

import (
	"errors"
	"strings"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// RequestEvent folds one event into the request's open row. Assignment appends
// its identity; native evidence without an open row appends a supplement.
func (l *ObservationLog) RequestEvent(event serving.RequestEvent) error {
	if event.RequestID == "" || event.At.IsZero() {
		return errors.New("request identity and event time are required")
	}
	l.requestMu.Lock()
	if l.closed {
		l.requestMu.Unlock()
		return errors.New("observation log is closed")
	}
	row := l.open[event.RequestID]
	if row == nil {
		if strings.HasPrefix(event.Kind, "backend_") {
			l.requestMu.Unlock()
			if event.NativeProgress == nil || event.NativeInstance == "" {
				return nil
			}
			return l.RecordNativeSupplement(event)
		}
		row = &serving.RequestRow{RequestID: event.RequestID}
	}
	next := *row
	next.Fold(&event)
	l.open[event.RequestID] = &next
	l.requestMu.Unlock()
	if event.Kind == "assigned" {
		assigned := next
		assigned.Kind = serving.RequestRowAssigned
		assigned.AcceptedNanos, assigned.ValidatedNanos, assigned.DispatchedNanos = nil, nil, nil
		l.writerMu.LockRequest(event.RequestID)
		defer l.writerMu.Unlock()
		l.mu.Lock()
		defer l.mu.Unlock()
		hour, err := l.appendRowLocked(&assigned)
		if err != nil {
			return err
		}
		l.requestMu.Lock()
		if current := l.open[event.RequestID]; current != nil {
			current.AssignedHour = hour
		}
		l.requestMu.Unlock()
	}
	if l.config.OnRequestEvent != nil {
		l.config.OnRequestEvent(event)
	}
	return nil
}

// RecordNativeSupplement retains progress for a request absent from Runtime.
// It never mutates an HTTP row, even if that row still awaits final append.
func (l *ObservationLog) RecordNativeSupplement(event serving.RequestEvent) error {
	if event.RequestID == "" || event.At.IsZero() || event.NativeProgress == nil || event.NativeInstance == "" {
		return errors.New("native request identity, time, instance and progress are required")
	}
	l.writerMu.LockRequest(event.RequestID)
	defer l.writerMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("observation log is closed")
	}
	return l.appendNativeSupplementLocked(event)
}

func (l *ObservationLog) appendNativeSupplementLocked(event serving.RequestEvent) error {
	// The original HTTP receipt and its legacy stage offsets are not known.
	// Keep only direct native timestamps/completion and the physical identity.
	late := serving.RequestRow{Kind: serving.RequestRowNative, RequestID: event.RequestID, WorkloadUID: event.WorkloadUID,
		DefinitionID: event.Definition, Instance: event.NativeInstance}
	late.FoldNative(event.NativeProgress)
	_, err := l.appendRowLocked(&late)
	return err
}

// RequestClosed appends the final row of a request the Runtime has settled.
func (l *ObservationLog) RequestClosed(id string) error {
	l.writerMu.LockRequest(id)
	defer l.writerMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closeRowLocked(id)
}

// closeRowLocked keeps the open row until its final row is stored; Flush
// retries a failed one.
func (l *ObservationLog) closeRowLocked(id string) error {
	l.requestMu.Lock()
	row := l.open[id]
	if row == nil {
		l.requestMu.Unlock()
		return nil
	}
	row.Closing = true
	final := *row
	l.requestMu.Unlock()
	final.Kind, final.DispatchedAt = serving.RequestRowFinal, nil
	final.FillNativeStages()
	if _, err := l.appendRowLocked(&final); err != nil {
		return err
	}
	l.requestMu.Lock()
	if l.open[id] == row {
		delete(l.open, id)
	}
	l.requestMu.Unlock()
	return nil
}

// appendRowLocked returns the hour that holds the row.
func (l *ObservationLog) appendRowLocked(row *serving.RequestRow) (time.Time, error) {
	now := l.now()
	if d := l.retained[row.DefinitionID]; d != nil {
		if err := l.referenceLocked("definition", row.DefinitionID, now, now, d); err != nil {
			return now, err
		}
	}
	if row.ProfileBody != nil && row.ProfileID != "" {
		if err := l.referenceLocked("profile", row.ProfileID, now, now, row.ProfileBody); err != nil {
			return now, err
		}
	}
	return now.Truncate(time.Hour), l.appendLocked(observationRequestStream, now, row)
}
