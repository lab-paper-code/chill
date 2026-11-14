package main

import (
	"encoding/json"
	"strings"
	"testing"

	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

// Held-out latency prediction needs a serving profile with a session input
// slope; old trial evidence has none, so the bridge refuses instead of
// predicting from a different quantity.
func TestHeldoutLatencyRequiresSessionResult(t *testing.T) {
	_, err := analyze(request{Heldout: []json.RawMessage{json.RawMessage(`{"trialID":"heldout"}`)}})
	if err == nil || !strings.Contains(err.Error(), derivation.SessionResultSchemaVersion) {
		t.Fatalf("heldout request was not refused with the session result format: %v", err)
	}
}
