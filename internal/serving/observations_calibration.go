package serving

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// CompletionCalibration selects existing raw journal objects, not a new evidence
// format. Archived metadata precedes publication and therefore has no object list;
// each window explicitly supplies the three raw roles from JournalHealth.Windows.
type CompletionCalibration struct {
	From    time.Time                     `json:"from"`
	To      time.Time                     `json:"to"`
	Windows []CompletionCalibrationWindow `json:"windows"`
}

type CompletionCalibrationWindow struct {
	Metadata    evidencestore.ObjectIdentity `json:"metadata"`
	Definitions evidencestore.ObjectIdentity `json:"definitions"`
	Requests    evidencestore.ObjectIdentity `json:"requests"`
}

// Matches binds a boundary-validated quantity to the existing measured execution
// condition. UID and SLO are deliberately not execution conditions.
func (o ObservedOverhead) Matches(instance SchedulerInstance) bool {
	if !o.Valid() || instance.Profile == nil || instance.Profile.RuntimeMetadata == nil {
		return false
	}
	p, runtime := instance.Profile, instance.Profile.RuntimeMetadata
	if !slices.Equal(instance.Allocation.CPUs, runtime.CPUAffinity) || instance.Allocation.Threads != runtime.IntraOpThreads || instance.Allocation.GPUUUID != runtime.GPUUUID || o.Node != instance.Node || o.Profile != p.ID || o.Artifact != p.Condition.Artifact.Name || o.ExecutionPath != p.Condition.Artifact.ExecutionPath {
		return false
	}
	for _, d := range o.Definitions {
		if d == nil || !MatchesProfile(d, p) {
			return false
		}
	}
	return len(o.Definitions) > 0
}

// CompletionPolicySource carries a bounded native quantity witness. Full trial
// references and execution provenance remain in Serving's measurement/journal.
func (instance SchedulerInstance) CompletionPolicySource() string {
	measurement := instance.CompletionMeasurement
	if measurement.Valid() && measurement.Completion == instance.CompletionOverhead && measurement.Matches(instance) {
		return fmt.Sprintf(
			"serving completion maximum; interval=%s/%s; trials=%d; full provenance=Serving completionMeasurement; not a future guarantee",
			measurement.From.UTC().Format(time.RFC3339Nano), measurement.To.UTC().Format(time.RFC3339Nano), measurement.Requests,
		)
	}
	return instance.EnvelopeSource
}

// LoadCompletionCalibration reads selected immutable bytes once at owner refresh.
// A selection is usable only with complete closed windows retaining [from,to),
// frozen definitions and successful joined native exchange/response spans.
func LoadCompletionCalibration(ctx context.Context, reader evidencestore.Reader, selection CompletionCalibration, maxBytes int64) (map[string]ObservedOverhead, error) {
	if reader == nil || maxBytes < 1 || selection.From.IsZero() || !selection.From.Before(selection.To) || len(selection.Windows) == 0 {
		return nil, errors.New("completion calibration requires bounded reader, explicit interval and raw windows")
	}
	collector := calibrationFacts{
		from: selection.From, to: selection.To,
		definitions: map[string]*Definition{}, facts: map[string]*overheadFact{},
		identities: map[string]calibrationRequestIdentity{},
	}
	windows := make([]ObservationWindow, 0, len(selection.Windows))
	references := make([]evidencestore.ObjectIdentity, 0, 3*len(selection.Windows))
	seen := map[string]bool{}
	for _, refs := range selection.Windows {
		w, err := readCalibrationWindow(ctx, reader, refs, maxBytes)
		if err != nil {
			return nil, err
		}
		if seen[w.ID] {
			return nil, errors.New("duplicate completion calibration window")
		}
		seen[w.ID] = true
		windows = append(windows, w)
		references = append(references, refs.Metadata, refs.Definitions, refs.Requests)
		if err = readCalibrationStream(ctx, reader, refs.Definitions, w, maxBytes, collector.addDefinition); err != nil {
			return nil, err
		}
		if err = readCalibrationStream(ctx, reader, refs.Requests, w, maxBytes, collector.addRequest); err != nil {
			return nil, err
		}
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].StartedAt.Before(windows[j].StartedAt) })
	// Rotation is serialized with append; planned EndsAt is not a continuity
	// witness. Flush can close after it, and starting the next window takes time.
	// Use actual retained boundaries without extending the arrival interval.
	if windows[0].StartedAt.After(selection.From) || windows[len(windows)-1].ClosedAt.Before(selection.To) {
		return nil, errors.New("completion calibration interval is not fully closed")
	}
	result := overheadProjection(collector.facts, collector.definitions, selection.From, selection.To, references)
	if len(result) == 0 {
		return nil, errors.New("completion calibration lacks completed native trials")
	}
	return result, nil
}

func readCalibrationWindow(ctx context.Context, reader evidencestore.Reader, refs CompletionCalibrationWindow, maxBytes int64) (ObservationWindow, error) {
	var w ObservationWindow
	raw, err := readCalibrationObject(ctx, reader, refs.Metadata, maxBytes)
	if err != nil {
		return w, err
	}
	if err = decodeCalibrationJSON(raw, &w); err != nil {
		return w, err
	}
	if w.SchemaVersion != observationSchema || w.ID == "" || w.Incomplete || w.ClosedAt == nil ||
		!w.ClosedAt.After(w.StartedAt) || w.Duration <= 0 || !w.EndsAt.Equal(w.StartedAt.Add(w.Duration)) {
		return w, errors.New("completion calibration requires complete closed journal windows")
	}
	for name, ref := range map[string]evidencestore.ObjectIdentity{
		"metadata.json": refs.Metadata, "definitions.jsonl": refs.Definitions, "requests.jsonl": refs.Requests,
	} {
		if ref.Store != refs.Metadata.Store || ref.Key != "serving/"+w.ID+"/"+name {
			return w, errors.New("completion calibration raw role/window mismatch")
		}
	}
	return w, nil
}

func readCalibrationStream(ctx context.Context, reader evidencestore.Reader, ref evidencestore.ObjectIdentity, w ObservationWindow, maxBytes int64, visit func(observationRecord) error) error {
	raw, err := readCalibrationObject(ctx, reader, ref, maxBytes)
	if err != nil {
		return err
	}
	return scanCalibrationStream(raw, w, visit)
}

type calibrationRequestIdentity struct {
	received             time.Time
	workload, definition string
}

type calibrationFacts struct {
	from, to    time.Time
	definitions map[string]*Definition
	facts       map[string]*overheadFact
	identities  map[string]calibrationRequestIdentity
}

func (c *calibrationFacts) addDefinition(record observationRecord) error {
	d := record.Definition
	if d == nil || record.Request != nil || record.Management != nil || record.Scheduler != nil ||
		d.Definition == nil || d.Identity != d.Definition.Identity() || d.Definition.UID == "" || d.Definition.Model.UID == "" {
		return errors.New("invalid frozen calibration definition")
	}
	c.definitions[d.Identity] = d.Definition
	return nil
}

func (c *calibrationFacts) addRequest(record observationRecord) error {
	e := record.Request
	if e == nil || record.Definition != nil || record.Management != nil || record.Scheduler != nil || e.RequestID == "" || e.At.IsZero() {
		return errors.New("invalid calibration request record")
	}
	if e.Outcome != outcomeCompleted || !observationInInterval(e.ReceivedAt, c.from, c.to) {
		return nil
	}
	if e.Kind != eventBackendExchangeFinished && e.Kind != eventResponseFinished {
		return nil
	}
	if err := c.joinIdentity(e); err != nil {
		return err
	}
	fact := c.facts[e.RequestID]
	if fact == nil {
		fact = &overheadFact{}
		c.facts[e.RequestID] = fact
	}
	if e.Kind == eventBackendExchangeFinished && fact.haveExchange || e.Kind == eventResponseFinished && fact.haveResponse {
		return errors.New("duplicate calibration completion span")
	}
	if p := e.NativeProgress; p != nil &&
		(p.RequestID != "" && p.RequestID != e.RequestID ||
			p.WorkloadUID != "" && p.WorkloadUID != e.WorkloadUID ||
			p.Definition != "" && p.Definition != e.Definition) {
		return errors.New("calibration native request identity mismatch")
	}
	fact.add(e)
	return nil
}

func (c *calibrationFacts) joinIdentity(e *RequestEvent) error {
	identity, exists := c.identities[e.RequestID]
	if exists && (!identity.received.Equal(e.ReceivedAt) ||
		identity.workload != "" && e.WorkloadUID != "" && identity.workload != e.WorkloadUID ||
		identity.definition != "" && e.Definition != "" && identity.definition != e.Definition) {
		return errors.New("calibration joined request identity mismatch")
	}
	if !exists {
		identity.received = e.ReceivedAt
	}
	if e.WorkloadUID != "" {
		identity.workload = e.WorkloadUID
	}
	if e.Definition != "" {
		identity.definition = e.Definition
	}
	c.identities[e.RequestID] = identity
	return nil
}

func readCalibrationObject(ctx context.Context, reader evidencestore.Reader, ref evidencestore.ObjectIdentity, maxBytes int64) ([]byte, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if ref.Size > maxBytes {
		return nil, errors.New("completion calibration object exceeds evidence bound")
	}
	stream, err := reader.Open(ctx, ref)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(stream, ref.Size+1))
	if err = errors.Join(readErr, stream.Close()); err != nil {
		return nil, err
	}
	if int64(len(raw)) != ref.Size {
		return nil, evidencestore.ErrContentMismatch
	}
	return raw, nil
}

func decodeCalibrationJSON(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("one raw calibration JSON object required")
	}
	return nil
}

func scanCalibrationStream(raw []byte, w ObservationWindow, visit func(observationRecord) error) error {
	reader := bufio.NewReader(bytes.NewReader(raw))
	first := true
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(line) != 0 {
			if line[len(line)-1] != '\n' {
				return errors.New("incomplete calibration JSONL tail")
			}
			if first {
				var header ObservationWindow
				if err := decodeCalibrationJSON(line, &header); err != nil {
					return err
				}
				if header.SchemaVersion != observationSchema || header.ID != w.ID || !header.StartedAt.Equal(w.StartedAt) || !header.EndsAt.Equal(w.EndsAt) || header.Duration != w.Duration {
					return errors.New("calibration stream header mismatch")
				}
				first = false
			} else {
				var record observationRecord
				if err := decodeCalibrationJSON(line, &record); err != nil {
					return err
				}
				if record.SchemaVersion != observationSchema || record.Window != w.ID {
					return fmt.Errorf("calibration record window mismatch: %s", w.ID)
				}
				if err := visit(record); err != nil {
					return err
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if first {
		return errors.New("calibration stream header missing")
	}
	return nil
}
