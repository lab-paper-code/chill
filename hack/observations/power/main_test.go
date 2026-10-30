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
)

type nopWriter struct{}

func (nopWriter) PutImmutable(
	_ context.Context, object evidencestore.ObjectIdentity, _ io.Reader,
) (evidencestore.Receipt, error) {
	return evidencestore.Receipt{Object: object, Outcome: evidencestore.CreateOutcomeCreated}, nil
}

// The tool and the ChillServer integrate the same records to the same joules.
func TestPowerMatchesServerOperatingPower(t *testing.T) {
	directory := t.TempDir()
	config := serving.ObservationLogConfig{Directory: directory, Store: "evidence", Retention: 2 * time.Hour, Writer: nopWriter{}}
	log, err := serving.NewObservationLog(config)
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
	if err := log.SchedulerRecord(serving.SchedulerObservation{At: snapshot.At, Snapshot: &snapshot}); err != nil {
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
		event := serving.SchedulerObservation{
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
	obs := segments(t, filepath.Join(directory, "v1alpha5"), t.TempDir())
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
}

func derivedTestMeters(t *testing.T, node serving.NodeReference, source metricobserver.SourceIdentity) []serving.OperatingPowerMeter {
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
