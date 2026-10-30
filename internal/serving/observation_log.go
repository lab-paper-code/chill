package serving

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"gonum.org/v1/gonum/stat"
)

const (
	observationVersion          = "v1alpha5"
	observationRequestStream    = "requests"
	observationSchedulerStream  = "scheduler"
	observationManagementStream = "management"
	observationCatalogStream    = "catalog"
	observationResourcesStream  = "resources"
	observationClusterStream    = "cluster"
	observationDefinitionsFile  = "definitions.jsonl"
	observationHourLayout       = "20060102T15"
)

var observationStreams = []string{observationRequestStream, observationSchedulerStream, observationManagementStream, observationCatalogStream, observationResourcesStream, observationClusterStream}

type ObservationLogConfig struct {
	Directory   string
	Store       string
	Writer      evidencestore.ImmutableWriter
	Definitions []string
	Outcomes    []string
	Retention   time.Duration
	Diagnostics *Diagnostics
	// OnRequestEvent runs after successful event reflection and assignment storage.
	// It must return promptly and must not reenter the log.
	OnRequestEvent func(RequestEvent)
}

type DefinitionObservation struct {
	Identity   string      `json:"identity"`
	Definition *Definition `json:"definition"`
}

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

// CatalogRow holds an immutable body that other records reference by key.
type CatalogRow struct {
	Kind string          `json:"kind"`
	Key  string          `json:"key"`
	At   time.Time       `json:"at"`
	Body json.RawMessage `json:"body"`
}

type ObservationLogHealth struct {
	Healthy bool   `json:"healthy"`
	Error   string `json:"error,omitempty"`
}

type observationCatalogEntry struct {
	hour   string
	body   []byte
	source any // the published *Profile or *Definition that body encodes
}

// ObservationLog appends minute files under hourly segment directories and
// keeps only the open request rows and the definitions in memory.
type ObservationLog struct {
	mu diagnosticMutex
	// requestMu protects open rows; it never covers filesystem work.
	// The disk writer may take requestMu while holding mu, never the reverse.
	requestMu sync.Mutex
	publishMu sync.Mutex
	// archived holds the past hours whose streams this process stored, under publishMu.
	archived map[time.Time]bool
	config   ObservationLogConfig
	root     string
	started  time.Time
	// floor is the start of complete local observation; deleting an hour advances it.
	floor time.Time
	now   func() time.Time
	files map[string]*os.File
	// offsets retain the last complete append boundary of this exclusive writer.
	offsets     map[*os.File]int64
	definitions *os.File
	retained    map[string]*Definition
	open        map[string]*RequestRow
	restored    []RequestRow
	catalog     map[string]observationCatalogEntry
	closed      bool
	lastError   error
}

func NewObservationLog(config ObservationLogConfig) (*ObservationLog, error) {
	if config.Directory == "" || config.Store == "" || config.Writer == nil {
		return nil, errors.New("observation directory, store and immutable writer are required")
	}
	if config.Retention <= 0 {
		return nil, errors.New("observation retention must be positive")
	}
	l := &ObservationLog{
		config: config, root: filepath.Join(config.Directory, observationVersion), started: time.Now().UTC(),
		now: func() time.Time { return time.Now().UTC() }, files: map[string]*os.File{}, offsets: map[*os.File]int64{},
		retained: map[string]*Definition{}, open: map[string]*RequestRow{}, catalog: map[string]observationCatalogEntry{},
		archived: map[time.Time]bool{},
	}
	l.mu.recorder, l.mu.id = config.Diagnostics, 2
	if err := os.MkdirAll(l.root, 0700); err != nil {
		return nil, err
	}
	hours, err := l.hours()
	if err != nil {
		return nil, err
	}
	// A crash can leave one torn line at the end of each stream's last file.
	for _, stream := range observationStreams {
		for index := len(hours) - 1; index >= 0; index-- {
			files, err := l.hourFiles(hours[index], stream)
			if err != nil {
				return nil, err
			}
			if len(files) > 0 {
				if err := truncateTornLine(files[len(files)-1].path); err != nil {
					return nil, err
				}
				break
			}
		}
	}
	definitions := filepath.Join(l.root, observationDefinitionsFile)
	if err := truncateTornLine(definitions); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := scanObservationFile(definitions, -1, func(raw []byte) error {
		var d DefinitionObservation
		if err := json.Unmarshal(raw, &d); err != nil {
			return err
		}
		l.retained[d.Identity] = d.Definition
		return nil
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if l.definitions, err = os.OpenFile(definitions, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err != nil {
		return nil, err
	}
	if err := l.restore(hours); err != nil {
		return nil, errors.Join(err, l.definitions.Close())
	}
	return l, nil
}

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
	path string
	at   time.Time
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
	return l.appendWholeLocked(file, data)
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

func (l *ObservationLog) DefinitionRecord(definition *Definition) error {
	if definition == nil {
		return errors.New("definition is required")
	}
	l.config.Diagnostics.Mark(DiagnosticMarshal, DiagnosticBegin, "", 0)
	data, err := json.Marshal(DefinitionObservation{Identity: definition.Identity(), Definition: definition})
	l.config.Diagnostics.Mark(DiagnosticMarshal, DiagnosticEnd, "", 0)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("observation log is closed")
	}
	// A restart publishes every Workload again; the root file keeps each body once.
	if retained := l.retained[definition.Identity()]; retained != nil {
		l.mu.mark(DiagnosticMarshal, DiagnosticBegin)
		stored, err := json.Marshal(DefinitionObservation{Identity: definition.Identity(), Definition: retained})
		l.mu.mark(DiagnosticMarshal, DiagnosticEnd)
		if err == nil && bytes.Equal(stored, data) {
			return nil
		}
	}
	if err := l.appendWholeLocked(l.definitions, append(data, '\n')); err != nil {
		l.lastError = err
		return err
	}
	l.retained[definition.Identity()] = definition
	return nil
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

func (l *ObservationLog) ManagementEvent(event ManagementEvent) error {
	if event.WorkloadUID == "" || event.At.IsZero() {
		return errors.New("management UID and event time are required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// Management codes are classified at the single registry boundary.
	return l.appendLocked(observationManagementStream, l.now(), event)
}

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
	l.mu.Lock()
	now := l.now() // Later appends choose this hour or a later one.
	l.mu.Unlock()
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

// removeHour deletes one uploaded hour under the writer lock, so a capture
// opens the whole hour or sees it gone with the floor already past it.
func (l *ObservationLog) removeHour(hour time.Time) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requestMu.Lock()
	for _, row := range l.open {
		if !row.assignedHour.IsZero() && !row.assignedHour.After(hour) {
			l.requestMu.Unlock()
			return false, nil
		}
	}
	l.requestMu.Unlock()
	l.mu.mark(DiagnosticRemove, DiagnosticBegin)
	err := os.RemoveAll(filepath.Join(l.root, observationHour(hour)))
	l.mu.mark(DiagnosticRemove, DiagnosticEnd)
	if err != nil {
		return false, err
	}
	if end := hour.Add(time.Hour); end.After(l.floor) {
		l.floor = end
	}
	return true, nil
}

func (l *ObservationLog) upload(ctx context.Context, hour time.Time, stream string) error {
	if l.config.Diagnostics.Active() {
		l.config.Diagnostics.Mark(DiagnosticArchive, DiagnosticBegin, "", 0)
		defer l.config.Diagnostics.Mark(DiagnosticArchive, DiagnosticEnd, "", 0)
	}
	files, err := l.hourFiles(hour, stream)
	if err != nil || len(files) == 0 {
		return err
	}
	// The compressed hour stages on scratch space, out of process memory and
	// off the volume the archive drains.
	compressed, err := os.CreateTemp("", ".archive-*")
	if err != nil {
		return err
	}
	defer func() { _ = compressed.Close(); _ = os.Remove(compressed.Name()) }()
	l.config.Diagnostics.Mark(DiagnosticCompress, DiagnosticBegin, "", 0)
	writer := gzip.NewWriter(compressed) // Zero header time: equal input, equal bytes.
	for _, segment := range files {
		file, err := os.Open(segment.path)
		if err != nil {
			return err
		}
		_, err = io.Copy(writer, file)
		if err = errors.Join(err, file.Close()); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	l.config.Diagnostics.Mark(DiagnosticCompress, DiagnosticEnd, "", 0)
	size, err := compressed.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := compressed.Seek(0, io.SeekStart); err != nil {
		return err
	}
	object := evidencestore.ObjectIdentity{
		Store: l.config.Store, Key: observationArchivePrefix + observationHour(hour) + "/" + stream + ".jsonl.gz",
		Size: size,
	}
	l.config.Diagnostics.Mark(DiagnosticUpload, DiagnosticBegin, "", 0)
	_, err = l.config.Writer.PutImmutable(ctx, object, compressed)
	l.config.Diagnostics.Mark(DiagnosticUpload, DiagnosticEnd, "", 0)
	return err
}

func (l *ObservationLog) Flush(ctx context.Context) error {
	l.mu.Lock()
	var result error
	l.requestMu.Lock()
	var closing []string
	for id, row := range l.open {
		if row.closing && !l.closed {
			closing = append(closing, id)
		}
	}
	l.requestMu.Unlock()
	for _, id := range closing {
		result = errors.Join(result, l.closeRowLocked(id))
	}
	for _, file := range l.files {
		result = errors.Join(result, l.syncFileLocked(file))
	}
	if !l.closed {
		result = errors.Join(result, l.syncFileLocked(l.definitions))
	}
	if result != nil {
		l.lastError = result
	}
	l.mu.Unlock()
	return errors.Join(result, l.archive(ctx))
}

func (l *ObservationLog) Close(ctx context.Context) error {
	l.mu.Lock()
	var result error
	if !l.closed {
		for stream, file := range l.files {
			result = errors.Join(result, l.syncFileLocked(file), l.closeFileLocked(file))
			delete(l.files, stream)
		}
		result = errors.Join(result, l.syncFileLocked(l.definitions), l.closeFileLocked(l.definitions))
		l.requestMu.Lock()
		l.closed = true
		l.requestMu.Unlock()
	}
	if result != nil {
		l.lastError = result
	}
	l.mu.Unlock()
	return errors.Join(result, l.archive(ctx))
}

// Started is when this process began observing arrivals; earlier ones may be
// missing (the entry point was down).
func (l *ObservationLog) Started() time.Time { return l.started }

func (l *ObservationLog) Health() ObservationLogHealth {
	l.mu.Lock()
	defer l.mu.Unlock()
	h := ObservationLogHealth{Healthy: l.lastError == nil}
	if l.lastError != nil {
		h.Error = l.lastError.Error()
	}
	return h
}

type ObservationCohort struct {
	Definition              string         `json:"definition"`
	Total                   int            `json:"total"`
	Outcomes                map[string]int `json:"outcomes"`
	SampleCount             int            `json:"sampleCount"`
	LatencyNanos            []float64      `json:"latencyNanos"`
	CompletionResidualNanos []float64      `json:"completionResidualNanos"`
	Percentile              *float64       `json:"percentile,omitempty"`
	QuantileNanos           *float64       `json:"quantileNanos"`
	TargetLatencyNanos      *int64         `json:"targetLatencyNanos,omitempty"`
}

type ObservationResult struct {
	UID              string                  `json:"uid"`
	From             time.Time               `json:"from"`
	To               time.Time               `json:"to"`
	DefinitionFilter []string                `json:"definitionFilter"`
	OutcomeFilter    []string                `json:"outcomeFilter"`
	Total            int                     `json:"total"`
	Selected         int                     `json:"selected"`
	Outcomes         map[string]int          `json:"outcomes"`
	Cohorts          []ObservationCohort     `json:"cohorts"`
	Definitions      []DefinitionObservation `json:"definitions"`
	Requests         []RequestRow            `json:"requests"`
	Management       []ManagementEvent       `json:"management"`
	NextCursor       string                  `json:"nextCursor,omitempty"`
	QuantileMethod   string                  `json:"quantileMethod"`
	SamplePopulation string                  `json:"samplePopulation"`
	GapUntil         *time.Time              `json:"gapUntil,omitempty"`
	Coverage         ObservationCoverage     `json:"coverage"`
}

func observationIncludes(filter []string, value string) bool {
	return len(filter) == 0 || slices.Contains(filter, value)
}

func observationInInterval(at, from, to time.Time) bool {
	return !at.IsZero() && (from.IsZero() || !at.Before(from)) && (to.IsZero() || at.Before(to))
}

func requestOutcome(row *RequestRow) string {
	switch {
	case row.Outcome != "":
		return row.Outcome
	case row.ElapsedNanos != nil:
		return outcomeCompleted
	case row.accepted():
		return outcomeIncomplete
	default:
		return phasePending
	}
}

// Aggregate the whole selected population before projecting any row page.
func (result *ObservationResult) aggregate(ctx context.Context, definitions map[string]*Definition, rows []*RequestRow) ([]RequestRow, error) {
	cohorts := make(map[string]*ObservationCohort)
	for id, d := range definitions {
		p := d.SLO.Percentile
		target := int64(d.SLO.TargetLatency)
		cohorts[id] = &ObservationCohort{
			Definition: id, Outcomes: make(map[string]int), Percentile: &p, TargetLatencyNanos: &target,
		}
		result.Definitions = append(result.Definitions, DefinitionObservation{Identity: id, Definition: d})
	}
	selected := []RequestRow{}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !observationInInterval(row.ReceivedAt, result.From, result.To) ||
			!observationIncludes(result.DefinitionFilter, row.DefinitionID) {
			continue
		}
		result.Total++
		outcome := requestOutcome(row)
		result.Outcomes[outcome]++
		if !observationIncludes(result.OutcomeFilter, outcome) {
			continue
		}
		result.Selected++
		selected = append(selected, *row)
		cohort := cohorts[row.DefinitionID]
		if cohort == nil {
			cohort = &ObservationCohort{Definition: row.DefinitionID, Outcomes: make(map[string]int)}
			cohorts[row.DefinitionID] = cohort
		}
		cohort.Total++
		cohort.Outcomes[outcome]++
		switch outcome {
		case "delivery_failed", outcomeIncomplete, "canceled":
			continue
		}
		if row.ElapsedNanos != nil && *row.ElapsedNanos > 0 {
			elapsed := *row.ElapsedNanos
			cohort.LatencyNanos = append(cohort.LatencyNanos, float64(elapsed))
			if native := row.NativeElapsedMS; native != nil && row.DispatchedNanos != nil && row.ExecutionComplete &&
				schedulerFinite(*native) && *native >= 0 {
				cohort.CompletionResidualNanos = append(cohort.CompletionResidualNanos,
					float64(elapsed-*row.DispatchedNanos)-*native*float64(time.Millisecond))
			}
		}
	}
	for _, c := range cohorts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sort.Float64s(c.LatencyNanos)
		sort.Float64s(c.CompletionResidualNanos)
		c.SampleCount = len(c.LatencyNanos)
		if c.SampleCount > 0 && c.Percentile != nil && *c.Percentile > 0 && *c.Percentile <= 100 {
			q := stat.Quantile(*c.Percentile/100, stat.Empirical, c.LatencyNanos, nil)
			c.QuantileNanos = &q
		}
		result.Cohorts = append(result.Cohorts, *c)
	}
	sort.Slice(result.Cohorts, func(a, b int) bool { return result.Cohorts[a].Definition < result.Cohorts[b].Definition })
	sort.Slice(result.Definitions, func(a, b int) bool { return result.Definitions[a].Identity < result.Definitions[b].Identity })
	return selected, ctx.Err()
}

func (l *ObservationLog) Query(ctx context.Context, uid string, from, to time.Time, definitions, outcomes []string, limit int, cursor string) (ObservationResult, error) {
	if len(definitions) == 0 {
		definitions = l.config.Definitions
	}
	if len(outcomes) == 0 {
		outcomes = l.config.Outcomes
	}
	result := ObservationResult{
		UID: uid, From: from, To: to, DefinitionFilter: definitions, OutcomeFilter: outcomes,
		Outcomes: map[string]int{}, QuantileMethod: "gonum/stat.Empirical",
		SamplePopulation: "requests received in [from,to); final rows excluding canceled, incomplete and delivery_failed; " +
			"positive monotonic elapsed latency",
	}
	if uid == "" || (!from.IsZero() && !to.IsZero() && !from.Before(to)) || limit < 0 {
		return result, failure("InvalidQuery", "invalid observation query")
	}
	offset := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return result, failure("InvalidQuery", "invalid observation cursor")
		}
		offset = n
	}
	// Rows follow receipt, and a final row can come at any later time.
	read, err := l.prepare(ctx, map[string][2]time.Time{
		observationRequestStream: {from, {}}, observationManagementStream: {from, to},
	})
	if err != nil {
		return result, err
	}
	defer read.close()
	result.Coverage = read.coverage(map[string][2]time.Time{observationRequestStream: {from, to}, observationManagementStream: {from, to}})
	result.GapUntil = result.Coverage.gapUntil(from)
	if err := l.readCatalogs(read); err != nil {
		return result, err
	}
	byID := map[string]*Definition{}
	for id, d := range read.definitions {
		if d.UID == uid && observationIncludes(result.DefinitionFilter, id) {
			byID[id] = d
		}
	}
	rows, err := l.requestRows(read, func(row *requestEnvelope) bool {
		return row.WorkloadUID == uid && observationInInterval(row.ReceivedAt, from, to)
	})
	if err != nil {
		return result, err
	}
	selected, err := result.aggregate(ctx, byID, rows)
	if err != nil {
		return result, err
	}
	sort.SliceStable(selected, func(a, b int) bool {
		if selected[a].ReceivedAt.Equal(selected[b].ReceivedAt) {
			return selected[a].RequestID < selected[b].RequestID
		}
		return selected[a].ReceivedAt.Before(selected[b].ReceivedAt)
	})
	if offset > len(selected) {
		return result, failure("InvalidQuery", "observation cursor is out of range")
	}
	end := len(selected)
	if limit > 0 && limit < end-offset {
		end = offset + limit
		result.NextCursor = strconv.Itoa(end)
	}
	result.Requests = selected[offset:end]
	return result, l.queryManagement(read, &result)
}

func (l *ObservationLog) queryManagement(read *observationRead, result *ObservationResult) error {
	err := l.scanStream(read, observationManagementStream, func(raw []byte) error {
		var e ManagementEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		filter := result.DefinitionFilter
		if e.WorkloadUID != result.UID || !observationInInterval(e.At, result.From, result.To) || len(filter) != 0 &&
			(e.Before == nil || !observationIncludes(filter, e.Before.Identity())) &&
			(e.Target == nil || !observationIncludes(filter, e.Target.Identity())) &&
			(e.After == nil || !observationIncludes(filter, e.After.Identity())) {
			return nil
		}
		result.Management = append(result.Management, e)
		return nil
	})
	sort.SliceStable(result.Management, func(a, b int) bool {
		return result.Management[a].At.Before(result.Management[b].At)
	})
	return err
}

type requestEnvelope struct {
	Kind        string    `json:"kind"`
	RequestID   string    `json:"requestID"`
	WorkloadUID string    `json:"workloadUID"`
	ReceivedAt  time.Time `json:"receivedAt"`
}

// requestRows merges the captured rows by request: final, else the open row,
// else assigned.
func (l *ObservationLog) requestRows(read *observationRead, keep func(*requestEnvelope) bool) ([]*RequestRow, error) {
	if err := l.readCatalogs(read); err != nil {
		return nil, err
	}
	rows := map[string]*RequestRow{}
	err := l.scanStream(read, observationRequestStream, func(raw []byte) error {
		return mergeRequestRow(rows, raw, keep, read.catalog())
	})
	if err != nil {
		return nil, err
	}
	for id, row := range read.open {
		if err := read.ctx.Err(); err != nil {
			return nil, err
		}
		if (rows[id] == nil || rows[id].Kind != requestRowFinal) &&
			keep(&requestEnvelope{RequestID: id, WorkloadUID: row.WorkloadUID, ReceivedAt: row.ReceivedAt}) {
			rows[id] = row
		}
	}
	result := make([]*RequestRow, 0, len(rows))
	for _, row := range rows {
		if err := read.ctx.Err(); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, nil
}

// mergeRequestRow decodes raw once with the standard decoder. A row the
// selector skips needs only valid syntax, so a decode error counts only when
// the envelope keeps the row.
func mergeRequestRow(rows map[string]*RequestRow, raw []byte, keep func(*requestEnvelope) bool, catalog *ObservationCatalog) error {
	row := &RequestRow{}
	if decodeErr := json.Unmarshal(raw, row); decodeErr != nil {
		var envelope requestEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		if !keep(&envelope) || envelope.Kind != requestRowFinal && rows[envelope.RequestID] != nil {
			return nil
		}
		return decodeErr
	}
	if row.Kind == RequestRowNative {
		return nil // Supplemental physical evidence is not an HTTP request row.
	}
	envelope := requestEnvelope{Kind: row.Kind, RequestID: row.RequestID, WorkloadUID: row.WorkloadUID, ReceivedAt: row.ReceivedAt}
	if keep(&envelope) && (row.Kind == requestRowFinal || rows[row.RequestID] == nil) {
		if catalog != nil && row.DefinitionID != "" && len(catalog.rows["definition/"+row.DefinitionID]) == 0 {
			return failure("IncompleteObservation", "catalog has no definition %s", row.DefinitionID)
		}
		rows[row.RequestID] = row
	}
	return nil
}

// runtimeFacts returns the retained definitions and the assigned requests
// without a final row that a restart must drain.
func (l *ObservationLog) runtimeFacts() (map[string]*Definition, []RequestRow) {
	l.mu.Lock()
	defer l.mu.Unlock()
	definitions := make(map[string]*Definition, len(l.retained))
	for id, d := range l.retained {
		definitions[id] = d
	}
	restored := l.restored
	l.restored = nil
	return definitions, restored
}

// observationRead holds files opened under the writer lock; reads and
// decoding run outside it. Deleting an hour does not affect an open file.
type observationRead struct {
	files        map[string][]observationOpenFile
	open         map[string]*RequestRow
	definitions  map[string]*Definition
	start        time.Time
	ctx          context.Context
	now          time.Time
	localHours   map[time.Time]bool
	sources      map[string][]observationSource
	archive      ObservationArchive
	catalogs     map[time.Time]*ObservationCatalog
	scanningHour time.Time
}

type observationOpenFile struct {
	file *os.File
	at   time.Time
	size int64
}

func (r *observationRead) close() {
	for _, files := range r.files {
		for _, file := range files {
			_ = file.file.Close()
		}
	}
}

// capture opens each stream's minute files whose minute lies in [from, to];
// a zero to reads through the latest file.
func (l *ObservationLog) capture(ranges map[string][2]time.Time) (*observationRead, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mu.recorder.Active() {
		l.mu.mark(DiagnosticCapture, DiagnosticBegin)
		defer l.mu.mark(DiagnosticCapture, DiagnosticEnd)
	}
	read := &observationRead{
		files: map[string][]observationOpenFile{}, open: map[string]*RequestRow{}, definitions: map[string]*Definition{},
		start: l.floor, now: l.now(), ctx: context.Background(), localHours: map[time.Time]bool{},
	}
	l.requestMu.Lock()
	for id, row := range l.open {
		copied := *row
		read.open[id] = &copied
	}
	l.requestMu.Unlock()
	for id, d := range l.retained {
		read.definitions[id] = d
	}
	hours, err := l.hours()
	if err != nil {
		read.close()
		return nil, err
	}
	read.localHours[read.now.Truncate(time.Hour)] = true
	for _, hour := range hours {
		read.localHours[hour] = true
		catalogWanted := false
		for stream, bounds := range ranges {
			if stream != observationCatalogStream && observationHourWanted(hour, bounds) {
				catalogWanted = true
			}
		}
		for _, stream := range observationStreams {
			files, err := l.hourFiles(hour, stream)
			if err != nil {
				read.close()
				return nil, err
			}
			bounds, wanted := ranges[stream]
			if stream == observationCatalogStream && catalogWanted {
				bounds, wanted = [2]time.Time{hour, hour.Add(time.Hour)}, true
			}
			for _, segment := range files {
				if !wanted || segment.at.Before(bounds[0].Truncate(time.Minute)) || !bounds[1].IsZero() && segment.at.After(bounds[1]) {
					continue
				}
				if err := l.openReadFile(read, stream, segment); err != nil {
					read.close()
					return nil, err
				}
			}
		}
	}
	return read, nil
}

func (l *ObservationLog) openReadFile(r *observationRead, stream string, segment observationSegment) error {
	l.mu.mark(DiagnosticFileOpen, DiagnosticBegin)
	file, err := os.Open(segment.path)
	l.mu.mark(DiagnosticFileOpen, DiagnosticEnd)
	if err != nil {
		return err
	}
	l.mu.mark(DiagnosticFileStat, DiagnosticBegin)
	info, err := file.Stat()
	l.mu.mark(DiagnosticFileStat, DiagnosticEnd)
	if err != nil {
		return errors.Join(err, l.closeFileLocked(file))
	}
	r.files[stream] = append(r.files[stream], observationOpenFile{file: file, at: segment.at, size: info.Size()})
	return nil
}

// errObservationStop ends a scan early without an error.
var errObservationStop = errors.New("observation scan stopped")

func (l *ObservationLog) scanStream(read *observationRead, stream string, visit func([]byte) error) error {
	if l.config.Diagnostics.Active() {
		l.config.Diagnostics.Mark(DiagnosticQuery, DiagnosticBegin, "", 0)
		defer l.config.Diagnostics.Mark(DiagnosticQuery, DiagnosticEnd, "", 0)
	}
	var scanner observationScanner
	if read.sources != nil {
		for _, source := range read.sources[stream] {
			if err := read.scanSource(stream, source, &scanner, visit); err != nil {
				return observationScanError(err)
			}
		}
		return read.ctx.Err()
	}
	for _, file := range read.files[stream] {
		read.scanningHour = file.at.Truncate(time.Hour)
		err := scanner.scan(observationContextReader{ctx: read.ctx, source: file.file}, file.size, visit)
		if err != nil {
			return observationScanError(fmt.Errorf("read %s: %w", file.file.Name(), err))
		}
	}
	return read.ctx.Err()
}

func scanObservationFile(name string, size int64, visit func([]byte) error) (result error) {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	var scanner observationScanner
	return scanner.scan(file, size, visit)
}

// observationScanner keeps its read and long-line buffers across files.
type observationScanner struct {
	reader         *bufio.Reader
	long           []byte
	requireNewline bool
}

// scan visits newline-terminated lines of a captured prefix; a negative size
// reads to the end. A line still being written has no newline and is skipped.
// Visitor bytes are borrowed for that call.
func (s *observationScanner) scan(source io.Reader, size int64, visit func([]byte) error) error {
	if size >= 0 {
		source = io.LimitReader(source, size)
	}
	if s.reader == nil {
		s.reader = bufio.NewReaderSize(source, 1<<16)
	} else {
		s.reader.Reset(source)
	}
	for {
		line, err := s.reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			s.long = append(s.long[:0], line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = s.reader.ReadSlice('\n')
				s.long = append(s.long, line...)
			}
			line = s.long
		}
		if err == io.EOF {
			if s.requireNewline && len(line) != 0 {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		if err != nil {
			return err
		}
		if len(line) > 1 {
			if err := visit(line[:len(line)-1]); err != nil {
				return err
			}
		}
	}
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
