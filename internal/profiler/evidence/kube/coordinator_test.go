package kube

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiler/evidence"
	corev1 "k8s.io/api/core/v1"
)

func TestCoordinatorProjectsReceiptAndTerminalFacts(t *testing.T) {
	finalizer := &recordingFinalizer{result: evidence.FinalizeResult{
		Reference: evidencestore.ObjectIdentity{
			Store: "chill-evidence", Key: "runs/run-uid/execution.json",
			Digest: "sha256:" + strings.Repeat("c", 64), Size: 100,
		},
	}}
	coordinator, err := NewCoordinator(finalizer)
	if err != nil {
		t.Fatal(err)
	}
	request := validFinalizeObservationRequest(t)
	result, err := coordinator.Finalize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reference != finalizer.result.Reference || finalizer.calls != 1 {
		t.Fatal("coordinator did not return exactly one finalizer result")
	}
	if finalizer.request.RawReceipt.Object.Key != "runs/run-uid/raw.json" ||
		finalizer.request.Terminal.Pod.UID != "pod-uid" ||
		finalizer.request.Terminal.Node.UID != "node-uid" {
		t.Fatalf("coordinator lost receipt or Kubernetes identity: %#v", finalizer.request)
	}
	request.CandidateReportBytes[0] = 'x'
	if finalizer.request.CandidateReportBytes[0] == 'x' {
		t.Fatal("coordinator retained caller-owned candidate report bytes")
	}
}

func TestCoordinatorFailsBeforeFinalizerOnInvalidHandoff(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*FinalizeObservationRequest)
		match  string
	}{
		{
			name: "nonterminal Kubernetes observation",
			mutate: func(request *FinalizeObservationRequest) {
				request.Observation.Job.Status.Conditions = nil
			},
			match: "project terminal Kubernetes facts",
		},
		{
			name: "missing receipt container",
			mutate: func(request *FinalizeObservationRequest) {
				request.ReceiptContainerName = "other"
			},
			match: "has no terminal status",
		},
		{
			name: "unknown receipt field",
			mutate: func(request *FinalizeObservationRequest) {
				setTerminationMessage(request.Observation.Pod, `{"schemaVersion":"x","unknown":true}`)
			},
			match: "unknown field",
		},
		{
			name: "trailing receipt value",
			mutate: func(request *FinalizeObservationRequest) {
				setTerminationMessage(request.Observation.Pod, validReceiptJSON(t)+` {}`)
			},
			match: "trailing JSON value",
		},
		{
			name: "oversized receipt",
			mutate: func(request *FinalizeObservationRequest) {
				setTerminationMessage(request.Observation.Pod, strings.Repeat("x", maxTerminationReceiptBytes+1))
			},
			match: "exceeds",
		},
		{
			name: "store redirection",
			mutate: func(request *FinalizeObservationRequest) {
				request.FinalLocation.Store = "other-store"
			},
			match: "different logical stores",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finalizer := &recordingFinalizer{}
			coordinator, err := NewCoordinator(finalizer)
			if err != nil {
				t.Fatal(err)
			}
			request := validFinalizeObservationRequest(t)
			test.mutate(&request)
			_, err = coordinator.Finalize(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("expected error containing %q, got %v", test.match, err)
			}
			if finalizer.calls != 0 {
				t.Fatal("invalid handoff reached execution finalizer")
			}
		})
	}
}

func TestCoordinatorReturnsFinalizerFailure(t *testing.T) {
	want := errors.New("store unavailable")
	finalizer := &recordingFinalizer{err: want}
	coordinator, err := NewCoordinator(finalizer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.Finalize(context.Background(), validFinalizeObservationRequest(t))
	if !errors.Is(err, want) || finalizer.calls != 1 {
		t.Fatalf("expected wrapped finalizer error, got %v", err)
	}
}

func validFinalizeObservationRequest(t *testing.T) FinalizeObservationRequest {
	t.Helper()
	observation := validObservation()
	setTerminationMessage(observation.Pod, validReceiptJSON(t))
	return FinalizeObservationRequest{
		Observation:          observation,
		CandidateReportBytes: []byte(`{"schemaVersion":"candidate"}`),
		ReceiptContainerName: "runtime",
		FinalLocation: evidencestore.ObjectLocation{
			Store: "chill-evidence",
			Key:   "runs/run-uid/execution.json",
		},
		FinalizedAt: time.Date(2026, time.July, 16, 12, 1, 2, 0, time.UTC),
	}
}

func validReceiptJSON(t *testing.T) string {
	t.Helper()
	receipt := evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion,
		Object: evidencestore.ObjectIdentity{
			Store:  "chill-evidence",
			Key:    "runs/run-uid/raw.json",
			Digest: "sha256:" + strings.Repeat("a", 64),
			Size:   100,
		},
		Outcome: evidencestore.CreateOutcomeCreated,
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func setTerminationMessage(pod *corev1.Pod, message string) {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == "runtime" &&
			pod.Status.ContainerStatuses[i].State.Terminated != nil {
			pod.Status.ContainerStatuses[i].State.Terminated.Message = message
			return
		}
	}
}

type recordingFinalizer struct {
	calls   int
	request evidence.FinalizeRequest
	result  evidence.FinalizeResult
	err     error
}

func (f *recordingFinalizer) Finalize(
	_ context.Context,
	request evidence.FinalizeRequest,
) (evidence.FinalizeResult, error) {
	f.calls++
	f.request = request
	return f.result, f.err
}
