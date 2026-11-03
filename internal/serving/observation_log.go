package serving

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
	// OccupancyTail bounds receipt lookback and late native supplement delivery.
	OccupancyTail time.Duration
	Diagnostics   *Diagnostics
	// OnRequestEvent runs after successful event reflection and assignment storage.
	// It must return promptly and must not reenter the log.
	OnRequestEvent func(RequestEvent)
}

type DefinitionObservation struct {
	Identity   string      `json:"identity"`
	Definition *Definition `json:"definition"`
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
	segments    map[string]observationSegment
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
	if config.OccupancyTail < 0 {
		return nil, errors.New("occupancy supplement tail must not be negative")
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
	if err := l.indexSegments(hours); err != nil {
		return nil, errors.Join(err, l.definitions.Close())
	}
	return l, nil
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

func (l *ObservationLog) ManagementEvent(event ManagementEvent) error {
	if event.WorkloadUID == "" || event.At.IsZero() {
		return errors.New("management UID and event time are required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// Management codes are classified at the single registry boundary.
	return l.appendLocked(observationManagementStream, l.now(), event)
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
