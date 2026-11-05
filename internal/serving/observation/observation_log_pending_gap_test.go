package observation

import (
	"context"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

func TestPendingPodGapResolvesOnlyItsExactExecutor(t *testing.T) {
	ctx := context.Background()
	log, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: time.Hour,
		Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close(ctx) }()
	at := time.Now().UTC()
	pending := serving.InstanceState{PodUID: "pod", Node: serving.NodeReference{Name: "node"}, ObservedAt: at}
	peer := serving.InstanceState{ID: "peer/container", PodUID: "peer", Node: serving.NodeReference{Name: "peer-node", UID: "peer-node-uid"}, ObservedAt: at}
	for _, state := range []serving.InstanceState{pending, peer} {
		if err := log.RecordRecovery(state, false); err != nil {
			t.Fatal(err)
		}
	}
	known := pending
	known.ID, known.Node.UID = "pod/container", "pending-node-uid"
	if err := log.RecordRecovery(known, true); err != nil {
		t.Fatal(err)
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	index, err := log.RawHour(ctx, observationHour(at))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, gap := range index.Gaps {
		if gap.PodUID == pending.PodUID {
			if gap.Instance != known.ID || gap.ClearAt == nil || !gap.Open {
				t.Fatalf("pending provenance was orphaned or scientifically closed at CLEAR: %+v", gap)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("discovered executor lost pending Pod provenance")
	}
	// A replacement does not inherit the old container's CLEAR.
	replacement := known
	replacement.ID = "pod/replacement"
	if err := log.RecordRecovery(replacement, false); err != nil {
		t.Fatal(err)
	}
	known.Terminated, known.ObservedAt = true, time.Now().UTC()
	known.Node.UID = "" // The exact terminal executor outlives Node API identity.
	if err := log.RecordRecovery(known, true); err != nil {
		t.Fatal(err)
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	later, err := log.RawHour(ctx, observationHour(known.ObservedAt.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	var replacementOpen, peerOpen bool
	for _, gap := range later.Gaps {
		switch gap.Instance {
		case known.ID:
			t.Fatalf("terminated or Pod-only provenance poisoned later window: %+v", gap)
		case replacement.ID:
			replacementOpen = gap.Open && gap.ClearAt == nil
		case peer.ID:
			peerOpen = gap.Open && gap.ClearAt == nil
		}
		if gap.PodUID == pending.PodUID && gap.Instance == "" {
			t.Fatalf("orphan Pod-only influence remains: %+v", gap)
		}
	}
	if !replacementOpen || !peerOpen {
		t.Fatal("termination cleared a different executor's uncertainty")
	}
}
