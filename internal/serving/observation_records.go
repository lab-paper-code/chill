package serving

import (
	"math"
	"time"
)

// RequestRow is one request in the requests stream. A final row supersedes the
// assigned row of the same request. Stage offsets count from ReceivedAt; an
// unreached stage stays empty.
type RequestRow struct {
	Kind               string         `json:"kind"`
	RequestID          string         `json:"requestID"`
	WorkloadUID        string         `json:"workloadUID,omitempty"`
	DefinitionID       string         `json:"definitionID,omitempty"`
	Node               *NodeReference `json:"node,omitempty"`
	Instance           string         `json:"instance,omitempty"`
	ProfileID          string         `json:"profileID,omitempty"`
	DecisionID         string         `json:"decisionID,omitempty"`
	Outcome            string         `json:"outcome,omitempty"`
	Error              string         `json:"error,omitempty"`
	ExecutionComplete  bool           `json:"executionComplete,omitempty"`
	ClientSpanNanos    int64          `json:"clientSpanNanos,omitempty"`
	ReceivedAt         time.Time      `json:"receivedAt"`
	LifetimeNanos      int64          `json:"lifetimeNanos,omitempty"`
	CompletionMarginMS *float64       `json:"completionMarginMS,omitempty"`
	DispatchedAt       *time.Time     `json:"dispatchedAt,omitempty"`
	ElapsedNanos       *int64         `json:"elapsedNanos,omitempty"`
	ValidatedNanos     *int64         `json:"validatedNanos,omitempty"`
	AcceptedNanos      *int64         `json:"acceptedNanos,omitempty"`
	DispatchedNanos    *int64         `json:"dispatchedNanos,omitempty"`
	QueuedNanos        *int64         `json:"queuedNanos,omitempty"`
	RunningNanos       *int64         `json:"runningNanos,omitempty"`
	ExecutedNanos      *int64         `json:"executedNanos,omitempty"`
	ExchangeNanos      *int64         `json:"exchangeNanos,omitempty"`
	ServerTiming       RequestTimings `json:"serverTiming,omitempty"`
	BatchID            uint64         `json:"batchID,omitempty"`
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
	// Canonical runner-exchange spans share the native monotonic clock.
	// A missing preceding idle is unknown/startup, never an observed zero.
	ServiceDurationNanos int64    `json:"serviceDurationNanos,omitempty"`
	PrecedingIdleNanos   *int64   `json:"precedingIdleNanos,omitempty"`
	NativeTrialID        string   `json:"nativeTrialID,omitempty"`
	ProfileBody          *Profile `json:"-"`
	// finished is the latest native finished record; closing fills missing stages from it.
	finished *BackendProgress
	// AssignedHour holds this request's assigned row; it stays local until the final row.
	AssignedHour time.Time `json:"-"`
	// Closing marks a settled request whose final row still awaits a successful append.
	Closing bool `json:"-"`
}

const (
	RequestRowAssigned = "assigned"
	RequestRowFinal    = "final"
	// RequestRowNative supplements a settled HTTP row with late direct evidence.
	RequestRowNative = "native"
)

func (r *RequestRow) Fold(e *RequestEvent) {
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
	if e.CompletionMarginMS != nil {
		margin := *e.CompletionMarginMS
		r.CompletionMarginMS = &margin
	}
	if a := e.Assignment; a != nil {
		node, dispatched := a.Node, a.DispatchedAt
		r.Node, r.Instance, r.ProfileID, r.DispatchedAt = &node, a.Instance, a.Profile, &dispatched
		if a.ProfileBody != nil {
			r.ProfileBody = a.ProfileBody
		}
	}
}

func (r *RequestRow) foldTiming(e *RequestEvent) {
	r.ServerTiming.Merge(e.ServerTiming)
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
		r.FoldNative(p)
	}
	offset := e.ElapsedNanos
	if assigned, ok := e.ServerTiming.Elapsed(ServerAssigned); e.Kind == phaseAssigned && ok {
		offset = int64(assigned)
	} else if offset == 0 && !r.ReceivedAt.IsZero() {
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

func (r *RequestRow) FoldNative(p *BackendProgress) {
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
	if sample := p.ServiceSample; sample != nil {
		r.ServiceDurationNanos, r.PrecedingIdleNanos = sample.ServiceDurationNanos, sample.PrecedingIdleNanos
		r.NativeTrialID = sample.TrialID
	}
	native := p.NativeElapsedMS
	r.NativeElapsedMS = &native
	if p.DecisionID != "" {
		r.DecisionID = p.DecisionID
	}
	if p.Phase == phaseFinished {
		r.NativeExecutionComplete = p.ExecutionComplete
		r.NativeOutcome, r.NativeError = p.Outcome, p.Error
		finished := *p
		r.finished = &finished
	}
}

// FillNativeStages fills the stages no progress line folded before the call
// settled. The exchange can settle a fast call before its lines arrive; the
// finished record of a batch that ran dates the same worker-clock instants:
// firstfloor receipt = At - NativeElapsedMS, running = receipt + QueueMS.
func (r *RequestRow) FillNativeStages() {
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

func (r *RequestRow) Accepted() bool {
	return r.AcceptedNanos != nil || r.DispatchedAt != nil || r.DispatchedNanos != nil
}

// ObservationCoverage reports the available extent and unavailable stream hours.
// A missing archive stream is unknown, not an observed zero.
type ObservationCoverage struct {
	From    *time.Time          `json:"from"`
	To      *time.Time          `json:"to"`
	Sources []ObservationSource `json:"sources"`
	Gaps    []ObservationGap    `json:"gaps"`
}

type ObservationSource struct {
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Stream string    `json:"stream"`
	Source string    `json:"source"`
	Store  string    `json:"store,omitempty"`
	Key    string    `json:"key,omitempty"`
}

type ObservationGap struct {
	From        time.Time      `json:"from"`
	To          time.Time      `json:"to"`
	Stream      string         `json:"stream"`
	Reason      string         `json:"reason"`
	Boot        string         `json:"boot,omitempty"`
	Instance    string         `json:"instance,omitempty"`
	PodUID      string         `json:"podUID,omitempty"`
	ContainerID string         `json:"containerID,omitempty"`
	Node        *NodeReference `json:"node,omitempty"`
	ClearAt     *time.Time     `json:"clearAt,omitempty"`
	Open        bool           `json:"open,omitempty"`
}

func (c ObservationCoverage) GapUntil(from time.Time) *time.Time {
	if from.IsZero() {
		return nil
	}
	ends := map[string]time.Time{}
	for _, gap := range c.Gaps {
		at, exists := ends[gap.Stream]
		if !exists {
			at = from
		}
		if !gap.From.After(at) && gap.To.After(at) {
			ends[gap.Stream] = gap.To
		}
	}
	var until *time.Time
	for _, at := range ends {
		if until == nil || at.After(*until) {
			value := at
			until = &value
		}
	}
	return until
}

func ObservationInInterval(at, from, to time.Time) bool {
	return !at.IsZero() && (from.IsZero() || !at.Before(from)) && (to.IsZero() || at.Before(to))
}

func OccupancyRowInWindow(row *RequestRow, from, to time.Time) bool {
	if row.NativeRunningAt != nil && ObservationInInterval(*row.NativeRunningAt, from, to) {
		return true
	}
	return !row.ReceivedAt.IsZero() && ObservationInInterval(row.ReceivedAt, from, to)
}

func LaterTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

const ObservationUnavailableCode = "ObservationUnavailable"

const (
	RequestRowOpen              = "occupancy-open"
	RequestRowOccupancySelected = "occupancy-selected"
)
