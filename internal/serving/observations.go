package serving

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"gonum.org/v1/gonum/stat"
)

const observationSchema = "chill.dacs.io/serving-observations.v1alpha2"
const observationSchedulerStream = "scheduler"

type JournalConfig struct {
	Directory   string
	Store       string
	Window      time.Duration
	Writer      evidencestore.ImmutableWriter
	Definitions []string
	Outcomes    []string
}

type ObservationWindow struct {
	SchemaVersion string                         `json:"schemaVersion"`
	ID            string                         `json:"id"`
	StartedAt     time.Time                      `json:"startedAt"`
	EndsAt        time.Time                      `json:"endsAt"`
	ClosedAt      *time.Time                     `json:"closedAt,omitempty"`
	Duration      time.Duration                  `json:"durationNanos"`
	Definitions   []string                       `json:"definitions,omitempty"`
	Outcomes      []string                       `json:"outcomes,omitempty"`
	Incomplete    bool                           `json:"incomplete"`
	Published     bool                           `json:"published"`
	Objects       []evidencestore.ObjectIdentity `json:"objects,omitempty"`
}

// overlaps uses the actual retained interval, not a closed window's planned end.
func (w ObservationWindow) overlaps(from, to time.Time) bool {
	end := w.EndsAt
	if w.ClosedAt != nil && w.ClosedAt.Before(end) {
		end = *w.ClosedAt
	}
	return w.StartedAt.Before(to) && end.After(from)
}

type DefinitionObservation struct {
	Identity   string      `json:"identity"`
	Definition *Definition `json:"definition"`
}

type observationRecord struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Window        string                 `json:"window"`
	Definition    *DefinitionObservation `json:"definition,omitempty"`
	Request       *RequestEvent          `json:"request,omitempty"`
	Management    *ManagementEvent       `json:"management,omitempty"`
	Scheduler     *SchedulerObservation  `json:"scheduler,omitempty"`
}

type JournalHealth struct {
	Healthy bool                `json:"healthy"`
	Error   string              `json:"error,omitempty"`
	Windows []ObservationWindow `json:"windows"`
}

// Journal keeps raw facts in files and a bounded, per-window scheduler census;
// queries capture both under the writer lock and scan selected prefixes outside it.
type Journal struct {
	mu              sync.Mutex
	config          JournalConfig
	windows         []ObservationWindow
	active          int
	files           map[string]*os.File
	schedulerCensus map[string]schedulerWindowCensus
	schedulerWriter schedulerCountingWriter
	closed          bool
	lastError       error
}

// Counting the existing encoder writes avoids encoding or retaining records a
// second time. Direct file rewrites do not advance this count and force a scan.
type schedulerCountingWriter struct {
	*os.File
	bytes int64
}

func (w *schedulerCountingWriter) Write(data []byte) (int, error) {
	n, err := w.File.Write(data)
	w.bytes += int64(n)
	return n, err
}

func NewJournal(config JournalConfig) (*Journal, error) {
	if config.Directory == "" || config.Store == "" || config.Window <= 0 || config.Writer == nil {
		return nil, errors.New("observation directory, store, positive window and immutable writer are required")
	}
	if err := os.MkdirAll(config.Directory, 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(config.Directory)
	if err != nil {
		return nil, err
	}
	j := &Journal{config: config, active: -1, schedulerCensus: map[string]schedulerWindowCensus{}}
	for _, entry := range entries {
		// Hidden directories are unpublished staging, never observation windows.
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(config.Directory, entry.Name(), "metadata.json"))
		if err != nil {
			return nil, fmt.Errorf("read observation window %s: %w", entry.Name(), err)
		}
		var window ObservationWindow
		if err := json.Unmarshal(data, &window); err != nil {
			return nil, err
		}
		if window.SchemaVersion != observationSchema || window.ID != entry.Name() {
			return nil, errors.New("invalid observation window metadata")
		}
		if window.ClosedAt == nil {
			now := time.Now().UTC()
			window.ClosedAt = &now
			window.Incomplete = true
		}
		j.windows = append(j.windows, window)
	}
	streams := []string{"definitions", "requests", "management", observationSchedulerStream}
	read, err := j.snapshot(streams...)
	if err != nil {
		return nil, err
	}
	for _, stream := range streams {
		if err := read.scan(stream, func(record observationRecord) {
			if stream == observationSchedulerStream {
				census := j.schedulerCensus[record.Window]
				census.add(record.Scheduler)
				j.schedulerCensus[record.Window] = census
			}
		}); err != nil {
			return nil, err
		}
		if stream == observationSchedulerStream {
			for _, prefix := range read.streams[stream] {
				window := read.windows[prefix.window]
				census := j.schedulerCensus[window.ID]
				census.prefixBytes, census.trusted = prefix.bytes, !window.Incomplete
				j.schedulerCensus[window.ID] = census
			}
		}
	}
	for i := range j.windows {
		if j.windows[i].Incomplete {
			if err := j.saveWindow(i); err != nil {
				return nil, err
			}
		}
	}
	// A restart never resumes an accepted request or appends to a prior window.
	if err := j.start(time.Now().UTC()); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *Journal) saveWindow(index int) error {
	w := j.windows[index]
	return writeWindowMetadata(filepath.Join(j.config.Directory, w.ID), w)
}

func writeWindowMetadata(directory string, w ObservationWindow) (result error) {
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".metadata-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() {
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	if err := file.Chmod(0600); err != nil {
		return errors.Join(err, file.Close())
	}
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(directory, "metadata.json"))
}

// start builds a window under a hidden staging name and publishes the complete
// directory with one rename. A window directory therefore never exists without
// its metadata and streams, and NewJournal only ever sees complete windows.
func (j *Journal) start(now time.Time) (result error) {
	const staging = ".starting-"
	directory, err := os.MkdirTemp(j.config.Directory, staging)
	if err != nil {
		return err
	}
	window := ObservationWindow{
		SchemaVersion: observationSchema,
		ID:            now.Format("20060102T150405.000000000Z") + "-" + strings.TrimPrefix(filepath.Base(directory), staging),
		StartedAt:     now, EndsAt: now.Add(j.config.Window), Duration: j.config.Window,
		Definitions: j.config.Definitions, Outcomes: j.config.Outcomes,
	}
	files := make(map[string]*os.File)
	defer func() {
		if result != nil {
			for _, file := range files {
				result = errors.Join(result, file.Close())
			}
			// Only this call's staging directory is removed; published windows stay visible.
			result = errors.Join(result, os.RemoveAll(directory))
		}
	}()
	if err := writeWindowMetadata(directory, window); err != nil {
		return err
	}
	for _, stream := range []string{"definitions", "requests", "management", observationSchedulerStream} {
		file, err := os.OpenFile(filepath.Join(directory, stream+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		files[stream] = file
		// Metadata is the first JSONL record, including in an otherwise empty stream.
		if err := json.NewEncoder(file).Encode(window); err != nil {
			return err
		}
	}
	info, err := files[observationSchedulerStream].Stat()
	if err != nil {
		return err
	}
	if err := os.Rename(directory, filepath.Join(j.config.Directory, window.ID)); err != nil {
		return err
	}
	j.windows = append(j.windows, window)
	j.active = len(j.windows) - 1
	j.files = files
	j.schedulerCensus[window.ID] = schedulerWindowCensus{prefixBytes: info.Size(), trusted: true}
	j.schedulerWriter = schedulerCountingWriter{File: files[observationSchedulerStream], bytes: info.Size()}
	return nil
}

func (j *Journal) append(stream string, record observationRecord) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return errors.New("observation journal is closed")
	}
	if j.files == nil || !time.Now().Before(j.windows[j.active].EndsAt) {
		if j.files != nil {
			if err := j.finish(); err != nil {
				j.lastError = err
				return err
			}
		}
		if err := j.start(time.Now().UTC()); err != nil {
			j.lastError = err
			return err
		}
	}
	record.SchemaVersion = observationSchema
	record.Window = j.windows[j.active].ID
	var err error
	if stream == observationSchedulerStream {
		err = json.NewEncoder(&j.schedulerWriter).Encode(record)
		census := j.schedulerCensus[record.Window]
		if err == nil {
			census.add(record.Scheduler)
			census.prefixBytes = j.schedulerWriter.bytes
		} else {
			census.trusted = false
		}
		j.schedulerCensus[record.Window] = census
	} else {
		err = json.NewEncoder(j.files[stream]).Encode(record)
	}
	if err != nil {
		j.lastError = err
		j.windows[j.active].Incomplete = true
	}
	return err
}

func (j *Journal) DefinitionRecord(definition *Definition) error {
	if definition == nil {
		return errors.New("definition is required")
	}
	return j.append("definitions", observationRecord{Definition: &DefinitionObservation{Identity: definition.Identity(), Definition: definition}})
}

func (j *Journal) RequestEvent(event RequestEvent) error {
	if event.RequestID == "" || event.At.IsZero() {
		return errors.New("request identity and event time are required")
	}
	// Producers supply classified codes, never transport bodies or credentials.
	return j.append("requests", observationRecord{Request: &event})
}

func (j *Journal) ManagementEvent(event ManagementEvent) error {
	if event.WorkloadUID == "" || event.At.IsZero() {
		return errors.New("management UID and event time are required")
	}
	// Management codes are classified at the single registry boundary.
	return j.append("management", observationRecord{Management: &event})
}

func (j *Journal) finish() error {
	if j.files == nil {
		return nil
	}
	var result error
	for _, file := range j.files {
		result = errors.Join(result, file.Sync(), file.Close())
	}
	j.files = nil
	now := time.Now().UTC()
	j.windows[j.active].ClosedAt = &now
	if result != nil {
		j.windows[j.active].Incomplete = true
	}
	return errors.Join(result, j.saveWindow(j.active))
}

func (j *Journal) archive(ctx context.Context) error {
	var result error
	for i := range j.windows {
		window := &j.windows[i]
		if window.ClosedAt == nil || window.Published {
			continue
		}
		objects := make([]evidencestore.ObjectIdentity, 0, 5)
		var failed error
		for _, name := range []string{"definitions.jsonl", "requests.jsonl", "management.jsonl", "scheduler.jsonl", "metadata.json"} {
			file, err := os.Open(filepath.Join(j.config.Directory, window.ID, name))
			if err != nil {
				failed = err
				break
			}
			info, err := file.Stat()
			if err != nil {
				failed = errors.Join(err, file.Close())
				break
			}
			object := evidencestore.ObjectIdentity{Store: j.config.Store, Key: "serving/" + window.ID + "/" + name, Size: info.Size()}
			_, err = j.config.Writer.PutImmutable(ctx, object, file)
			closeErr := file.Close()
			if err = errors.Join(err, closeErr); err != nil {
				failed = err
				break
			}
			objects = append(objects, object)
		}
		if failed != nil {
			result = errors.Join(result, failed)
			continue
		}
		window.Objects = objects
		window.Published = true
		if err := j.saveWindow(i); err != nil {
			window.Published = false
			window.Objects = nil
			result = errors.Join(result, err)
		}
	}
	return result
}

func (j *Journal) Flush(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	var result error
	if !j.closed {
		if j.files == nil || !time.Now().Before(j.windows[j.active].EndsAt) {
			if j.files != nil {
				result = j.finish()
			}
			if result == nil {
				result = j.start(time.Now().UTC())
			}
		} else {
			for _, file := range j.files {
				result = errors.Join(result, file.Sync())
			}
		}
	}
	if result != nil {
		j.lastError = result
		// A failed start or finish leaves no open window; finish marks itself.
		if j.files != nil && j.active >= 0 {
			j.windows[j.active].Incomplete = true
		}
	}
	return errors.Join(result, j.archive(ctx))
}

func (j *Journal) Close(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	var result error
	if !j.closed {
		result = j.finish()
		j.closed = true
	}
	if result != nil {
		j.lastError = result
	}
	return errors.Join(result, j.archive(ctx))
}

func (j *Journal) Health() JournalHealth {
	j.mu.Lock()
	defer j.mu.Unlock()
	h := JournalHealth{Healthy: j.lastError == nil, Windows: append([]ObservationWindow(nil), j.windows...)}
	if j.lastError != nil {
		h.Error = j.lastError.Error()
	}
	return h
}

type ObservationCohort struct {
	Definition         string         `json:"definition"`
	Total              int            `json:"total"`
	Outcomes           map[string]int `json:"outcomes"`
	SampleCount        int            `json:"sampleCount"`
	LatencyNanos       []float64      `json:"latencyNanos"`
	Percentile         *float64       `json:"percentile,omitempty"`
	QuantileNanos      *float64       `json:"quantileNanos"`
	TargetLatencyNanos *int64         `json:"targetLatencyNanos,omitempty"`
}

type ObservationResult struct {
	UID               string                  `json:"uid"`
	From              time.Time               `json:"from"`
	To                time.Time               `json:"to"`
	DefinitionFilter  []string                `json:"definitionFilter"`
	OutcomeFilter     []string                `json:"outcomeFilter"`
	Total             int                     `json:"total"`
	Selected          int                     `json:"selected"`
	Outcomes          map[string]int          `json:"outcomes"`
	Cohorts           []ObservationCohort     `json:"cohorts"`
	Definitions       []DefinitionObservation `json:"definitions"`
	Requests          []RequestEvent          `json:"requests"`
	Management        []ManagementEvent       `json:"management"`
	NextCursor        string                  `json:"nextCursor,omitempty"`
	QuantileMethod    string                  `json:"quantileMethod"`
	SamplePopulation  string                  `json:"samplePopulation"`
	IncompleteWindows []string                `json:"incompleteWindows,omitempty"`
}

func observationIncludes(filter []string, value string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, item := range filter {
		if item == value {
			return true
		}
	}
	return false
}

func observationInInterval(at, from, to time.Time) bool {
	return !at.IsZero() && (from.IsZero() || !at.Before(from)) && (to.IsZero() || at.Before(to))
}

type requestFacts struct {
	events     []RequestEvent
	received   time.Time
	definition string
	outcome    string
	completion *RequestEvent
	accepted   bool
}

func (f *requestFacts) addEvent(e *RequestEvent) {
	f.events = append(f.events, *e)
	if e.Definition != "" {
		f.definition = e.Definition
	}
	if !e.ReceivedAt.IsZero() {
		f.received = e.ReceivedAt
	} else if e.Kind == phaseReceived {
		f.received = e.At
	}
	if e.Kind == phaseAccepted {
		f.accepted = true
	}
	if e.Outcome != "" && !strings.HasPrefix(e.Kind, "backend_") &&
		e.Kind != phaseAssigned && e.Kind != phaseReceived && e.Kind != phaseAccepted {
		f.outcome = e.Outcome
	}
	if e.Kind == eventResponseFinished {
		switch e.Outcome {
		case "delivery_failed", outcomeIncomplete, "canceled":
			f.completion = nil
			return
		}
		f.completion = e
		if f.outcome == "" {
			f.outcome = outcomeCompleted
		}
	}
}

func (read *observationSnapshot) collectObservationFacts(
	result *ObservationResult,
) (map[string]*Definition, map[string]*requestFacts, error) {
	definitions := make(map[string]*Definition)
	if err := read.scan("definitions", func(r observationRecord) {
		d := r.Definition
		if d != nil && d.Definition != nil && d.Definition.UID == result.UID &&
			observationIncludes(result.DefinitionFilter, d.Identity) {
			definitions[d.Identity] = d.Definition
		}
	}); err != nil {
		return nil, nil, err
	}
	requests := make(map[string]*requestFacts)
	if err := read.scan("requests", func(r observationRecord) {
		e := r.Request
		if e == nil || e.WorkloadUID != result.UID {
			return
		}
		f := requests[e.RequestID]
		if f == nil {
			f = &requestFacts{}
			requests[e.RequestID] = f
		}
		f.addEvent(e)
	}); err != nil {
		return nil, nil, err
	}
	return definitions, requests, nil
}

func (read *observationSnapshot) collectManagementFacts(result *ObservationResult) error {
	if err := read.scan("management", func(r observationRecord) {
		e := r.Management
		if e == nil || e.WorkloadUID != result.UID || !observationInInterval(e.At, result.From, result.To) {
			return
		}
		filter := result.DefinitionFilter
		if len(filter) != 0 &&
			(e.Before == nil || !observationIncludes(filter, e.Before.Identity())) &&
			(e.Target == nil || !observationIncludes(filter, e.Target.Identity())) &&
			(e.After == nil || !observationIncludes(filter, e.After.Identity())) {
			return
		}
		result.Management = append(result.Management, *e)
	}); err != nil {
		return err
	}
	sort.SliceStable(result.Management, func(a, b int) bool {
		return result.Management[a].At.Before(result.Management[b].At)
	})
	for _, w := range read.windows {
		if w.Incomplete {
			result.IncompleteWindows = append(result.IncompleteWindows, w.ID)
		}
	}
	return nil
}

// Aggregate the whole selected population before projecting any raw-event page.
func (result *ObservationResult) aggregate(
	definitions map[string]*Definition, requests map[string]*requestFacts,
) []RequestEvent {
	cohorts := make(map[string]*ObservationCohort)
	for id, d := range definitions {
		p := d.SLO.Percentile
		target := int64(d.SLO.TargetLatency)
		cohorts[id] = &ObservationCohort{
			Definition: id, Outcomes: make(map[string]int), Percentile: &p, TargetLatencyNanos: &target,
		}
		result.Definitions = append(result.Definitions, DefinitionObservation{Identity: id, Definition: d})
	}
	selectedEvents := []RequestEvent{}
	for _, f := range requests {
		if !observationInInterval(f.received, result.From, result.To) ||
			!observationIncludes(result.DefinitionFilter, f.definition) {
			continue
		}
		result.Total++
		if f.outcome == "" {
			if f.accepted {
				f.outcome = outcomeIncomplete
			} else {
				f.outcome = phasePending
			}
		}
		result.Outcomes[f.outcome]++
		if !observationIncludes(result.OutcomeFilter, f.outcome) {
			continue
		}
		result.Selected++
		selectedEvents = append(selectedEvents, f.events...)
		cohort := cohorts[f.definition]
		if cohort == nil {
			cohort = &ObservationCohort{Definition: f.definition, Outcomes: make(map[string]int)}
			cohorts[f.definition] = cohort
		}
		cohort.Total++
		cohort.Outcomes[f.outcome]++
		switch f.outcome {
		case "delivery_failed", outcomeIncomplete, "canceled":
			continue
		}
		if e := f.completion; e != nil && e.ElapsedNanos > 0 && !e.ReceivedAt.IsZero() {
			cohort.LatencyNanos = append(cohort.LatencyNanos, float64(e.ElapsedNanos))
		}
	}
	for _, c := range cohorts {
		sort.Float64s(c.LatencyNanos)
		c.SampleCount = len(c.LatencyNanos)
		if c.SampleCount > 0 && c.Percentile != nil && *c.Percentile > 0 && *c.Percentile <= 100 {
			q := stat.Quantile(*c.Percentile/100, stat.Empirical, c.LatencyNanos, nil)
			c.QuantileNanos = &q
		}
		result.Cohorts = append(result.Cohorts, *c)
	}
	sort.Slice(result.Cohorts, func(a, b int) bool { return result.Cohorts[a].Definition < result.Cohorts[b].Definition })
	sort.Slice(result.Definitions, func(a, b int) bool { return result.Definitions[a].Identity < result.Definitions[b].Identity })
	return selectedEvents
}

func (j *Journal) Query(uid string, from, to time.Time, definitions, outcomes []string, limit int, cursor string) (ObservationResult, error) {
	if len(definitions) == 0 {
		definitions = j.config.Definitions
	}
	if len(outcomes) == 0 {
		outcomes = j.config.Outcomes
	}
	result := ObservationResult{
		UID: uid, From: from, To: to, DefinitionFilter: definitions, OutcomeFilter: outcomes,
		Outcomes: map[string]int{}, QuantileMethod: "gonum/stat.Empirical",
		SamplePopulation: "response_finished excluding canceled, incomplete and delivery_failed; " +
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
	read, err := j.snapshot("definitions", "requests", "management")
	if err != nil {
		return result, err
	}
	definitionsByID, requests, err := read.collectObservationFacts(&result)
	if err != nil {
		return result, err
	}
	selectedEvents := result.aggregate(definitionsByID, requests)
	sort.SliceStable(selectedEvents, func(a, b int) bool {
		if selectedEvents[a].At.Equal(selectedEvents[b].At) {
			return selectedEvents[a].RequestID < selectedEvents[b].RequestID
		}
		return selectedEvents[a].At.Before(selectedEvents[b].At)
	})
	if offset > len(selectedEvents) {
		return result, failure("InvalidQuery", "observation cursor is out of range")
	}
	end := len(selectedEvents)
	if limit > 0 && limit < end-offset {
		end = offset + limit
		result.NextCursor = strconv.Itoa(end)
	}
	result.Requests = selectedEvents[offset:end]
	if err := read.collectManagementFacts(&result); err != nil {
		return result, err
	}
	return result, nil
}

type unfinishedBackend struct {
	event            RequestEvent
	phase            string
	responseRecorded bool
}

// Startup reads existing facts only. It does not replay inference or resume an
// interrupted management operation.
func (j *Journal) runtimeFacts() (map[string]*Definition, []unfinishedBackend, error) {
	read, err := j.snapshot("definitions", "requests")
	if err != nil {
		return nil, nil, err
	}
	definitions := map[string]*Definition{}
	if err := read.scan("definitions", func(record observationRecord) {
		if d := record.Definition; d != nil {
			definitions[d.Identity] = d.Definition
		}
	}); err != nil {
		return nil, nil, err
	}
	assigned := map[string]*unfinishedBackend{}
	responded := map[string]bool{}
	ended := map[string]bool{}
	if err := read.scan("requests", func(record observationRecord) {
		e := record.Request
		if e == nil {
			return
		}
		switch e.Kind {
		case phaseAssigned:
			if e.Assignment != nil {
				assigned[e.RequestID] = &unfinishedBackend{event: *e, phase: phaseAssigned}
			}
		case "backend_queued", "backend_deferred", "backend_running":
			if a := assigned[e.RequestID]; a != nil {
				a.phase = strings.TrimPrefix(e.Kind, "backend_")
			}
		case "backend_finished":
			if e.ExecutionComplete {
				ended[e.RequestID] = true
			}
		case eventResponseFinished:
			responded[e.RequestID] = true
		}
	}); err != nil {
		return nil, nil, err
	}
	pending := []unfinishedBackend{}
	for id, fact := range assigned {
		if !ended[id] {
			fact.responseRecorded = responded[id]
			pending = append(pending, *fact)
		}
	}
	return definitions, pending, nil
}
