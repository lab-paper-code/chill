package observation

import (
	stdruntime "runtime"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// Together with serving's blocked-recorder tests this retains the original
// cross-owner invariant: neither Runtime.mu nor requestMu spans storage waits.
func TestAssignmentStorageWaitDoesNotHoldRequestState(t *testing.T) {
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	log.mu.Lock()
	locked := true
	defer func() {
		if locked {
			log.mu.Unlock()
		}
	}()
	stored := make(chan error, 1)
	go func() {
		stored <- log.RequestEvent(serving.RequestEvent{
			RequestID: "assignment", WorkloadUID: "w", Kind: "assigned", At: observationTestHour,
			Assignment: &serving.Assignment{Instance: "physical", DispatchedAt: observationTestHour},
		})
	}()
	folded := make(chan struct{})
	go func() {
		for {
			log.requestMu.Lock()
			row := log.open["assignment"]
			reflected := row != nil && row.Instance == "physical"
			log.requestMu.Unlock()
			if reflected {
				close(folded)
				return
			}
			stdruntime.Gosched()
		}
	}()
	select {
	case <-folded:
	case <-time.After(time.Second):
		t.Fatal("assignment storage wait held requestMu")
	}
	received := make(chan error, 1)
	go func() {
		received <- log.RequestEvent(serving.RequestEvent{RequestID: "other", Kind: "received", At: observationTestHour})
	}()
	select {
	case err := <-received:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("assignment storage wait blocked unrelated request memory")
	}
	select {
	case err := <-stored:
		t.Fatalf("assignment returned before storage lock release: %v", err)
	default:
	}
	log.mu.Unlock()
	locked = false
	if err := <-stored; err != nil {
		t.Fatal(err)
	}
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 1 || rows[0].RequestID != "assignment" || rows[0].Kind != serving.RequestRowAssigned {
		t.Fatalf("assignment durability changed: %+v", rows)
	}
}
