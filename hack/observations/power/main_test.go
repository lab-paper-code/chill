package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

type nopWriter struct{}

func (nopWriter) StreamImmutable(
	_ context.Context, _ evidencestore.ObjectLocation, produce func(io.Writer) error,
) (evidencestore.Receipt, error) {
	return evidencestore.Receipt{}, produce(io.Discard)
}

// The tool and the ChillServer integrate the same records to the same joules.
func TestPowerMatchesServerOperatingPower(t *testing.T) {
	directory := t.TempDir()
	config := observation.ObservationLogConfig{Directory: directory, Store: "evidence", Retention: 2 * time.Hour, Writer: nopWriter{}}
	log, err := observation.NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close(context.Background()) }()
	from := time.Now().UTC().Add(time.Second)
	to := from.Add(10 * time.Second)
	node := serving.NodeReference{Name: "node", UID: "node-uid"}
	source := metricobserver.SourceIdentity{Kind: "edge-metrics", NodeName: "node",
		Endpoint: "http://meter:18766/prometheus?hostname=node", Metric: "wall_watts", MetricLabels: map[string]string{"hostname": "node"}}
	instance := serving.SchedulerInstance{ID: "i", Node: node, Ready: true, WorkloadUID: "w"}
	snapshot := serving.SchedulerSnapshot{At: from.Add(-time.Second), Instances: []serving.SchedulerInstance{instance}}
	if err := log.SchedulerRecord(observation.SchedulerObservation{At: snapshot.At, Snapshot: &snapshot}); err != nil {
		t.Fatal(err)
	}
	policy := powerwindow.Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: 1, MaximumGap: 2 * time.Second}
	for chunk := range 4 {
		start := from.Add(time.Duration(chunk*3-1) * time.Second)
		power := &metricobserver.Result{Source: source, StartedAt: start, EndedAt: start.Add(3 * time.Second)}
		for s := range 3 {
			at := start.Add(time.Duration(s) * time.Second)
			power.Samples = append(power.Samples, metricobserver.Sample{ObservedAt: at, Watts: 10 + at.Sub(from).Seconds()})
		}
		event := observation.SchedulerObservation{
			At: power.EndedAt, Power: power, Node: &node, Meter: "wall",
			PowerScope: "node-exclusive-wall", PowerCoverage: &policy,
		}
		if err := log.SchedulerRecord(event); err != nil {
			t.Fatal(err)
		}
	}
	meters := derivedTestMeters(t, node, source)
	want, err := log.OperatingPower(context.Background(), from, to, meters, 1000)
	if err != nil || want.WholePoolJoules == nil || want.ActivePoolJoules == nil {
		t.Fatalf("server: %+v %v", want, err)
	}
	if err := log.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	obs := segments(t, filepath.Join(directory, "v1alpha7"), t.TempDir())
	if _, err := run(t.TempDir(), meters, from, to, 0); err == nil {
		t.Fatal("missing boundary hours accepted")
	}
	// The collector brings neighbor hours; an hour without records is empty.
	for _, at := range []time.Time{from.Add(-time.Minute), to.Add(time.Minute)} {
		name := filepath.Join(obs, at.UTC().Format("20060102T15"), "scheduler.jsonl.gz")
		if _, err := os.Stat(name); err != nil {
			writeGzip(t, name, nil)
		}
	}
	got, err := run(obs, meters, from, to, 0)
	if err != nil || got.WholePoolJoules == nil || got.ActivePoolJoules == nil {
		t.Fatalf("tool: %+v %v", got, err)
	}
	if *got.WholePoolJoules != *want.WholePoolJoules || *got.ActivePoolJoules != *want.ActivePoolJoules {
		t.Fatalf("tool %v/%v J, server %v/%v J",
			*got.WholePoolJoules, *got.ActivePoolJoules, *want.WholePoolJoules, *want.ActivePoolJoules)
	}
	// A stricter override re-evaluates raw samples without changing recorded
	// policy; a later ordinary reduction must retain its original result.
	strict, err := run(obs, meters, from, to, 500*time.Millisecond)
	if err != nil || !strings.Contains(strings.Join(strict.Reasons, " "), "maximum sample gap exceeds policy") {
		t.Fatalf("override did not re-evaluate raw gaps: %+v, %v", strict, err)
	}
	original, err := run(obs, meters, from, to, 0)
	if err != nil || !reflect.DeepEqual(original, got) {
		t.Fatalf("override mutated recorded policy: %+v, %v", original, err)
	}
	// Put the membership baseline before the support-hour union. Both readers
	// must retain the original fetched-history behavior, including its energy.
	older := from.Add(-time.Minute).UTC().Truncate(time.Hour).Add(-time.Hour)
	schedulers, err := filepath.Glob(filepath.Join(obs, "*", "scheduler.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range schedulers {
		var remaining bytes.Buffer
		if err := eachLine(name, func(raw []byte) error {
			var row struct {
				Snapshot *json.RawMessage `json:"snapshot"`
			}
			if err := json.Unmarshal(raw, &row); err != nil {
				return err
			}
			if row.Snapshot == nil {
				remaining.Write(raw)
				remaining.WriteByte('\n')
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		writeGzip(t, name, remaining.Bytes())
	}
	snapshot.At = older.Add(59 * time.Minute)
	baseline, err := json.Marshal(observation.SchedulerObservation{At: snapshot.At, Snapshot: &snapshot})
	if err != nil {
		t.Fatal(err)
	}
	writeGzip(t, filepath.Join(obs, older.Format("20060102T15"), "scheduler.jsonl.gz"), append(baseline, '\n'))
	historical, err := run(obs, meters, from, to, 0)
	if err != nil || !reflect.DeepEqual(historical, got) {
		t.Fatalf("older membership baseline changed power: %+v, want %+v, error %v", historical, got, err)
	}
	// Overlapping and distant windows share one local archive decode pass.
	distant := from.UTC().Truncate(time.Hour).Add(3*time.Hour + 10*time.Second)
	windows := []powerWindow{
		{ID: "later", From: distant, To: distant.Add(time.Second)},
		{ID: "full", From: from, To: to},
		{ID: "overlap", From: from.Add(time.Second), To: to.Add(-time.Second)},
		{ID: "repeat", From: from, To: to},
	}
	for _, at := range []time.Time{distant.Add(-time.Minute), distant.Add(time.Second + time.Minute)} {
		writeGzip(t, filepath.Join(obs, at.UTC().Truncate(time.Hour).Format("20060102T15"), "scheduler.jsonl.gz"), nil)
	}
	expected := map[string]bool{}
	for _, stream := range []string{"catalog", "scheduler"} {
		names, err := filepath.Glob(filepath.Join(obs, "*", stream+".jsonl.gz"))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			expected[name] = true
		}
	}
	for _, gap := range []time.Duration{0, 500 * time.Millisecond} {
		calls := map[string]int{}
		decoded := map[string]int{}
		readLines := func(name string, visit func([]byte) error) error {
			calls[name]++
			return eachLine(name, func(raw []byte) error {
				decoded[name]++
				return visit(raw)
			})
		}
		results, err := runWindows(obs, meters, windows, gap, readLines)
		if err != nil || len(results) != len(windows) {
			t.Fatalf("windows: %+v, %v", results, err)
		}
		if len(calls) != len(expected) {
			t.Fatalf("segment reads = %v, want %v", calls, expected)
		}
		for name := range expected {
			if calls[name] != 1 {
				t.Fatalf("segment %s read %d times, want once", name, calls[name])
			}
			// Count actual decoded lines, not just reader construction. This
			// catches rereading a segment through a retained reader callback.
			lines := 0
			if err := eachLine(name, func([]byte) error {
				lines++
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if decoded[name] != lines {
				t.Fatalf("segment %s decoded %d lines, want %d", name, decoded[name], lines)
			}
		}
		for i, window := range windows {
			single, err := run(obs, meters, window.From, window.To, gap)
			if err != nil || results[i].ID != window.ID || !reflect.DeepEqual(results[i].Power, single) {
				t.Fatalf("window %q gap %v: %+v, want %+v, error %v", window.ID, gap, results[i], single, err)
			}
		}
	}
}

func TestReadWindows(t *testing.T) {
	valid := `[{"id":"w0","from":"2026-11-12T10:00:00.123456789Z","to":"2026-11-12T10:00:01Z"}]`
	name := filepath.Join(t.TempDir(), "windows.json")
	for _, test := range []struct {
		name      string
		raw       string
		hasBounds bool
		wantError bool
	}{
		{name: "valid", raw: valid},
		{name: "bounds conflict", raw: valid, hasBounds: true, wantError: true},
		{name: "empty", raw: `[]`, wantError: true},
		{name: "null", raw: `null`, wantError: true},
		{name: "object", raw: `{}`, wantError: true},
		{name: "malformed", raw: `[`, wantError: true},
		{name: "trailing value", raw: valid + `[]`, wantError: true},
		{name: "unknown field", raw: strings.Replace(valid, `"id"`, `"other"`, 1), wantError: true},
		{name: "missing ID", raw: strings.Replace(valid, `"w0"`, `""`, 1), wantError: true},
		{name: "blank ID", raw: strings.Replace(valid, `"w0"`, `" "`, 1), wantError: true},
		{name: "duplicate ID", raw: valid[:len(valid)-1] + "," + valid[1:], wantError: true},
		{name: "missing from", raw: `[{"id":"w0","to":"2026-11-12T10:00:01Z"}]`, wantError: true},
		{name: "missing to", raw: `[{"id":"w0","from":"2026-11-12T10:00:00Z"}]`, wantError: true},
		{name: "invalid time", raw: strings.Replace(valid, "2026-11-12T10:00:01Z", "tomorrow", 1), wantError: true},
		{name: "reversed", raw: strings.Replace(valid, "2026-11-12T10:00:01Z", "2026-11-12T09:00:00Z", 1), wantError: true},
		{name: "zero interval", raw: strings.Replace(valid, "2026-11-12T10:00:01Z", "2026-11-12T10:00:00.123456789Z", 1), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(name, []byte(test.raw), 0600); err != nil {
				t.Fatal(err)
			}
			windows, err := readWindows(name, test.hasBounds)
			if (err != nil) != test.wantError {
				t.Fatalf("windows = %+v, error = %v, want error %v", windows, err, test.wantError)
			}
			if !test.wantError && (len(windows) != 1 || windows[0].ID != "w0" || windows[0].From.Nanosecond() != 123456789) {
				t.Fatalf("parsed windows = %+v", windows)
			}
		})
	}
	if _, err := readWindows(filepath.Join(t.TempDir(), "absent.json"), false); err == nil {
		t.Fatal("missing windows file accepted")
	}
}

func derivedTestMeters(t *testing.T, node serving.NodeReference, source metricobserver.SourceIdentity) []observation.OperatingPowerMeter {
	t.Helper()
	// Accepted settings meters and actual state references name the same meter set.
	files := t.TempDir()
	settingsFile, stateFile := filepath.Join(files, "settings.json"), filepath.Join(files, "state.json")
	settings, err := json.Marshal(map[string]any{"meters": []map[string]any{
		{"id": "wall", "node": node, "scope": "node-exclusive-wall", "source": source},
		{"id": "other", "node": serving.NodeReference{Name: "absent", UID: "absent-uid"}, "scope": "node-exclusive-wall", "source": source},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsFile, settings, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, []byte(`{"snapshot":{"nodes":[{"node":{"name":"node","uid":"node-uid"}}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	meters, err := derivedMeters(settingsFile, stateFile, "node")
	if err != nil || len(meters) != 1 || meters[0].Meter != "wall" || meters[0].Node != node || !reflect.DeepEqual(meters[0].Source, source) {
		t.Fatalf("derived meters = %+v, %v", meters, err)
	}
	for _, names := range []string{"absent", "node,node", "node,"} {
		if _, err := derivedMeters(settingsFile, stateFile, names); err == nil {
			t.Fatalf("invalid Node selection accepted: %q", names)
		}
	}
	return meters
}

func writeGzip(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

// segments gzips each hour's minute files per stream, as the archive uploads them.
func segments(t *testing.T, root, out string) string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	writers := map[string]*gzip.Writer{}
	files := map[string]*os.File{}
	for _, name := range names { // Glob sorts minute files in order.
		hour, stream := filepath.Base(filepath.Dir(name)), strings.SplitN(filepath.Base(name), ".", 2)[0]
		key := filepath.Join(out, hour, stream+".jsonl.gz")
		if writers[key] == nil {
			if err := os.MkdirAll(filepath.Dir(key), 0700); err != nil {
				t.Fatal(err)
			}
			file, err := os.Create(key)
			if err != nil {
				t.Fatal(err)
			}
			files[key], writers[key] = file, gzip.NewWriter(file)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writers[key].Write(data); err != nil {
			t.Fatal(err)
		}
	}
	for key, writer := range writers {
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := files[key].Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}
