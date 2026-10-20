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
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	jsoniter "github.com/json-iterator/go"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"gonum.org/v1/gonum/stat"
)

const (
	observationVersion          = "v1alpha5"
	observationRequestStream    = "requests"
	observationSchedulerStream  = "scheduler"
	observationManagementStream = "management"
	observationCatalogStream    = "catalog"
	observationDefinitionsFile  = "definitions.jsonl"
	observationHourLayout       = "20060102T15"
)

var observationStreams = []string{observationRequestStream, observationSchedulerStream, observationManagementStream, observationCatalogStream}

type ObservationLogConfig struct {
	Directory   string
	Store       string
	Writer      evidencestore.ImmutableWriter
	Definitions []string
	Outcomes    []string
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
	BatchSize         int            `json:"batchSize,omitempty"`
	QueueMS           float64        `json:"queueMS,omitempty"`
	ServiceMS         float64        `json:"serviceMS,omitempty"`
	BackendMS         float64        `json:"backendMS,omitempty"`
	PreprocessMS      float64        `json:"preprocessMS,omitempty"`
	PostprocessMS     float64        `json:"postprocessMS,omitempty"`
	FirstTokenMS      float64        `json:"firstTokenMS,omitempty"`
	Units             int            `json:"units,omitempty"`
	NativeElapsedMS   *float64       `json:"nativeElapsedMS,omitempty"`
	profile           *Profile
}

const (
	requestRowAssigned = "assigned"
	requestRowFinal    = "final"
)

func (r *RequestRow) fold(e *RequestEvent) {
	if e.WorkloadUID != "" {
		r.WorkloadUID = e.WorkloadUID
	}
	if e.Definition != "" {
		r.DefinitionID = e.Definition
	}
	if !e.ReceivedAt.IsZero() {
		r.ReceivedAt = e.ReceivedAt
	} else if r.ReceivedAt.IsZero() && e.Kind == phaseReceived {
		r.ReceivedAt = e.At
	}
	if e.LifetimeNanos > 0 {
		r.LifetimeNanos = e.LifetimeNanos
	}
	if a := e.Assignment; a != nil {
		node, dispatched := a.Node, a.DispatchedAt
		r.Node, r.Instance, r.ProfileID, r.DispatchedAt = &node, a.Instance, a.Profile, &dispatched
		if a.ProfileBody != nil {
			r.profile = a.ProfileBody
		}
	}
	if e.ClientSpanNanos != 0 {
		r.ClientSpanNanos = e.ClientSpanNanos
	}
	r.ExecutionComplete = r.ExecutionComplete || e.ExecutionComplete
	if p := e.NativeProgress; p != nil {
		r.BatchID, r.BatchSize, r.QueueMS, r.ServiceMS, r.BackendMS = p.BatchID, p.BatchSize, p.QueueMS, p.ServiceMS, p.BackendMS
		r.PreprocessMS, r.PostprocessMS, r.FirstTokenMS, r.Units = p.PreprocessMS, p.PostprocessMS, p.FirstTokenMS, p.Units
		native := p.NativeElapsedMS
		r.NativeElapsedMS = &native
		if p.DecisionID != "" {
			r.DecisionID = p.DecisionID
		}
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

func (r *RequestRow) accepted() bool { return r.AcceptedNanos != nil || r.DispatchedAt != nil }

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
	hour string
	body []byte
}

// ObservationLog appends minute files under hourly segment directories and
// keeps only the open request rows and the definitions in memory.
type ObservationLog struct {
	mu          sync.Mutex
	publishMu   sync.Mutex
	config      ObservationLogConfig
	root        string
	started     time.Time
	now         func() time.Time
	files       map[string]*os.File
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
	l := &ObservationLog{
		config: config, root: filepath.Join(config.Directory, observationVersion), started: time.Now().UTC(),
		now: func() time.Time { return time.Now().UTC() }, files: map[string]*os.File{},
		retained: map[string]*Definition{}, open: map[string]*RequestRow{}, catalog: map[string]observationCatalogEntry{},
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
	// RestartDrain observes assigned requests without a final row to completion.
	rows := map[string]RequestRow{}
	for _, hour := range hours {
		files, err := l.hourFiles(hour, observationRequestStream)
		if err != nil {
			return nil, errors.Join(err, l.definitions.Close())
		}
		for _, file := range files {
			if err := scanObservationFile(file.path, -1, func(raw []byte) error {
				var row RequestRow
				if err := json.Unmarshal(raw, &row); err != nil {
					return err
				}
				if row.Kind == requestRowFinal || rows[row.RequestID].Kind != requestRowFinal {
					rows[row.RequestID] = row
				}
				return nil
			}); err != nil {
				return nil, errors.Join(err, l.definitions.Close())
			}
		}
	}
	for _, row := range rows {
		if row.Kind == requestRowAssigned {
			l.restored = append(l.restored, row)
			open := row
			open.Kind = ""
			l.open[row.RequestID] = &open
		}
	}
	return l, nil
}

func truncateTornLine(name string) error {
	data, err := os.ReadFile(name)
	if err != nil || len(data) == 0 || data[len(data)-1] == '\n' {
		return err
	}
	return os.Truncate(name, int64(bytes.LastIndexByte(data, '\n')+1))
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
	data, err := json.Marshal(value)
	if err == nil {
		err = l.writeLocked(stream, now, append(data, '\n'))
	}
	if err != nil {
		l.lastError = err
	}
	return err
}

// ponytail: a failed partial write leaves a torn line that readers report;
// rewind the file on failure if ENOSPC ever happens outside an S3 outage.
func (l *ObservationLog) writeLocked(stream string, now time.Time, data []byte) error {
	name := l.segment(stream, now)
	file := l.files[stream]
	if file == nil || file.Name() != name {
		if file != nil {
			delete(l.files, stream)
			if err := file.Close(); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		var err error
		if file, err = os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err != nil {
			return err
		}
		l.files[stream] = file
	}
	_, err := file.Write(data)
	return err
}

// referenceLocked writes a catalog row when the hour first references key, or
// when its body differs from the last body this process wrote for it.
func (l *ObservationLog) referenceLocked(kind, key string, at, now time.Time, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	id, hour := kind+"/"+key, observationHour(now)
	if entry, ok := l.catalog[id]; ok && entry.hour == hour && bytes.Equal(entry.body, data) {
		return nil
	}
	if err := l.appendLocked(observationCatalogStream, now, CatalogRow{Kind: kind, Key: key, At: at, Body: data}); err != nil {
		return err
	}
	l.catalog[id] = observationCatalogEntry{hour: hour, body: data}
	return nil
}

func (l *ObservationLog) DefinitionRecord(definition *Definition) error {
	if definition == nil {
		return errors.New("definition is required")
	}
	data, err := json.Marshal(DefinitionObservation{Identity: definition.Identity(), Definition: definition})
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("observation log is closed")
	}
	if _, err := l.definitions.Write(append(data, '\n')); err != nil {
		l.lastError = err
		return err
	}
	l.retained[definition.Identity()] = definition
	return nil
}

// RequestEvent folds one event into the request's open row. Only assignment
// appends; an event without an open row is dropped.
func (l *ObservationLog) RequestEvent(event RequestEvent) error {
	if event.RequestID == "" || event.At.IsZero() {
		return errors.New("request identity and event time are required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("observation log is closed")
	}
	row := l.open[event.RequestID]
	if row == nil {
		if strings.HasPrefix(event.Kind, "backend_") {
			return nil // Already settled, or read again from a native log.
		}
		row = &RequestRow{RequestID: event.RequestID}
	}
	next := *row
	next.fold(&event)
	if event.Kind == phaseAssigned {
		assigned := next
		assigned.Kind = requestRowAssigned
		assigned.AcceptedNanos, assigned.ValidatedNanos, assigned.DispatchedNanos = nil, nil, nil
		if err := l.appendRowLocked(&assigned); err != nil {
			return err
		}
	}
	l.open[event.RequestID] = &next
	return nil
}

// RequestClosed appends the final row of a request the Runtime has settled.
func (l *ObservationLog) RequestClosed(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	row := l.open[id]
	if row == nil {
		return nil
	}
	delete(l.open, id)
	final := *row
	final.Kind, final.DispatchedAt = requestRowFinal, nil
	return l.appendRowLocked(&final)
}

func (l *ObservationLog) appendRowLocked(row *RequestRow) error {
	now := l.now()
	if d := l.retained[row.DefinitionID]; d != nil {
		if err := l.referenceLocked("definition", row.DefinitionID, now, now, d); err != nil {
			return err
		}
	}
	if row.profile != nil && row.ProfileID != "" {
		if err := l.referenceLocked("profile", row.ProfileID, now, now, row.profile); err != nil {
			return err
		}
	}
	return l.appendLocked(observationRequestStream, now, row)
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
// makes equal bytes, so a repeated upload is Existing and nothing records
// upload state. An hour is deleted once every stream succeeds in one pass and
// the hour ended at least an hour ago.
func (l *ObservationLog) archive(ctx context.Context) error {
	l.publishMu.Lock()
	defer l.publishMu.Unlock()
	l.mu.Lock()
	now := l.now() // Later appends choose this hour or a later one.
	l.mu.Unlock()
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
		for _, stream := range observationStreams {
			if err := l.upload(ctx, hour, stream); err != nil {
				failed = errors.Join(failed, err)
			}
		}
		if failed == nil && !now.Before(hour.Add(2*time.Hour)) {
			failed = os.RemoveAll(filepath.Join(l.root, observationHour(hour)))
		}
		result = errors.Join(result, failed)
	}
	return result
}

func (l *ObservationLog) upload(ctx context.Context, hour time.Time, stream string) error {
	files, err := l.hourFiles(hour, stream)
	if err != nil || len(files) == 0 {
		return err
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed) // Zero header time: equal input, equal bytes.
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
	object := evidencestore.ObjectIdentity{
		Store: l.config.Store, Key: observationArchivePrefix + observationHour(hour) + "/" + stream + ".jsonl.gz",
		Size: int64(compressed.Len()),
	}
	_, err = l.config.Writer.PutImmutable(ctx, object, &compressed)
	return err
}

func (l *ObservationLog) Flush(ctx context.Context) error {
	l.mu.Lock()
	var result error
	for _, file := range l.files {
		result = errors.Join(result, file.Sync())
	}
	if !l.closed {
		result = errors.Join(result, l.definitions.Sync())
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
			result = errors.Join(result, file.Sync(), file.Close())
			delete(l.files, stream)
		}
		result = errors.Join(result, l.definitions.Sync(), l.definitions.Close())
		l.closed = true
	}
	if result != nil {
		l.lastError = result
	}
	l.mu.Unlock()
	return errors.Join(result, l.archive(ctx))
}

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
func (result *ObservationResult) aggregate(definitions map[string]*Definition, rows []*RequestRow) []RequestRow {
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
	return selected
}

func (l *ObservationLog) Query(uid string, from, to time.Time, definitions, outcomes []string, limit int, cursor string) (ObservationResult, error) {
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
	read, err := l.capture(map[string][2]time.Time{
		observationRequestStream: {from, {}}, observationManagementStream: {from, to},
	})
	if err != nil {
		return result, err
	}
	defer read.close()
	result.GapUntil = read.gap(from)
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
	selected := result.aggregate(byID, rows)
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
// else assigned. Only rows the selector keeps are fully decoded.
func (l *ObservationLog) requestRows(read *observationRead, keep func(*requestEnvelope) bool) ([]*RequestRow, error) {
	rows := map[string]*RequestRow{}
	err := l.scanStream(read, observationRequestStream, func(raw []byte) error {
		var envelope requestEnvelope
		if err := jsoniter.ConfigCompatibleWithStandardLibrary.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		if !keep(&envelope) || envelope.Kind != requestRowFinal && rows[envelope.RequestID] != nil {
			return observationJSONSyntaxError(raw)
		}
		row := &RequestRow{}
		if err := json.Unmarshal(raw, row); err != nil {
			return err
		}
		rows[row.RequestID] = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	for id, row := range read.open {
		if (rows[id] == nil || rows[id].Kind != requestRowFinal) &&
			keep(&requestEnvelope{RequestID: id, WorkloadUID: row.WorkloadUID, ReceivedAt: row.ReceivedAt}) {
			rows[id] = row
		}
	}
	result := make([]*RequestRow, 0, len(rows))
	for _, row := range rows {
		result = append(result, row)
	}
	return result, nil
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
	files       map[string][]observationOpenFile
	open        map[string]*RequestRow
	definitions map[string]*Definition
	start       time.Time
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

// gap reports the start of local observation when the query begins before it.
func (r *observationRead) gap(from time.Time) *time.Time {
	if from.Before(r.start) {
		start := r.start
		return &start
	}
	return nil
}

// capture opens each stream's minute files whose minute lies in [from, to];
// a zero to reads through the latest file.
func (l *ObservationLog) capture(ranges map[string][2]time.Time) (*observationRead, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	read := &observationRead{files: map[string][]observationOpenFile{}, open: map[string]*RequestRow{}, definitions: map[string]*Definition{}, start: l.started}
	for id, row := range l.open {
		copied := *row
		read.open[id] = &copied
	}
	for id, d := range l.retained {
		read.definitions[id] = d
	}
	hours, err := l.hours()
	if err != nil {
		l.lastError = err
		return nil, err
	}
	first := true
	for _, hour := range hours {
		for _, stream := range observationStreams {
			files, err := l.hourFiles(hour, stream)
			if err != nil {
				read.close()
				l.lastError = err
				return nil, err
			}
			if first && len(files) > 0 && files[0].at.Before(read.start) {
				read.start = files[0].at
			}
			bounds, wanted := ranges[stream]
			for _, segment := range files {
				if !wanted || segment.at.Before(bounds[0].Truncate(time.Minute)) || !bounds[1].IsZero() && segment.at.After(bounds[1]) {
					continue
				}
				if err := read.openFile(stream, segment); err != nil {
					read.close()
					l.lastError = err
					return nil, err
				}
			}
		}
		first = false
	}
	return read, nil
}

func (r *observationRead) openFile(stream string, segment observationSegment) error {
	file, err := os.Open(segment.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // Deleted by an archive pass after the listing.
	}
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return errors.Join(err, file.Close())
	}
	r.files[stream] = append(r.files[stream], observationOpenFile{file: file, at: segment.at, size: info.Size()})
	return nil
}

// errObservationStop ends a scan early without an error.
var errObservationStop = errors.New("observation scan stopped")

func (l *ObservationLog) scanStream(read *observationRead, stream string, visit func([]byte) error) error {
	var scanner observationScanner
	for _, file := range read.files[stream] {
		err := scanner.scan(file.file, file.size, visit)
		if errors.Is(err, errObservationStop) {
			return nil
		}
		if err != nil {
			l.mu.Lock()
			l.lastError = err
			l.mu.Unlock()
			return fmt.Errorf("read %s: %w", file.file.Name(), err)
		}
	}
	return nil
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
	reader *bufio.Reader
	long   []byte
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

// jsoniter's skipped-number fast path is not a complete syntax validator.
// Keep the standard syntax boundary without materializing ignored payloads.
func observationJSONSyntaxError(raw []byte) error {
	if json.Valid(raw) {
		return nil
	}
	var invalid json.RawMessage
	return json.Unmarshal(raw, &invalid)
}
