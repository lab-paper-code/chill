package state

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestPreEffectProofDoesNotChangeOperationJSON(t *testing.T) {
	result := OperationResult{Operation: Operation{ID: "prepare-proof", Kind: OperationPrepare}}
	before, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	result.EffectsNotStarted = true
	after, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("internal pre-effect proof changed evidence JSON: %s != %s", before, after)
	}
}
