package serving

import (
	"errors"
	"math"
	"strings"
	"time"
)

// RequestRow is one request in the requests stream. A final row supersedes the
// assigned row of the same request. Stage offsets count from ReceivedAt; an
// unreached stage stays empty.
type RequestRow struct {
	Kind              string         `json:"kind"`
	RequestID         string         `json:"requestID"`
	WorkloadUID       string         `json:"workloadUID,omitempty"`
	DefinitionID      string         `json:"definitionID,omitempty"`
	Node              *NodeReference `json:"node,omitempty"`
	Instance          string         `json:"instance,omitempty"`
	ProfileID         string         `json:"profileID,omitempty"`
	DecisionID        string         `json:"decisionID,omitempty"`
	Outcome           string         `json:"outcome,omitempty"`
	Error             string         `json:"error,omitempty"`
	ExecutionComplete bool           `json:"executionComplete,omitempty"`
	ClientSpanNanos   int64          `json:"clientSpanNanos,omitempty"`
	ReceivedAt        time.Time      `json:"receivedAt"`
	LifetimeNanos     int64          `json:"lifetimeNanos,omitempty"`
	DispatchedAt      *time.Time     `json:"dispatchedAt,omitempty"`
	ElapsedNanos      *int64         `json:"elapsedNanos,omitempty"`
	ValidatedNanos    *int64         `json:"validatedNanos,omitempty"`
	AcceptedNanos     *int64         `json:"acceptedNanos,omitempty"`
	DispatchedNanos   *int64         `json:"dispatchedNanos,omitempty"`
	QueuedNanos       *int64         `json:"queuedNanos,omitempty"`
	RunningNanos      *int64         `json:"runningNanos,omitempty"`
	ExecutedNanos     *int64         `json:"executedNanos,omitempty"`
	ExchangeNanos     *int64         `json:"exchangeNanos,omitempty"`
	BatchID           uint64         `json:"batchID,omitempty"`
	// Native timestamps are direct worker-clock progress, never reconstructed
	// from QueueMS (which also includes ingress and is not actor queue wait).
	NativeQueuedAt          *time.Time `json:"nativeQueuedAt,omitempty"`
	NativeRunningAt         *time.Time `json:"nativeRunningAt,omitempty"`
	NativeFinishedAt        *time.Time `json:"nativeFinishedAt,omitempty"`
	NativeExecutionComplete bool       `json:"nativeExecutionComplete,omitempty"`
	NativeOutcome           string     `json:"nativeOutcome,omitempty"`
	NativeError             string     `json:"nativeError,omitempty"`
	BatchSize               int        `json:"batchSize,omitempty"`
	QueueMS                 float64    `json:"queueMS,omitempty"`
	ServiceMS               float64    `json:"serviceMS,omitempty"`
	BackendMS               float64    `json:"backendMS,omitempty"`
	PreprocessMS            float64    `json:"preprocessMS,omitempty"`
	PostprocessMS           float64    `json:"postprocessMS,omitempty"`
	FirstTokenMS            float64    `json:"firstTokenMS,omitempty"`
	Units                   int        `json:"units,omitempty"`
	NativeElapsedMS         *float64   `json:"nativeElapsedMS,omitempty"`
	profile                 *Profile
	// finished is the latest native finished record; closing fills missing stages from it.
	finished *BackendProgress
	// assignedHour holds this request's assigned row; it stays local until the final row.
	assignedHour time.Time
	// closing marks a settled request whose final row still awaits a successful append.
	closing bool
}

const (
	requestRowAssigned = "assigned"
	requestRowFinal    = "final"
	// RequestRowNative supplements a settled HTTP row with late direct evidence.
	RequestRowNative = "native"
)

func (r *RequestRow) fold(e *RequestEvent) {
	r.foldIdentity(e)
	r.foldTiming(e)
}

func (r *RequestRow) foldIdentity(e *RequestEvent) {
	if e.WorkloadUID != "" {
		r.WorkloadUID = e.WorkloadUID
	}
	if e.Definition != "" {
		r.DefinitionID = e.Definition
	}
	if a := e.Assignment; a != nil {
		node, dispatched := a.Node, a.DispatchedAt
		r.Node, r.Instance, r.ProfileID, r.DispatchedAt = &node, a.Instance, a.Profile, &dispatched
		if a.ProfileBody != nil {
			r.profile = a.ProfileBody
		}
	}
}

func (r *RequestRow) foldTiming(e *RequestEvent) {
	if !e.ReceivedAt.IsZero() {
		r.ReceivedAt = e.ReceivedAt
	} else if r.ReceivedAt.IsZero() && e.Kind == phaseReceived {
		r.ReceivedAt = e.At
	}
	if e.LifetimeNanos > 0 {
		r.LifetimeNanos = e.LifetimeNanos
	}
	if e.ClientSpanNanos != 0 {
		r.ClientSpanNanos = e.ClientSpanNanos
	}
	if e.NativeInstance != "" {
		r.Instance = e.NativeInstance
	}
	r.ExecutionComplete = r.ExecutionComplete || e.ExecutionComplete
	if p := e.NativeProgress; p != nil {
		r.foldNative(p)
	}
	offset := e.ElapsedNanos
	if offset == 0 && !r.ReceivedAt.IsZero() {
		offset = e.At.Sub(r.ReceivedAt).Nanoseconds()
	}
	var stage **int64
	switch e.Kind {
	case "validated":
		stage = &r.ValidatedNanos
	case phaseAccepted:
		stage = &r.AcceptedNanos
	case phaseAssigned:
		stage = &r.DispatchedNanos
	case "backend_" + phaseQueued:
		stage = &r.QueuedNanos
	case "backend_" + phaseRunning:
		stage = &r.RunningNanos
	case "backend_" + phaseFinished:
		stage = &r.ExecutedNanos
	case eventBackendExchangeFinished:
		stage = &r.ExchangeNanos
	case eventResponseFinished:
		stage = &r.ElapsedNanos
	}
	if stage != nil && (*stage == nil || e.Kind == eventResponseFinished) {
		*stage = &offset
	}
	if e.Kind == eventResponseFinished {
		r.Outcome = e.Outcome
	}
	if e.Error != "" && (r.Error == "" || e.Kind == eventResponseFinished) {
		r.Error = e.Error
	}
}

func (r *RequestRow) foldNative(p *BackendProgress) {
	if !p.At.IsZero() {
		at := p.At
		switch p.Phase {
		case phaseQueued:
			r.NativeQueuedAt = &at
		case phaseRunning:
			r.NativeRunningAt = &at
		case phaseFinished:
			r.NativeFinishedAt = &at
		}
	}
	r.BatchID, r.BatchSize, r.QueueMS, r.ServiceMS, r.BackendMS = p.BatchID, p.BatchSize, p.QueueMS, p.ServiceMS, p.BackendMS
	r.PreprocessMS, r.PostprocessMS, r.FirstTokenMS, r.Units = p.PreprocessMS, p.PostprocessMS, p.FirstTokenMS, p.Units
	native := p.NativeElapsedMS
	r.NativeElapsedMS = &native
	if p.DecisionID != "" {
		r.DecisionID = p.DecisionID
	}
	if p.Phase == phaseFinished {
		r.NativeExecutionComplete = p.ExecutionComplete
		r.NativeOutcome, r.NativeError = p.Outcome, p.Error
		r.finished = p
	}
}

// fillNativeStages fills the stages no progress line folded before the call
// settled. The exchange can settle a fast call before its lines arrive; the
// finished record of a batch that ran dates the same worker-clock instants:
// firstfloor receipt = At - NativeElapsedMS, running = receipt + QueueMS.
func (r *RequestRow) fillNativeStages() {
	p := r.finished
	if p == nil || p.BatchID == 0 || p.At.IsZero() || r.ReceivedAt.IsZero() {
		return
	}
	received := p.At.Add(-time.Duration(math.Round(p.NativeElapsedMS * float64(time.Millisecond))))
	queued := received.Sub(r.ReceivedAt).Nanoseconds()
	running := received.Add(time.Duration(math.Round(p.QueueMS * float64(time.Millisecond)))).Sub(r.ReceivedAt).Nanoseconds()
	if r.QueuedNanos == nil {
		r.QueuedNanos = &queued
	}
	if r.RunningNanos == nil {
		r.RunningNanos = &running
	}
}

func (r *RequestRow) accepted() bool {
	return r.AcceptedNanos != nil || r.DispatchedAt != nil || r.DispatchedNanos != nil
}

// RequestEvent folds one event into the request's open row. Assignment appends
// its identity; native evidence without an open row appends a supplement.
func (l *ObservationLog) RequestEvent(event RequestEvent) error {
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
		row = &RequestRow{RequestID: event.RequestID}
	}
	next := *row
	next.fold(&event)
	l.open[event.RequestID] = &next
	l.requestMu.Unlock()
	if event.Kind == phaseAssigned {
		assigned := next
		assigned.Kind = requestRowAssigned
		assigned.AcceptedNanos, assigned.ValidatedNanos, assigned.DispatchedNanos = nil, nil, nil
		l.mu.LockRequest(event.RequestID)
		defer l.mu.Unlock()
		hour, err := l.appendRowLocked(&assigned)
		if err != nil {
			return err
		}
		l.requestMu.Lock()
		if current := l.open[event.RequestID]; current != nil {
			current.assignedHour = hour
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
func (l *ObservationLog) RecordNativeSupplement(event RequestEvent) error {
	if event.RequestID == "" || event.At.IsZero() || event.NativeProgress == nil || event.NativeInstance == "" {
		return errors.New("native request identity, time, instance and progress are required")
	}
	l.mu.LockRequest(event.RequestID)
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("observation log is closed")
	}
	return l.appendNativeSupplementLocked(event)
}

func (l *ObservationLog) appendNativeSupplementLocked(event RequestEvent) error {
	// The original HTTP receipt and its legacy stage offsets are not known.
	// Keep only direct native timestamps/completion and the physical identity.
	late := RequestRow{Kind: RequestRowNative, RequestID: event.RequestID, WorkloadUID: event.WorkloadUID,
		DefinitionID: event.Definition, Instance: event.NativeInstance}
	late.foldNative(event.NativeProgress)
	_, err := l.appendRowLocked(&late)
	return err
}

// RequestClosed appends the final row of a request the Runtime has settled.
func (l *ObservationLog) RequestClosed(id string) error {
	l.mu.LockRequest(id)
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
	row.closing = true
	final := *row
	l.requestMu.Unlock()
	final.Kind, final.DispatchedAt = requestRowFinal, nil
	final.fillNativeStages()
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
func (l *ObservationLog) appendRowLocked(row *RequestRow) (time.Time, error) {
	now := l.now()
	if d := l.retained[row.DefinitionID]; d != nil {
		if err := l.referenceLocked("definition", row.DefinitionID, now, now, d); err != nil {
			return now, err
		}
	}
	if row.profile != nil && row.ProfileID != "" {
		if err := l.referenceLocked("profile", row.ProfileID, now, now, row.profile); err != nil {
			return now, err
		}
	}
	return now.Truncate(time.Hour), l.appendLocked(observationRequestStream, now, row)
}
