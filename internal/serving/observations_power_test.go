package serving

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
)

var operatingTestSource = metricobserver.SourceIdentity{Kind: "edge-metrics", Endpoint: "http://meter/metrics", Metric: "wall_watts"}

type operatingPowerScenario struct {
	name                                                  string
	missingInactive, zero, missingMembership, coverageGap bool
	duplicateMeter, conflictingSample, incompleteHistory  bool
	boundedQuery, measured                                bool
}

func TestOperatingPowerSameIntervalAndActualMembership(t *testing.T) {
	scenarios := []operatingPowerScenario{
		{name: "complete", measured: true},
		{name: "missing-inactive-node", missingInactive: true},
		{name: "zero", zero: true, measured: true},
		{name: "no-membership", missingMembership: true, measured: true},
		{name: "coverage-gap", coverageGap: true},
		{name: "duplicate-meter", duplicateMeter: true},
		{name: "duplicate-sample-conflict", conflictingSample: true},
		{name: "incomplete-history", incompleteHistory: true},
		{name: "bounded-query", boundedQuery: true},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			j, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := j.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			from := time.Now().UTC().Add(time.Second)
			to := from.Add(10 * time.Second)
			meters := seedOperatingPower(t, j, from, scenario)
			limit := 20
			if scenario.boundedQuery {
				limit = 1
			}
			got, err := j.OperatingPower(from, to, meters, limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Nodes) != 2 {
				t.Fatalf("physical Node double-counted: %+v", got.Nodes)
			}
			if scenario.measured {
				assertMeasuredOperatingPower(t, got, scenario)
			} else {
				assertMissingOperatingPower(t, got, scenario)
			}
		})
	}
}

func seedOperatingPower(t *testing.T, j *Journal, from time.Time, scenario operatingPowerScenario) []OperatingPowerMeter {
	t.Helper()
	a := NodeReference{Name: "a", UID: "uid-a"}
	b := NodeReference{Name: "b", UID: "uid-b"}
	meters := []OperatingPowerMeter{{Node: a, Meter: "wall-a", Scope: "node-exclusive-wall", Source: operatingTestSource}, {Node: b, Meter: "wall-b", Scope: "node-exclusive-wall", Source: operatingTestSource}}
	// Repeated identical declarations and multiple serving instances still
	// contribute only one physical Node's power.
	meters = append(meters, meters[0])
	if scenario.duplicateMeter {
		meters = append(meters, OperatingPowerMeter{Node: a, Meter: "other-wall-a", Scope: "node-exclusive-wall"})
	}
	appendRecord := func(event SchedulerObservation) {
		t.Helper()
		if err := j.SchedulerRecord(event); err != nil {
			t.Fatal(err)
		}
	}
	if !scenario.missingMembership {
		initial := SchedulerSnapshot{At: from.Add(-time.Second), Instances: []SchedulerInstance{
			{ID: "a-1", Node: a, Ready: true, WorkloadUIDs: []string{"w"}},
			{ID: "a-2", Node: a, Ready: true, WorkloadUIDs: []string{"w", "shared"}},
			{ID: "b-stopped", Node: b, Ready: true, Stopped: true, WorkloadUIDs: []string{"w"}},
		}}
		appendRecord(SchedulerObservation{At: initial.At, Snapshot: &initial})
		transition := SchedulerSnapshot{At: from.Add(3 * time.Second), Instances: []SchedulerInstance{
			{ID: "a-preparing", Node: a, Ready: false, WorkloadUIDs: []string{"w"}},
			{ID: "a-unassigned", Node: a, Ready: true},
			{ID: "b-ready", Node: b, Ready: true, WorkloadUIDs: []string{"w"}},
		}}
		if scenario.missingInactive {
			transition.Instances[2].WorkloadUIDs = nil
		}
		appendRecord(SchedulerObservation{At: transition.At, Snapshot: &transition})
	}
	policy := powerwindow.Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: .8, MaximumGap: 10 * time.Second}
	if scenario.coverageGap {
		policy.MaximumGap = time.Second
	}
	power := func(node NodeReference, meter string, start, end int, wattsStart, wattsEnd float64) {
		t.Helper()
		if scenario.zero {
			wattsStart, wattsEnd = 0, 0
		}
		result := metricobserver.Result{Source: operatingTestSource, StartedAt: from.Add(time.Duration(start) * time.Second), EndedAt: from.Add(time.Duration(end) * time.Second), Summary: metricobserver.Summary{SourceTimestampAbsent: true}}
		result.Samples = []metricobserver.Sample{{ObservedAt: result.StartedAt, Watts: wattsStart}, {ObservedAt: result.EndedAt, Watts: wattsEnd}}
		wrongPreintegratedTotal := 99999.0
		appendRecord(SchedulerObservation{At: result.EndedAt, Node: &node, Meter: meter, PowerScope: "node-exclusive-wall", Power: &result, PowerCoverage: &policy, PowerJoules: &wrongPreintegratedTotal})
	}
	// Asynchronous original windows; A is a linear ramp, B constant.
	// A's shared boundary sample is retained twice and must be deduplicated.
	power(a, "wall-a", 0, 4, 0, 40)
	power(a, "wall-a", 4, 10, 40, 100)
	if !scenario.missingInactive {
		power(b, "wall-b", 0, 6, 20, 20)
		power(b, "wall-b", 6, 10, 20, 20)
	}
	if scenario.conflictingSample {
		power(a, "wall-a", 0, 4, 0, 41)
	}
	if scenario.incompleteHistory {
		j.windows[j.active].Incomplete = true
	}
	return meters
}

func assertMeasuredOperatingPower(t *testing.T, got OperatingPowerWindow, scenario operatingPowerScenario) {
	t.Helper()
	wantWhole, wantActive := 700.0, 185.0 // A active [0,3), B [3,10).
	if scenario.zero {
		wantWhole, wantActive = 0, 0
	}
	if got.WholePoolJoules == nil || math.Abs(*got.WholePoolJoules-wantWhole) > 1e-9 || got.WholePoolMeanWatts == nil || math.Abs(*got.WholePoolMeanWatts-wantWhole/10) > 1e-9 {
		t.Fatalf("same-window raw integral: %+v", got)
	}
	if got.Nodes[0].Samples != 3 || !got.Nodes[0].SourceTimestampAbsent {
		t.Fatalf("raw dedup/timestamp facts lost: %+v", got.Nodes[0])
	}
	if scenario.missingMembership {
		if got.ActivePoolJoules != nil || got.Complete || len(got.Reasons) == 0 {
			t.Fatalf("unknown membership fabricated: %+v", got)
		}
	} else if got.ActivePoolJoules == nil || math.Abs(*got.ActivePoolJoules-wantActive) > 1e-9 || got.ActivePoolMeanWatts == nil || math.Abs(*got.ActivePoolMeanWatts-wantActive/10) > 1e-9 || !got.Complete {
		t.Fatalf("actual membership transition integral: %+v", got)
	}
}

func assertMissingOperatingPower(t *testing.T, got OperatingPowerWindow, scenario operatingPowerScenario) {
	t.Helper()
	if got.WholePoolJoules != nil || got.WholePoolMeanWatts != nil || got.ActivePoolJoules != nil || got.Complete || len(got.Reasons) == 0 {
		t.Fatalf("partial/error history admitted as total: %+v", got)
	}
	if scenario.missingInactive && (got.Nodes[0].Joules == nil || got.Nodes[1].Joules != nil || got.Nodes[1].Samples != 0) {
		t.Fatalf("missing configured Node hidden/zero-filled: %+v", got.Nodes)
	}
	if scenario.duplicateMeter && (got.Nodes[0].Joules != nil || got.Nodes[1].Joules == nil) {
		t.Fatalf("ambiguous Node poisoned valid per-node observation: %+v", got.Nodes)
	}
	if scenario.incompleteHistory && len(got.IncompleteWindows) != 1 {
		t.Fatalf("incomplete window identity lost: %+v", got)
	}
}

func TestOperatingPowerProjectsLargeUnrelatedPayload(t *testing.T) {
	j, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := j.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	from := time.Now().UTC().Add(time.Second)
	to := from.Add(10 * time.Second)
	meters := seedOperatingPower(t, j, from, operatingPowerScenario{})
	want, err := j.OperatingPower(from, to, meters, 20)
	if err != nil {
		t.Fatal(err)
	}
	assertMeasuredOperatingPower(t, want, operatingPowerScenario{})

	path := filepath.Join(j.config.Directory, j.windows[j.active].ID, "scheduler.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(data, []byte{'\n'})
	large := json.RawMessage(`{"nested":[` + strings.TrimSuffix(strings.Repeat(`{"distribution":[1,2,3,4]},`, 16384), ",") + `]}`)
	var selected json.RawMessage
	for index := 1; index < len(lines)-1; index++ {
		var record map[string]json.RawMessage
		if err := json.Unmarshal(lines[index], &record); err != nil {
			t.Fatal(err)
		}
		var scheduler map[string]json.RawMessage
		if err := json.Unmarshal(record["scheduler"], &scheduler); err != nil {
			t.Fatal(err)
		}
		// These are valid JSON but not their unrelated scheduler field types.
		// A full SchedulerObservation decode would reject them.
		scheduler["decision"] = json.RawMessage(`{"candidates":` + string(large) + `}`)
		if raw, found := scheduler["snapshot"]; found {
			var snapshot map[string]json.RawMessage
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				t.Fatal(err)
			}
			var instances []map[string]json.RawMessage
			if err := json.Unmarshal(snapshot["instances"], &instances); err != nil {
				t.Fatal(err)
			}
			for _, instance := range instances {
				instance["profile"] = json.RawMessage(`[` + string(large) + `]`)
			}
			snapshot["instances"], err = json.Marshal(instances)
			if err != nil {
				t.Fatal(err)
			}
			scheduler["snapshot"], err = json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
		}
		record["scheduler"], err = json.Marshal(scheduler)
		if err != nil {
			t.Fatal(err)
		}
		lines[index], err = json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		selected = lines[index]
	}
	if err := os.WriteFile(path, bytes.Join(lines, []byte{'\n'}), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := j.OperatingPower(from, to, meters, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unrelated payload changed power, samples or active membership:\n got: %+v\nwant: %+v", got, want)
	}
	for _, field := range []string{"power", "powerCoverage", "snapshot"} {
		t.Run("invalid-selected-"+field, func(t *testing.T) {
			var record map[string]json.RawMessage
			if err := json.Unmarshal(selected, &record); err != nil {
				t.Fatal(err)
			}
			var scheduler map[string]json.RawMessage
			if err := json.Unmarshal(record["scheduler"], &scheduler); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "power":
				scheduler[field] = json.RawMessage(`{"samples":[{"watts":"not-a-measurement"}]}`)
			case "powerCoverage":
				scheduler[field] = json.RawMessage(`{"MaximumGap":"not-a-duration"}`)
			case "snapshot":
				scheduler[field] = json.RawMessage(`{"instances":[{"ready":"not-a-bool"}]}`)
			}
			record["scheduler"], err = json.Marshal(scheduler)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := operatingPowerRecord(j.windows[j.active].ID, raw); err == nil {
				t.Fatalf("invalid selected %s accepted", field)
			}
		})
	}
}

func TestOperatingPowerInterpolatesRealBoundarySupport(t *testing.T) {
	scenarios := []struct {
		name                string
		from, to            time.Duration
		adjacent, missing   bool
		maxGap, maxBoundary time.Duration
		joules              float64
	}{
		{name: "between-raw-points", from: 2 * time.Second, to: 8 * time.Second, maxGap: 10 * time.Second, maxBoundary: 2 * time.Second, joules: 300},
		{name: "adjacent-recorded-windows", from: 4500 * time.Millisecond, to: 5500 * time.Millisecond, adjacent: true, maxGap: 10 * time.Second, maxBoundary: 500 * time.Millisecond, joules: 50},
		{name: "original-bracketing-gap", from: 4500 * time.Millisecond, to: 5500 * time.Millisecond, adjacent: true, missing: true, maxGap: time.Second, maxBoundary: 500 * time.Millisecond},
		{name: "real-boundary-distance", from: 4500 * time.Millisecond, to: 5500 * time.Millisecond, missing: true, maxGap: 10 * time.Second, maxBoundary: time.Second},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			j, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := j.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			base := time.Now().UTC().Add(time.Second)
			node := NodeReference{Name: "ramp", UID: "ramp-uid"}
			snapshot := SchedulerSnapshot{At: base, Instances: []SchedulerInstance{{ID: "ready", Node: node, Ready: true, WorkloadUIDs: []string{"workload"}}}}
			if err := j.SchedulerRecord(SchedulerObservation{At: base, Snapshot: &snapshot}); err != nil {
				t.Fatal(err)
			}
			policy := powerwindow.Policy{MinimumCoverage: 1, MaximumGap: scenario.maxGap, MaximumBoundaryDistance: scenario.maxBoundary}
			appendPower := func(start, end int) {
				t.Helper()
				power := metricobserver.Result{Source: operatingTestSource, StartedAt: base.Add(time.Duration(start) * time.Second), EndedAt: base.Add(time.Duration(end) * time.Second)}
				power.Samples = []metricobserver.Sample{{ObservedAt: power.StartedAt, Watts: float64(start * 10)}, {ObservedAt: power.EndedAt, Watts: float64(end * 10)}}
				event := SchedulerObservation{At: power.EndedAt, Node: &node, Meter: "ramp-wall", PowerScope: "node-exclusive-wall", Power: &power, PowerCoverage: &policy}
				if err := j.SchedulerRecord(event); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.adjacent {
				appendPower(0, 4)
				appendPower(6, 10)
			} else {
				appendPower(0, 10)
			}
			if scenario.adjacent {
				const nodeIdentityDimension = "node"
				// A closer neighbor from another series must not displace valid support.
				for _, wrong := range []string{"meter", "scope", "source", "unknown-source", nodeIdentityDimension} {
					source, otherNode, meter, scope := operatingTestSource, node, "ramp-wall", "node-exclusive-wall"
					switch wrong {
					case "meter":
						meter = schedulerTestOther
					case "scope":
						scope = "shared-wall"
					case "source":
						source.Endpoint = "http://replacement/metrics"
					case "unknown-source":
						source = metricobserver.SourceIdentity{}
					case nodeIdentityDimension:
						otherNode.Name = schedulerTestOther
					}
					power := metricobserver.Result{Source: source, StartedAt: base.Add(4400 * time.Millisecond), EndedAt: base.Add(5600 * time.Millisecond), Samples: []metricobserver.Sample{{ObservedAt: base.Add(4400 * time.Millisecond), Watts: 999}, {ObservedAt: base.Add(5600 * time.Millisecond), Watts: 999}}}
					if err := j.SchedulerRecord(SchedulerObservation{At: power.EndedAt, Node: &otherNode, Meter: meter, PowerScope: scope, Power: &power, PowerCoverage: &policy}); err != nil {
						t.Fatal(err)
					}
				}
			}
			from, to := base.Add(scenario.from), base.Add(scenario.to)
			got, err := j.OperatingPower(from, to, []OperatingPowerMeter{{Node: node, Meter: "ramp-wall", Scope: "node-exclusive-wall", Source: operatingTestSource}}, 10)
			if err != nil {
				t.Fatal(err)
			}
			if !scenario.missing && got.Nodes[0].Samples != 2 {
				t.Fatalf("interpolated boundary counted as real observation: %+v", got.Nodes[0])
			}
			if scenario.missing {
				if got.WholePoolJoules != nil || got.ActivePoolJoules != nil || got.Nodes[0].Joules != nil || got.Complete {
					t.Fatalf("invalid source admitted: %+v", got)
				}
				return
			}
			if got.WholePoolJoules == nil || math.Abs(*got.WholePoolJoules-scenario.joules) > 1e-9 || got.ActivePoolJoules == nil || math.Abs(*got.ActivePoolJoules-scenario.joules) > 1e-9 || !got.Complete {
				t.Fatalf("boundary trapezoid not integrated: %+v", got)
			}
			if got.WholePoolMeanWatts == nil || math.Abs(*got.WholePoolMeanWatts-50) > 1e-9 {
				t.Fatalf("ramp mean differs from measured interpolation: %+v", got)
			}
		})
	}
}

func TestOperatingPowerClosedWindowRejectsRealGapsNotCollectorChunkVerdicts(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		fromOffset   time.Duration
		gap, failure bool
		reason       string
	}{
		{name: "closed-window-after-collector-restart"},
		{name: "real-gap-over-two-seconds", gap: true, reason: "maximum sample gap exceeds policy"},
		{name: "query-boundary-missing", fromOffset: -2 * time.Second, reason: "sample boundary distance exceeds policy"},
		{name: "source-failure-in-query", failure: true, reason: "actual meter read failed"},
		{name: "incomplete-window-closed-before-query"},
		{name: "incomplete-window-crosses-query", reason: "raw history for selected interval is incomplete"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			j, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := j.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			base := time.Unix(1000, 0).UTC()
			node := NodeReference{Name: "metered", UID: "physical-node"}
			meters := []OperatingPowerMeter{{Node: node, Meter: "wall", Scope: "node-exclusive-wall", Source: operatingTestSource}}
			policy := powerwindow.Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: .75, MaximumGap: 2 * time.Second}
			snapshot := SchedulerSnapshot{At: base.Add(-20 * time.Second), Instances: []SchedulerInstance{{ID: "ready", Node: node, Ready: true, WorkloadUIDs: []string{"workload"}}}}
			if err := j.SchedulerRecord(SchedulerObservation{At: snapshot.At, Snapshot: &snapshot}); err != nil {
				t.Fatal(err)
			}
			appendClosedPowerSeries(t, j, node, base, policy, scenario.gap, scenario.failure)
			if strings.HasPrefix(scenario.name, "incomplete-window-") {
				closed := base.Add(-time.Second)
				if scenario.name == "incomplete-window-crosses-query" {
					closed = base.Add(time.Second)
				}
				previous := ObservationWindow{SchemaVersion: observationSchema, ID: "previous-owner", StartedAt: base.Add(-time.Hour), EndsAt: base.Add(time.Hour), Duration: 2 * time.Hour, ClosedAt: &closed, Incomplete: true}
				directory := filepath.Join(j.config.Directory, previous.ID)
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
				if err := writeWindowMetadata(directory, previous); err != nil {
					t.Fatal(err)
				}
				header, err := json.Marshal(previous)
				if err != nil {
					t.Fatal(err)
				}
				for _, stream := range []string{"definitions", "requests", "management", "scheduler"} {
					if err := os.WriteFile(filepath.Join(directory, stream+".jsonl"), append(header, '\n'), 0600); err != nil {
						t.Fatal(err)
					}
				}
				j.windows = append(j.windows, previous)
				fresh := SchedulerSnapshot{At: base.Add(-500 * time.Millisecond), Instances: snapshot.Instances}
				if err := j.SchedulerRecord(SchedulerObservation{At: fresh.At, Snapshot: &fresh}); err != nil {
					t.Fatal(err)
				}
			}
			from := base.Add(scenario.fromOffset)
			to := from.Add(60 * time.Second)
			got, err := j.OperatingPower(from, to, meters, 100)
			if err != nil {
				t.Fatal(err)
			}
			if scenario.reason != "" {
				if got.Complete || got.Nodes[0].Joules != nil || got.WholePoolJoules != nil || got.ActivePoolJoules != nil ||
					!strings.Contains(strings.Join(got.Nodes[0].Reasons, " "), scenario.reason) {
					t.Fatalf("real query gap/failure hidden: %+v", got)
				}
				return
			}
			if !got.Complete || got.Nodes[0].Samples != 61 || got.WholePoolJoules == nil || math.Abs(*got.WholePoolJoules-600) > 1e-9 ||
				got.ActivePoolJoules == nil || math.Abs(*got.ActivePoolJoules-600) > 1e-9 || len(got.Nodes[0].Reasons) != 0 {
				t.Fatalf("chunk verdict or stale sample poisoned closed query: %+v", got)
			}
		})
	}
}

func TestObservationWindowOverlapUsesActualClose(t *testing.T) {
	start := time.Unix(100, 0)
	closed := start.Add(time.Minute)
	window := ObservationWindow{StartedAt: start, EndsAt: start.Add(time.Hour), ClosedAt: &closed}
	if window.overlaps(closed, closed.Add(time.Minute)) {
		t.Fatal("planned end extended a closed observation window")
	}
	if !window.overlaps(closed.Add(-time.Second), closed.Add(time.Second)) {
		t.Fatal("actual incomplete interval was hidden")
	}
	window.ClosedAt = nil
	if !window.overlaps(closed, closed.Add(time.Minute)) {
		t.Fatal("open observation window lost its planned bound")
	}
}

func appendClosedPowerSeries(
	t *testing.T, j *Journal, node NodeReference, base time.Time,
	policy powerwindow.Policy, gap, failure bool,
) {
	t.Helper()
	appendPower := func(power metricobserver.Result) {
		t.Helper()
		power.Source = operatingTestSource
		points := make([]powerwindow.Sample, len(power.Samples))
		for index, sample := range power.Samples {
			points[index] = powerwindow.Sample{ObservedAt: sample.ObservedAt, Watts: sample.Watts}
		}
		_, coverageErr := powerwindow.Integrate(powerwindow.Window{Start: power.StartedAt, End: power.EndedAt, Samples: points}, policy)
		event := SchedulerObservation{At: power.EndedAt, Node: &node, Meter: "wall", PowerScope: "node-exclusive-wall", Power: &power, PowerCoverage: &policy}
		if coverageErr != nil {
			event.PowerError = coverageErr.Error()
		}
		if err := j.SchedulerRecord(event); err != nil {
			t.Fatal(err)
		}
	}
	// A cancelled one-sample chunk precedes the requested interval by
	// eleven seconds. Neither its verdict nor its sample is support.
	staleAt := base.Add(-11 * time.Second)
	appendPower(metricobserver.Result{StartedAt: staleAt.Add(-30 * time.Millisecond), EndedAt: staleAt.Add(500 * time.Millisecond), Samples: []metricobserver.Sample{{ObservedAt: staleAt, Watts: 1000}}})
	for chunk := range 13 {
		start := base.Add(time.Duration(chunk*5) * time.Second)
		power := metricobserver.Result{StartedAt: start, EndedAt: start.Add(5250 * time.Millisecond)}
		for index := chunk * 5; index < chunk*5+5; index++ {
			if gap && index == 30 {
				continue
			}
			at := base.Add(time.Duration(index)*time.Second + 200*time.Millisecond)
			if gap && index == 31 {
				at = at.Add(5 * time.Millisecond)
			}
			power.Samples = append(power.Samples, metricobserver.Sample{ObservedAt: at, Watts: 10})
		}
		if failure && chunk == 6 {
			power.Failures = []metricobserver.Failure{{ObservedAt: start.Add(700 * time.Millisecond), Reason: metricobserver.FailureReasonSourceRead, Message: "actual meter read failed"}}
		}
		appendPower(power)
	}
}

func TestOperatingPowerProjectionPreservesNumericTimeAndCombinedFields(t *testing.T) {
	raw := []byte(fmt.Sprintf(`{
		"scheduler": {
			"snapshot":{"instances":[{"workloadUIDs":["w"],"stopped":false,"ready":true,"node":{"uid":"n","name":"node"},"profile":{"ignored":[9007199254740993]}}],"at":"2026-10-15T10:07:01.478738123+09:00"},
			"powerCoverage":{"MaximumBoundaryDistance":9007199254740993,"MaximumGap":9007199254740995,"MinimumCoverage":0.12345678901234567},
			"power":{"startedAt":"2026-10-15T01:07:01.478738123Z","endedAt":"2026-10-15T01:07:02.478738123Z","samples":[{"observedAt":"2026-10-15T01:07:01.478738123Z","watts":0.12345678901234567}],"summary":{"attempts":3}},
			"node":{"name":"node","uid":"n"},"meter":"meter","powerScope":"node-exclusive-wall"
		},
		"w\u0069ndow":"numeric-window","SCHEMAVERSION":%q
	}`, observationSchema))
	got, err := operatingPowerRecord("numeric-window", raw)
	if err != nil {
		t.Fatal(err)
	}
	var want observationRecord
	if err = json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Scheduler.Power, want.Scheduler.Power) ||
		!reflect.DeepEqual(got.Scheduler.PowerCoverage, want.Scheduler.PowerCoverage) {
		t.Fatalf("projection changed consumed numeric/time fields: got=%+v want=%+v", got.Scheduler, want.Scheduler)
	}
	member := got.Scheduler.Snapshot
	if member == nil || !member.At.Equal(want.Scheduler.Snapshot.At) ||
		len(member.Instances) != 1 || !member.Instances[0].Ready || member.Instances[0].Stopped ||
		member.Instances[0].Node != *got.Scheduler.Node || !reflect.DeepEqual(member.Instances[0].WorkloadUIDs, []string{"w"}) {
		t.Fatalf("combined snapshot/power lost membership: %+v", member)
	}
}

func TestOperatingPowerProjectionRejectsMalformedIgnoredSubtrees(t *testing.T) {
	for _, invalid := range []string{`-01`, `1e`, `["value",]`, `"bad\q"`} {
		raw := fmt.Sprintf(
			`{"schemaVersion":%q,"window":"syntax-window","scheduler":{"decision":{"ignored":%s}}}`,
			observationSchema, invalid,
		)
		if _, err := operatingPowerRecord("syntax-window", []byte(raw)); err == nil {
			t.Fatalf("malformed skipped power JSON accepted: %s", invalid)
		}
	}
}
