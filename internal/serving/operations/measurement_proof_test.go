package operations

import (
	"strings"
	"testing"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

func TestResizeRemovedSourceNeedsRetainedActualZeroInferenceProof(t *testing.T) {
	result := servingstate.OperationResult{Operation: servingstate.Operation{Kind: servingstate.OperationResize}, Before: servingstate.OperationState{Native: map[string]native.State{"physical": {Ready: true, AcceptedRequests: 5, CompletedRequests: 5, Executions: 3}}}, Actual: servingstate.OperationState{Native: map[string]native.State{"replacement": {Ready: true}}}}
	if err := operationZeroInference(result); err == nil {
		t.Fatal("removed process disappearance invented zero-inference proof")
	}
	result.DrainNative = map[string]native.State{"physical": {Ready: true, AcceptedRequests: 5, CompletedRequests: 5, Executions: 3}}
	if err := operationZeroInference(result); err != nil {
		t.Fatal(err)
	}
	result.DrainNative["physical"] = native.State{Ready: true, AcceptedRequests: 6, CompletedRequests: 6, Executions: 4}
	if err := operationZeroInference(result); err == nil {
		t.Fatal("serving-inclusive wall cost would be double counted by replay")
	}
}

func TestIncompleteNativeObservationPreservesReasons(t *testing.T) {
	result := servingstate.OperationResult{
		Before: servingstate.OperationState{Reasons: []string{"starting-pod: native identity unavailable"}},
		Actual: servingstate.OperationState{Reasons: []string{"UnconfirmedInstance: executor recovery unknown"}},
	}
	err := operationZeroInference(result)
	if err == nil {
		t.Fatal("incomplete native observation was accepted")
	}
	for _, reason := range append(result.Before.Reasons, result.Actual.Reasons...) {
		if !strings.Contains(strings.SplitN(err.Error(), "\n", 2)[0], reason) {
			t.Fatalf("original observation reason %q was hidden: %v", reason, err)
		}
	}
}
