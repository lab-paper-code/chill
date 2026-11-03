package kube

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
)

const catalogOccupancyPath = "cpu"

func TestCatalogOccupancyProjectsOnlyExistingSchedulerSelection(t *testing.T) {
	const missingSource = "missing"
	profile := serving.Profile{ID: "current"}
	profile.Condition.Artifact.ExecutionPath = catalogOccupancyPath
	identity := evidencestore.ObjectIdentity{
		Store: "evidence",
		Key:   DispatchOccupancyPrefix + "node/current.json",
		Size:  1234,
	}
	record := &DispatchOccupancyRecord{
		Node:            serving.NodeReference{Name: "node", UID: "node-uid"},
		PowerMode:       "fixed",
		Model:           "model",
		RuntimeImage:    "image",
		ProfileID:       "current",
		Batch:           2,
		PerRequestNanos: -5,
		Verdict:         servingstate.MeasurementAccepted,
	}
	stale := *record
	stale.ProfileID = "retired"
	insufficient := *record
	insufficient.Verdict = "Insufficient"
	evidence := &CostEvidence{occupancy: map[evidencestore.ObjectIdentity]*DispatchOccupancyRecord{
		identity: record,
		{Store: "evidence", Key: DispatchOccupancyPrefix + "node/stale.json", Size: 12}:        &stale,
		{Store: "evidence", Key: DispatchOccupancyPrefix + "node/insufficient.json", Size: 13}: &insufficient,
	}}
	// This is the clamped row refreshOccupancy actually passes to the scheduler.
	selected := []serving.DispatchOccupancy{
		{
			Node:         record.Node,
			PowerMode:    record.PowerMode,
			Model:        record.Model,
			RuntimeImage: record.RuntimeImage,
			PerRequest:   time.Duration(0),
			Source:       identity.Key,
		},
	}
	rows, skipped := evidence.OccupancySelections(selected, []serving.Profile{profile})
	if len(skipped) != 0 || len(rows) != 1 {
		t.Fatalf("selection projection: %+v skipped=%+v", rows, skipped)
	}
	expected := OccupancySelection{
		Node:          record.Node,
		PowerMode:     "fixed",
		Model:         "model",
		RuntimeImage:  "image",
		ExecutionPath: catalogOccupancyPath,
		ProfileID:     "current",
		Batch:         2,
		Evidence:      identity,
	}
	if !reflect.DeepEqual(rows[0], expected) {
		t.Fatalf("lost selected tuple/evidence: %+v", rows[0])
	}
	snapshot := CatalogSnapshot{Occupancy: rows}
	raw, err := json.Marshal(CatalogRead{Accepted: &snapshot})
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Accepted struct {
			Occupancy []OccupancySelection `json:"occupancy"`
		} `json:"accepted"`
	}
	if err = json.Unmarshal(raw, &wire); err != nil || !reflect.DeepEqual(wire.Accepted.Occupancy, rows) {
		t.Fatalf("private wire lost occupancy: %s %v", raw, err)
	}
	empty, skipped := evidence.OccupancySelections(nil, []serving.Profile{profile})
	if len(skipped) != 0 || len(empty) != 0 {
		t.Fatal("unselected records appeared adopted")
	}
	selected[0].RuntimeImage = "new-image"
	selected[0].PerRequest = time.Second
	selected[0].ProfileID = "another-internal-id"
	rows, skipped = evidence.OccupancySelections(selected, []serving.Profile{profile})
	if len(rows) != 1 || len(skipped) != 0 || rows[0] != expected {
		t.Fatalf("reporting reapplied image/value/profile admission: %+v %+v", rows, skipped)
	}
	for _, path := range []string{"", "record-path"} {
		record.ExecutionPath = path
		rows, skipped = evidence.OccupancySelections(selected, nil)
		if len(rows) != 1 || len(skipped) != 0 || rows[0].ExecutionPath != path || rows[0].ProfileID != record.ProfileID {
			t.Fatalf("retired profile lost evidence row: %+v %+v", rows, skipped)
		}
	}
	rows, skipped = evidence.OccupancySelections(selected, []serving.Profile{profile})
	if len(rows) != 1 || len(skipped) != 0 || rows[0].ExecutionPath != "record-path" {
		t.Fatalf("profile replaced record execution path: %+v %+v", rows, skipped)
	}
	selected[0].Source = missingSource
	rows, skipped = evidence.OccupancySelections(selected, []serving.Profile{profile})
	wantSkip := OccupancySkipped{Source: missingSource, Node: record.Node, Model: record.Model,
		PowerMode: record.PowerMode, ProfileID: selected[0].ProfileID, Reason: "noMatchingEvidence"}
	if len(rows) != 0 || !reflect.DeepEqual(skipped, []OccupancySkipped{wantSkip}) {
		t.Fatalf("missing provenance was not recorded: %+v %+v", rows, skipped)
	}
	selected[0].Source = identity.Key
	duplicate := identity
	duplicate.Store = "duplicate-store"
	evidence.occupancy[duplicate] = record
	rows, skipped = evidence.OccupancySelections(selected, []serving.Profile{profile})
	wantSkip.Source, wantSkip.Reason = identity.Key, "ambiguousEvidence(2)"
	if len(rows) != 0 || !reflect.DeepEqual(skipped, []OccupancySkipped{wantSkip}) {
		t.Fatalf("ambiguous provenance was not recorded: %+v %+v", rows, skipped)
	}
	raw, err = json.Marshal(CatalogSnapshot{OccupancySkipped: skipped})
	var projected CatalogSnapshot
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &projected); err != nil || !reflect.DeepEqual(projected.OccupancySkipped, skipped) {
		t.Fatalf("snapshot lost skipped selections: %s %v", raw, err)
	}
	// One unprojectable selection must not hide a valid sibling.
	delete(evidence.occupancy, duplicate)
	selected = append(selected, selected[0])
	selected[0].Source = missingSource
	rows, skipped = evidence.OccupancySelections(selected, nil)
	if len(rows) != 1 || len(skipped) != 1 || skipped[0].Reason != "noMatchingEvidence" {
		t.Fatalf("partial projection lost valid sibling: %+v %+v", rows, skipped)
	}
}

func TestCatalogReadRetainsResolvedOccupancyAndSignedRollingFacts(t *testing.T) {
	at := time.Now().UTC()
	window := serving.OccupancyWindow{Start: at.Add(-120 * time.Second), End: at}
	selected := []serving.DispatchOccupancy{
		{ProfileID: "measured", ExecutionPath: "cpu", Batch: 1,
			Provenance: &serving.DispatchOccupancyProvenance{Source: "measured", Pairs: 100, Window: &window, UpdatedAt: at}},
		{ProfileID: "legacy", Batch: 2, Source: "retained-dom.json", PerRequest: 270625,
			Provenance: &serving.DispatchOccupancyProvenance{Source: "dom", Value: 270625}},
		{ProfileID: "default", Batch: 4, PerRequest: 2000000,
			Provenance: &serving.DispatchOccupancyProvenance{Source: "default", Value: 2000000}},
	}
	facts := []serving.OccupancyEstimate{{Profile: serving.OccupancyProfile{ProfileID: "measured"},
		Pairs: 100, Window: window, Statistics: &serving.OccupancyStatistics{Median: -25, Mean: -10}}}
	raw, err := json.Marshal(CatalogRead{Accepted: &CatalogSnapshot{DispatchOccupancy: selected, OccupancyEstimates: facts}})
	if err != nil {
		t.Fatal(err)
	}
	var read CatalogRead
	if err := json.Unmarshal(raw, &read); err != nil {
		t.Fatal(err)
	}
	if read.Accepted == nil || len(read.DispatchOccupancy) != 0 || len(read.OccupancyEstimates) != 0 ||
		!reflect.DeepEqual(read.Accepted.DispatchOccupancy, selected) ||
		!reflect.DeepEqual(read.Accepted.OccupancyEstimates, facts) {
		t.Fatalf("resolved source or signed facts lost/mixed into reporting: %s", raw)
	}
}
