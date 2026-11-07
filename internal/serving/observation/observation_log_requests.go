package observation

import (
	"errors"
	"strings"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// RequestEvent reflects an event and admits its immutable assignment bytes.
// The submission lock linearizes folds with final admission, never disk I/O.
func (l *ObservationLog) RequestEvent(event serving.RequestEvent) error {
	if event.RequestID == "" || event.At.IsZero() {
		return errors.New("request identity and event time are required")
	}
	l.submitMu.Lock()
	if err := l.submissionErrorLocked(); err != nil {
		// Physical completion remains observable in memory after disk failure.
		if !l.closed && strings.HasPrefix(event.Kind, "backend_") {
			l.requestMu.Lock()
			if row := l.open[event.RequestID]; row != nil {
				next := *row
				next.Fold(&event)
				l.open[event.RequestID] = &next
			}
			l.requestMu.Unlock()
		}
		l.submitMu.Unlock()
		return err
	}
	l.requestMu.Lock()
	row := l.open[event.RequestID]
	if row == nil && strings.HasPrefix(event.Kind, "backend_") {
		l.requestMu.Unlock()
		var err error
		if event.NativeProgress != nil && event.NativeInstance != "" {
			err = l.enqueueNativeLocked(event)
		}
		l.submitMu.Unlock()
		return err
	}
	if row == nil {
		row = &serving.RequestRow{RequestID: event.RequestID}
	}
	next := *row
	next.Fold(&event)
	l.open[event.RequestID] = &next
	l.requestMu.Unlock()
	if event.Kind == serving.RequestRowAssigned {
		at := l.receiptLocked()
		assigned := next
		assigned.Kind = serving.RequestRowAssigned
		assigned.AcceptedNanos, assigned.ValidatedNanos, assigned.DispatchedNanos = nil, nil, nil
		batch, err := l.rowBatchLocked(&assigned, at)
		if err != nil {
			l.submitMu.Unlock()
			return err
		}
		l.requestMu.Lock()
		next.AssignedHour = at.Truncate(time.Hour)
		l.requestMu.Unlock()
		l.enqueueLocked(batch)
	}
	l.submitMu.Unlock()
	if l.config.OnRequestEvent != nil {
		l.config.OnRequestEvent(event)
	}
	return nil
}

// RecordNativeSupplement never manufactures an HTTP assignment or final.
func (l *ObservationLog) RecordNativeSupplement(event serving.RequestEvent) error {
	if event.RequestID == "" || event.At.IsZero() || event.NativeProgress == nil || event.NativeInstance == "" {
		return errors.New("native request identity, time, instance and progress are required")
	}
	l.submitMu.Lock()
	defer l.submitMu.Unlock()
	if err := l.submissionErrorLocked(); err != nil {
		return err
	}
	return l.enqueueNativeLocked(event)
}

func (l *ObservationLog) enqueueNativeLocked(event serving.RequestEvent) error {
	late := serving.RequestRow{Kind: serving.RequestRowNative, RequestID: event.RequestID, WorkloadUID: event.WorkloadUID,
		DefinitionID: event.Definition, Instance: event.NativeInstance, ServerTiming: event.ServerTiming}
	late.FoldNative(event.NativeProgress)
	batch, err := l.rowBatchLocked(&late, l.receiptLocked())
	if err == nil {
		l.enqueueLocked(batch)
	}
	return err
}

// RequestClosed retains the exact admitted version until the worker commits it.
func (l *ObservationLog) RequestClosed(id string, timing serving.RequestTimings) error {
	l.submitMu.Lock()
	defer l.submitMu.Unlock()
	if err := l.submissionErrorLocked(); err != nil {
		if !l.closed {
			l.requestMu.Lock()
			if row := l.open[id]; row != nil && l.finalQueued[id] != row {
				row = l.mergeCloseTimingLocked(row, timing)
				row.Closing = true
			}
			l.requestMu.Unlock()
		}
		return err
	}
	return l.enqueueFinalLocked(id, timing)
}

func (l *ObservationLog) enqueueFinalLocked(id string, timing serving.RequestTimings) error {
	l.requestMu.Lock()
	row := l.open[id]
	if row == nil || l.finalQueued[id] == row {
		l.requestMu.Unlock()
		return nil
	}
	row = l.mergeCloseTimingLocked(row, timing)
	row.Closing = true
	final := *row
	l.requestMu.Unlock()
	final.Kind, final.DispatchedAt = serving.RequestRowFinal, nil
	final.FillNativeStages()
	batch, err := l.rowBatchLocked(&final, l.receiptLocked())
	if err != nil {
		return err
	}
	batch.final, batch.requestID = row, id
	if l.finalQueued == nil {
		l.finalQueued = make(map[string]*serving.RequestRow)
	}
	l.finalQueued[id] = row
	l.enqueueLocked(batch)
	return nil
}

// mergeCloseTimingLocked requires requestMu and keeps captured row versions intact.
func (l *ObservationLog) mergeCloseTimingLocked(row *serving.RequestRow, timing serving.RequestTimings) *serving.RequestRow {
	if timing.IsZero() {
		return row
	}
	merged := row.ServerTiming
	merged.Merge(timing)
	if merged == row.ServerTiming {
		return row
	}
	next := *row
	next.ServerTiming = merged
	l.open[row.RequestID] = &next
	return &next
}

func (l *ObservationLog) enqueueClosingLocked() error {
	l.requestMu.Lock()
	var closing []string
	for id, row := range l.open {
		if row.Closing {
			closing = append(closing, id)
		}
	}
	l.requestMu.Unlock()
	for _, id := range closing {
		if err := l.enqueueFinalLocked(id, serving.RequestTimings{}); err != nil {
			return err
		}
	}
	return nil
}

func (l *ObservationLog) rowBatchLocked(row *serving.RequestRow, at time.Time) (*observationBatch, error) {
	var refs []observationReference
	l.mu.Lock()
	definition := l.retained[row.DefinitionID]
	l.mu.Unlock()
	if definition != nil {
		refs = append(refs, observationReference{kind: observationDefinitionCatalog, key: row.DefinitionID, body: definition})
	}
	if row.ProfileBody != nil && row.ProfileID != "" {
		refs = append(refs, observationReference{kind: observationProfileCatalog, key: row.ProfileID, body: row.ProfileBody})
	}
	batch, err := l.freezeBatchLocked(observationRequestStream, at, row, refs, at)
	if err == nil {
		batch.requestID = row.RequestID
	}
	return batch, err
}
