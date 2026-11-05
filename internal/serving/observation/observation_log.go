package observation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	serving "github.com/lab-paper-code/chill/internal/serving"
)

const (
	observationVersion           = "v1alpha5"
	observationRequestStream     = "requests"
	observationSchedulerStream   = "scheduler"
	observationManagementStream  = "management"
	observationCatalogStream     = "catalog"
	observationResourcesStream   = "resources"
	observationClusterStream     = "cluster"
	observationDefinitionsFile   = "definitions.jsonl"
	observationHourLayout        = "20060102T15"
	observationDefinitionCatalog = "definition"
	observationProfileCatalog    = "profile"
)

var observationStreams = []string{observationRequestStream, observationSchedulerStream, observationManagementStream, observationCatalogStream, observationResourcesStream, observationClusterStream}

type ObservationLogConfig struct {
	Directory   string
	Store       string
	Writer      evidencestore.StreamingImmutableWriter
	Definitions []string
	Outcomes    []string
	Retention   time.Duration
	// EventLookupHours bounds append-time hours read by each event query page.
	EventLookupHours int
	// OccupancyTail bounds receipt lookback and late native supplement delivery.
	OccupancyTail time.Duration
	Diagnostics   *serving.Diagnostics
	// OnRequestEvent runs after successful event reflection and FIFO admission.
	// It must return promptly and must not reenter the log.
	OnRequestEvent func(serving.RequestEvent)
}

type DefinitionObservation struct {
	Identity   string              `json:"identity"`
	Definition *serving.Definition `json:"definition"`
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

// ObservationLog admits immutable rows to one FIFO disk writer. Committed
// metadata and current open request rows support mixed read captures.
type ObservationLog struct {
	mu sync.Mutex
	// writerMu belongs only to the disk worker; producers never acquire it.
	writerMu serving.DiagnosticLock
	flushMu  sync.Mutex
	submitMu sync.Mutex
	wake     *sync.Cond
	// ponytail: unbounded observation backlog; sustained CephFS stalls can exhaust
	// process memory (OOM loses the unwritten tail). Add a durable spool or an
	// explicitly approved admission/backpressure limit if this ceiling is reached.
	queue             []*observationBatch
	frontier          time.Time
	failure           error
	failed            bool
	retry             bool
	fatalBoundary     bool
	failureSignal     chan struct{}
	workerDone        chan struct{}
	terminal          *observationBatch
	definitionsClosed bool
	finalQueued       map[string]*serving.RequestRow
	// references tracks FIFO-admitted dependencies, never committed reader metadata.
	references map[string]observationCatalogEntry
	// run is submission-owned; runCommitted is published under mu after I/O.
	run          observationRunState
	runCommitted observationRunState
	// requestMu protects open rows; it never covers filesystem work.
	// The disk writer may take requestMu while holding mu, never the reverse.
	requestMu sync.Mutex
	// captureMu excludes retention only until a reader has opened its paths.
	// It is always acquired before mu; appends never acquire it.
	captureMu sync.RWMutex
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
	offsets map[*os.File]int64
	// segments retain local paths and complete sizes, including rotated files.
	// Capture copies this metadata under mu and opens files outside it.
	segments        map[string]observationSegment
	definitions     *os.File
	definitionsSize int64
	retained        map[string]*serving.Definition
	open            map[string]*serving.RequestRow
	restored        []serving.RequestRow
	catalog         map[string]observationCatalogEntry
	closed          bool
	lastError       error
	syncFile        func(*os.File) error
	writeFile       func(*os.File, []byte) (int, error)
	removeAll       func(string) error
}

func NewObservationLog(config ObservationLogConfig) (*ObservationLog, error) {
	if config.Directory == "" || config.Store == "" || config.Writer == nil {
		return nil, errors.New("observation directory, store and immutable writer are required")
	}
	if config.Retention <= 0 {
		return nil, errors.New("observation retention must be positive")
	}
	if config.OccupancyTail < 0 {
		return nil, errors.New("occupancy supplement tail must not be negative")
	}
	if config.EventLookupHours <= 0 {
		config.EventLookupHours = 1
	}
	l := &ObservationLog{
		config: config, root: filepath.Join(config.Directory, observationVersion), started: time.Now().UTC(),
		now: func() time.Time { return time.Now().UTC() }, files: map[string]*os.File{}, offsets: map[*os.File]int64{},
		retained: map[string]*serving.Definition{}, open: map[string]*serving.RequestRow{}, catalog: map[string]observationCatalogEntry{},
		archived: map[time.Time]bool{},
		syncFile: (*os.File).Sync, removeAll: os.RemoveAll,
		writeFile: (*os.File).Write,
	}
	l.writerMu.Recorder, l.writerMu.ID = config.Diagnostics, 2
	l.failureSignal, l.workerDone = make(chan struct{}), make(chan struct{})
	l.wake = sync.NewCond(&l.submitMu)
	if err := l.startRun(); err != nil {
		return nil, err
	}
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
	if err := scanObservationFile(definitions, func(raw []byte) error {
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
	if err := l.indexSegments(hours); err != nil {
		return nil, errors.Join(err, l.definitions.Close())
	}
	info, err := l.definitions.Stat()
	if err != nil {
		return nil, errors.Join(err, l.definitions.Close())
	}
	l.definitionsSize = info.Size()
	l.offsets[l.definitions] = info.Size()
	go l.writeLoop()
	return l, nil
}

func (l *ObservationLog) DefinitionRecord(definition *serving.Definition) error {
	if definition == nil {
		return errors.New("definition is required")
	}
	l.config.Diagnostics.Mark(serving.DiagnosticMarshal, serving.DiagnosticBegin, "", 0)
	data, err := json.Marshal(DefinitionObservation{Identity: definition.Identity(), Definition: definition})
	l.config.Diagnostics.Mark(serving.DiagnosticMarshal, serving.DiagnosticEnd, "", 0)
	if err != nil {
		return err
	}
	l.submitMu.Lock()
	defer l.submitMu.Unlock()
	if err := l.submissionErrorLocked(); err != nil {
		return err
	}
	l.mu.Lock()
	retained := l.retained[definition.Identity()]
	l.mu.Unlock()
	if retained != nil {
		stored, marshalErr := json.Marshal(DefinitionObservation{Identity: definition.Identity(), Definition: retained})
		if marshalErr == nil && bytes.Equal(stored, data) {
			return nil
		}
	}
	l.enqueueLocked(&observationBatch{appends: []observationAppend{{data: append(data, '\n')}}})
	l.mu.Lock()
	l.retained[definition.Identity()] = definition
	l.mu.Unlock()
	return nil
}

func (l *ObservationLog) ManagementEvent(event serving.ManagementEvent) error {
	if event.WorkloadUID == "" || event.At.IsZero() {
		return errors.New("management UID and event time are required")
	}
	// Management codes are classified at the single registry boundary.
	return l.submitRecord(observationManagementStream, event, nil, event.At)
}

func (l *ObservationLog) Flush(ctx context.Context) error {
	l.flushMu.Lock()
	err := l.retryDrain(context.WithoutCancel(ctx))
	l.flushMu.Unlock()
	if err != nil {
		return err
	}
	return l.archive(ctx)
}

func (l *ObservationLog) Close(_ context.Context) error {
	l.flushMu.Lock()
	defer l.flushMu.Unlock()
	l.submitMu.Lock()
	done := make(chan error, 1)
	if l.closed {
		select {
		case <-l.workerDone:
			l.submitMu.Unlock()
			return nil
		default:
		}
		l.terminal.done = done
	} else {
		if err := l.enqueueClosingLocked(); err != nil {
			l.submitMu.Unlock()
			return err
		}
		l.closed = true
		l.terminal = &observationBatch{sync: true, terminal: true, done: done}
		l.enqueueLocked(l.terminal)
	}
	l.retry = true
	l.wake.Signal()
	l.submitMu.Unlock()
	if err := <-done; err != nil {
		return err
	}
	<-l.workerDone
	return nil
}

func (l *ObservationLog) recordError(err error) {
	if err != nil {
		l.mu.Lock()
		l.lastError = err
		l.mu.Unlock()
	}
}

// Started is when this process began observing arrivals; earlier ones may be
// missing (the entry point was down).
func (l *ObservationLog) Started() time.Time { return l.started }

func (l *ObservationLog) Health() ObservationLogHealth {
	err := l.Err()
	l.mu.Lock()
	if err == nil {
		err = l.lastError
	}
	l.mu.Unlock()
	h := ObservationLogHealth{Healthy: err == nil}
	if err != nil {
		h.Error = err.Error()
	}
	return h
}

// RuntimeFacts returns the retained definitions and the assigned requests
// without a final row that a restart must drain.
func (l *ObservationLog) RuntimeFacts() (map[string]*serving.Definition, []serving.RequestRow) {
	l.mu.Lock()
	defer l.mu.Unlock()
	definitions := make(map[string]*serving.Definition, len(l.retained))
	for id, d := range l.retained {
		definitions[id] = d
	}
	restored := l.restored
	l.restored = nil
	return definitions, restored
}

type observationAppend struct {
	stream    string
	at        time.Time
	data      []byte
	catalogID string
	catalog   *observationCatalogEntry
}

type observationBatch struct {
	appends   []observationAppend
	next      int
	final     *serving.RequestRow
	requestID string
	run       *observationRunState
	sync      bool
	terminal  bool
	done      chan error
}

func (l *ObservationLog) submissionErrorLocked() error {
	if l.failure != nil {
		return l.failure
	}
	if l.closed {
		return errors.New("observation log is closed")
	}
	return nil
}

func (l *ObservationLog) receiptLocked() time.Time {
	at := l.now().UTC()
	if at.Before(l.frontier) {
		return l.frontier
	}
	l.frontier = at
	return at
}

func (l *ObservationLog) enqueueLocked(batch *observationBatch) {
	for _, row := range batch.appends {
		if row.catalog != nil {
			if l.references == nil {
				l.references = make(map[string]observationCatalogEntry)
			}
			l.references[row.catalogID] = *row.catalog
		}
	}
	l.queue = append(l.queue, batch)
	l.wake.Signal()
}

func (l *ObservationLog) freezeBatchLocked(stream string, at time.Time, value any, refs []observationReference, eventAt time.Time) (*observationBatch, error) {
	l.config.Diagnostics.Mark(serving.DiagnosticMarshal, serving.DiagnosticBegin, "", 0)
	defer l.config.Diagnostics.Mark(serving.DiagnosticMarshal, serving.DiagnosticEnd, "", 0)
	batch := &observationBatch{}
	for _, ref := range refs {
		id := ref.kind + "/" + ref.key
		entry, known := l.references[id]
		if known && entry.hour == observationHour(at) && entry.source != nil && entry.source == ref.body {
			continue
		}
		var body []byte
		if known && entry.source != nil && entry.source == ref.body {
			body = entry.body
		} else {
			var err error
			body, err = json.Marshal(ref.body)
			if err != nil {
				return nil, err
			}
		}
		if known && entry.hour == observationHour(at) && bytes.Equal(entry.body, body) {
			continue
		}
		data, err := json.Marshal(CatalogRow{Kind: ref.kind, Key: ref.key, At: eventAt, Body: body})
		if err != nil {
			return nil, err
		}
		var source any
		switch ref.body.(type) {
		case *serving.Profile, *serving.Definition:
			source = ref.body
		}
		entry = observationCatalogEntry{hour: observationHour(at), body: body, source: source}
		batch.appends = append(batch.appends, observationAppend{stream: observationCatalogStream, at: at,
			data: append(data, '\n'), catalogID: id, catalog: &entry})
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	batch.appends = append(batch.appends, observationAppend{stream: stream, at: at, data: append(data, '\n')})
	return batch, nil
}

func (l *ObservationLog) submitRecord(stream string, value any, refs []observationReference, eventAt time.Time) error {
	l.submitMu.Lock()
	defer l.submitMu.Unlock()
	if err := l.submissionErrorLocked(); err != nil {
		return err
	}
	batch, err := l.freezeBatchLocked(stream, l.receiptLocked(), value, refs, eventAt)
	if err == nil {
		l.enqueueLocked(batch)
	}
	return err
}

// Err reports the sticky first disk-worker failure without waiting for I/O.
func (l *ObservationLog) Err() error {
	l.submitMu.Lock()
	defer l.submitMu.Unlock()
	return l.failure
}

// Failure closes once when the worker publishes its first failure.
func (l *ObservationLog) Failure() <-chan struct{} { return l.failureSignal }

func (l *ObservationLog) writeBarrier(ctx context.Context, frontier bool) (time.Time, error) {
	return l.barrier(ctx, frontier, false, false)
}

// Drain waits for a finite admitted prefix to be written and locally synced.
// Canceling the wait never removes or cancels its FIFO commands.
func (l *ObservationLog) Drain(ctx context.Context) error {
	_, err := l.barrier(ctx, false, true, false)
	return err
}

func (l *ObservationLog) retryDrain(ctx context.Context) error {
	_, err := l.barrier(ctx, true, true, true)
	return err
}

func (l *ObservationLog) barrier(ctx context.Context, frontier, sync, retry bool) (time.Time, error) {
	l.submitMu.Lock()
	if l.closed {
		at := l.frontier
		l.submitMu.Unlock()
		select {
		case <-l.workerDone:
			return at, nil
		case <-l.failureSignal:
			return at, l.Err()
		case <-ctx.Done():
			return at, ctx.Err()
		}
	}
	if l.failure != nil && !retry {
		err := l.failure
		l.submitMu.Unlock()
		return time.Time{}, err
	}
	at := l.frontier
	if frontier {
		at = l.receiptLocked()
	}
	if retry {
		if err := l.enqueueClosingLocked(); err != nil {
			l.submitMu.Unlock()
			return at, err
		}
		l.retry = true
	}
	done := make(chan error, 1)
	l.enqueueLocked(&observationBatch{sync: sync, done: done})
	l.submitMu.Unlock()
	select {
	case err := <-done:
		return at, err
	case <-ctx.Done():
		return at, ctx.Err()
	}
}

func (l *ObservationLog) writeLoop() {
	defer close(l.workerDone)
	for {
		l.submitMu.Lock()
		for len(l.queue) == 0 || l.failed && !l.retry {
			l.wake.Wait()
		}
		if l.failed {
			l.failed = false
			l.retry = false
		}
		batch := l.queue[0]
		l.submitMu.Unlock()
		err := l.writeBatch(batch)
		l.submitMu.Lock()
		if err != nil {
			if l.failure == nil {
				l.failure = err
				close(l.failureSignal)
			}
			l.failed = true
			l.retry = false
			for _, pending := range l.queue {
				if pending.done != nil {
					pending.done <- err
					pending.done = nil
				}
			}
			l.submitMu.Unlock()
			l.recordError(err)
			continue
		}
		if batch.final != nil && l.finalQueued[batch.requestID] == batch.final {
			delete(l.finalQueued, batch.requestID)
		}
		l.queue[0] = nil
		l.queue = l.queue[1:]
		if len(l.queue) == 0 {
			l.queue = nil
		}
		if batch.done != nil {
			batch.done <- nil
		}
		terminal := batch.terminal
		l.submitMu.Unlock()
		if terminal {
			return
		}
	}
}

func (l *ObservationLog) writeBatch(batch *observationBatch) error {
	l.writerMu.LockRequest(batch.requestID)
	defer l.writerMu.Unlock()
	if l.fatalBoundary {
		return errors.New("observation append boundary is unproved")
	}
	for batch.next < len(batch.appends) {
		row := &batch.appends[batch.next]
		l.mu.Lock()
		if row.catalog != nil {
			old, known := l.catalog[row.catalogID]
			if known && old.hour == row.catalog.hour && bytes.Equal(old.body, row.catalog.body) {
				l.mu.Unlock()
				batch.next++
				continue
			}
		}
		var err error
		if row.stream == "" {
			err = l.appendWholeLocked(l.definitions, row.data)
			if err == nil {
				l.definitionsSize = l.offsets[l.definitions]
			}
		} else {
			err = l.writeLocked(row.stream, row.at, row.data)
		}
		if err == nil && row.catalog != nil {
			l.catalog[row.catalogID] = *row.catalog
		}
		l.mu.Unlock()
		if err != nil {
			return err
		}
		row.data = nil
		batch.next++
	}
	if batch.run != nil {
		if err := l.writeRun(*batch.run); err != nil {
			return err
		}
		l.mu.Lock()
		l.runCommitted = *batch.run
		l.mu.Unlock()
	}
	if batch.final != nil {
		l.requestMu.Lock()
		if l.open[batch.requestID] == batch.final {
			delete(l.open, batch.requestID)
		}
		l.requestMu.Unlock()
	}
	if batch.sync {
		for _, file := range l.files {
			if err := l.syncFileOutsideLock(file); err != nil {
				return err
			}
		}
		if err := l.syncFileOutsideLock(l.definitions); err != nil {
			return err
		}
		batch.sync = false
	}
	if batch.terminal {
		for stream, file := range l.files {
			if err := file.Close(); err != nil {
				return err
			}
			delete(l.files, stream)
			delete(l.offsets, file)
		}
		if !l.definitionsClosed {
			if err := l.definitions.Close(); err != nil {
				return err
			}
			l.definitionsClosed = true
		}
		clean := l.run
		clean.Clean = true
		if err := l.writeRun(clean); err != nil {
			return err
		}
		l.mu.Lock()
		l.runCommitted = clean
		l.mu.Unlock()
	}
	return nil
}
