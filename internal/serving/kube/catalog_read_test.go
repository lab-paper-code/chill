package kube

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
)

const catalogOccupancyPath = "cpu"

func TestCatalogOccupancyProjectsOnlyExistingSchedulerSelection(t *testing.T) {
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
		Verdict:         measurementAccepted,
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
	rows, err := evidence.OccupancySelections(selected, []serving.Profile{profile})
	if err != nil || len(rows) != 1 {
		t.Fatalf("selection projection: %+v %v", rows, err)
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
	empty, err := evidence.OccupancySelections(nil, []serving.Profile{profile})
	if err != nil || len(empty) != 0 {
		t.Fatal("unselected records appeared adopted")
	}
	selected[0].Source = "missing"
	if _, err = evidence.OccupancySelections(selected, []serving.Profile{profile}); err == nil {
		t.Fatal("missing selected provenance fabricated")
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
