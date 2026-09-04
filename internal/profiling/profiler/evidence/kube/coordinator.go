package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	corev1 "k8s.io/api/core/v1"
)

const maxTerminationReceiptBytes = 4 * 1024

// ExecutionFinalizer is the Kubernetes adapter's narrow application port.
// evidence.Finalizer satisfies it without exposing a storage implementation.
type ExecutionFinalizer interface {
	Finalize(context.Context, evidence.FinalizeRequest) (evidence.FinalizeResult, error)
}

var _ ExecutionFinalizer = (*evidence.Finalizer)(nil)

// FinalizeObservationRequest contains terminal Kubernetes facts. The exact
// RunConfig is loaded from the worker's raw measurement, not supplied again by
// the controller.
type FinalizeObservationRequest struct {
	Observation          Observation
	ReceiptContainerName string
	FinalLocation        evidencestore.ObjectLocation
	Actuation            *evidence.ActuationFacts
	FinalizedAt          time.Time
}

// Coordinator translates one observed terminal execution into the existing
// immutable evidence finalization use case. It holds no Run state.
type Coordinator struct {
	finalizer ExecutionFinalizer
}

func NewCoordinator(finalizer ExecutionFinalizer) (*Coordinator, error) {
	if finalizer == nil {
		return nil, errors.New("execution evidence finalizer is required")
	}
	return &Coordinator{finalizer: finalizer}, nil
}

func (c *Coordinator) Finalize(
	ctx context.Context,
	request FinalizeObservationRequest,
) (evidence.FinalizeResult, error) {
	terminal, err := ProjectTerminalFacts(request.Observation)
	if err != nil {
		return evidence.FinalizeResult{}, fmt.Errorf("project terminal Kubernetes facts: %w", err)
	}
	receipt, err := terminationReceipt(request.Observation.Pod, request.ReceiptContainerName)
	if err != nil {
		return evidence.FinalizeResult{}, err
	}
	if err := request.FinalLocation.Validate(); err != nil {
		return evidence.FinalizeResult{}, fmt.Errorf("validate final evidence location: %w", err)
	}
	if receipt.Object.Store != request.FinalLocation.Store {
		return evidence.FinalizeResult{}, errors.New("raw receipt and final evidence use different logical stores")
	}

	result, err := c.finalizer.Finalize(ctx, evidence.FinalizeRequest{
		RawReceipt:    receipt,
		FinalLocation: request.FinalLocation,
		Terminal:      terminal,
		Actuation:     request.Actuation,
		FinalizedAt:   request.FinalizedAt,
	})
	if err != nil {
		return evidence.FinalizeResult{}, fmt.Errorf("finalize execution evidence: %w", err)
	}
	return result, nil
}

func terminationReceipt(pod *corev1.Pod, containerName string) (evidencestore.Receipt, error) {
	if strings.TrimSpace(containerName) == "" || strings.TrimSpace(containerName) != containerName {
		return evidencestore.Receipt{}, errors.New("receipt container name is required without surrounding whitespace")
	}
	statuses := append([]corev1.ContainerStatus(nil), pod.Status.InitContainerStatuses...)
	statuses = append(statuses, pod.Status.ContainerStatuses...)
	var terminated *corev1.ContainerStateTerminated
	for i := range statuses {
		if statuses[i].Name != containerName {
			continue
		}
		if terminated != nil {
			return evidencestore.Receipt{}, fmt.Errorf("duplicate receipt container status %q", containerName)
		}
		terminated = statuses[i].State.Terminated
	}
	if terminated == nil {
		return evidencestore.Receipt{}, fmt.Errorf("receipt container %q has no terminal status", containerName)
	}
	payload := []byte(terminated.Message)
	if len(payload) == 0 {
		return evidencestore.Receipt{}, fmt.Errorf("receipt container %q has an empty termination message", containerName)
	}
	if len(payload) > maxTerminationReceiptBytes {
		return evidencestore.Receipt{}, fmt.Errorf("termination receipt exceeds %d-byte limit", maxTerminationReceiptBytes)
	}

	var receipt evidencestore.Receipt
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("decode termination receipt: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return evidencestore.Receipt{}, errors.New("termination receipt contains a trailing JSON value")
	}
	if err := receipt.Validate(); err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("validate termination receipt: %w", err)
	}
	return receipt, nil
}
