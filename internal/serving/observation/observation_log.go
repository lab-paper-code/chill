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

	serving "github.com/lab-paper-code/chill/internal/serving"
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
	Archive     ObservationArchive
	Definitions []string
	Outcomes    []string
	Retention   time.Duration
	// EventLookupHours bounds append-time hours read by each event query page.
	EventLookupHours int
	// OccupancyTail bounds receipt lookback and late native supplement delivery.
	OccupancyTail time.Duration
	Diagnostics   *serving.Diagnostics
	// OnRequestEvent runs after successful event reflection and assignment storage.
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

// ObservationLog appends minute files under hourly segment directories and
// keeps only the open request rows and the definitions in memory.
type ObservationLog struct {
	mu sync.Mutex
	// writerMu orders synchronous appends and rotation. Maintenance never holds
	// it during sync, removal or archive I/O. Acquire it before mu.
	writerMu serving.DiagnosticLock
	// flushMu serializes Flush/Close durability barriers, not request writes.
	flushMu sync.Mutex
	// requestMu protects open rows; it never covers filesystem work.
	// The disk writer may take requestMu while holding mu, never the reverse.
	requestMu sync.Mutex
	// captureMu excludes retention only until a reader has opened its paths.
	// It is always acquired before mu; appends never acquire it.
	captureMu sync.RWMutex
	// appendFloor fences completed hours before maintenance releases append locks.
	appendFloor time.Time
	lastAppend  time.Time
	config      ObservationLogConfig
	root        string
	started     time.Time
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
	retained    map[string]*serving.Definition
	open        map[string]*serving.RequestRow
	restored    []serving.RequestRow
	catalog     map[string]observationCatalogEntry
	closed      bool
	lastError   error
	syncFile    func(*os.File) error
	writeFile   func(*os.File, []byte) (int, error)
	removeAll   func(string) error
}

func NewObservationLog(config ObservationLogConfig) (*ObservationLog, error) {
	if config.Directory == "" || config.Store == "" {
		return nil, errors.New("observation directory and store are required")
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
		syncFile: (*os.File).Sync, removeAll: os.RemoveAll,
		writeFile: (*os.File).Write,
	}
	l.writerMu.Recorder, l.writerMu.ID = config.Diagnostics, 2
	if err := os.MkdirAll(l.root, 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(config.Directory, "archive-control"), 0700); err != nil {
		return nil, err
	}
	hours, err := l.hours()
	if err != nil {
		return nil, err
	}
	sealed := map[time.Time]bool{}
	for _, hour := range hours {
		seal, err := readObservationSeal(l.root, hour)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := checkSealedInputs(l.root, seal, false); err != nil {
			return nil, err
		}
		sealed[hour] = true
		if end := hour.Add(time.Hour); end.After(l.appendFloor) {
			l.appendFloor = end
		}
	}
	// A crash can leave one torn line at the end of each stream's last file.
	for _, stream := range observationStreams {
		for index := len(hours) - 1; index >= 0; index-- {
			if sealed[hours[index]] {
				continue
			}
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
	l.writerMu.Lock()
	defer l.writerMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("observation log is closed")
	}
	// A restart publishes every Workload again; the root file keeps each body once.
	if retained := l.retained[definition.Identity()]; retained != nil {
		l.writerMu.Mark(serving.DiagnosticMarshal, serving.DiagnosticBegin)
		stored, err := json.Marshal(DefinitionObservation{Identity: definition.Identity(), Definition: retained})
		l.writerMu.Mark(serving.DiagnosticMarshal, serving.DiagnosticEnd)
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

func (l *ObservationLog) ManagementEvent(event serving.ManagementEvent) error {
	if event.WorkloadUID == "" || event.At.IsZero() {
		return errors.New("management UID and event time are required")
	}
	l.writerMu.Lock()
	defer l.writerMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	// Management codes are classified at the single registry boundary.
	return l.appendLocked(observationManagementStream, l.appendTimeLocked(), event)
}

func (l *ObservationLog) Flush(ctx context.Context) error {
	l.flushMu.Lock()
	defer l.flushMu.Unlock()
	l.writerMu.Lock()
	l.mu.Lock()
	var result error
	l.requestMu.Lock()
	var closing []string
	for id, row := range l.open {
		if row.Closing && !l.closed {
			closing = append(closing, id)
		}
	}
	l.requestMu.Unlock()
	for _, id := range closing {
		result = errors.Join(result, l.closeRowLocked(id))
	}
	files, err := l.duplicateFilesLocked()
	result = errors.Join(result, err)
	l.mu.Unlock()
	l.writerMu.Unlock()
	for _, file := range files {
		result = errors.Join(result, l.syncFileOutsideLock(file), file.Close())
	}
	l.recordError(result)
	return errors.Join(result, l.maintain(ctx))
}

func (l *ObservationLog) Close(ctx context.Context) error {
	l.flushMu.Lock()
	defer l.flushMu.Unlock()
	l.writerMu.Lock()
	l.mu.Lock()
	var files []*os.File
	if !l.closed {
		l.requestMu.Lock()
		l.closed = true
		l.requestMu.Unlock()
		for stream, file := range l.files {
			files = append(files, file)
			delete(l.files, stream)
			delete(l.offsets, file)
		}
		files = append(files, l.definitions)
		delete(l.offsets, l.definitions)
	}
	l.mu.Unlock()
	l.writerMu.Unlock()
	var result error
	for _, file := range files {
		result = errors.Join(result, l.syncFileOutsideLock(file), file.Close())
	}
	l.recordError(result)
	return errors.Join(result, l.maintain(ctx))
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
	l.mu.Lock()
	defer l.mu.Unlock()
	h := ObservationLogHealth{Healthy: l.lastError == nil}
	if l.lastError != nil {
		h.Error = l.lastError.Error()
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
