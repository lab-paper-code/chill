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
