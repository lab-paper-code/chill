// Package worker coordinates one already-frozen profiling measurement inside
// a Job. It is independent of Kubernetes and concrete runtime, file, and
// object-store adapters.
package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

// Runtime performs the frozen warm-up and measurement protocol. Concrete
// runtime verification and measurement signaling belong to its adapter.
type Runtime interface {
	Measure(context.Context, RuntimeRequest) (profilingrun.RuntimeResult, error)
}

type RuntimeRequest struct {
	Intent            profilingrun.Intent
	PodUID            string
	ScheduledNodeName string
}

// MetricResultReader returns the complete result produced by the separate
// MetricObserver process after the runtime measurement.
type MetricResultReader interface {
	Read(context.Context) (metricobserver.Result, error)
}

type WorkloadResultReader interface {
	Read(context.Context) (workloadobserver.Result, error)
}

type Request struct {
	Intent               profilingrun.Intent
	ExpectedIntentDigest string
	PodUID               string
	ScheduledNodeName    string
	RawLocation          evidencestore.ObjectLocation
}

// Coordinator assembles and immutably commits one raw measurement. It holds no
// state between calls and does not choose a runtime, metric source, or store.
type Coordinator struct {
	runtime  Runtime
	metrics  MetricResultReader
	workload WorkloadResultReader
	store    evidencestore.ImmutableWriter
}

func NewCoordinator(
	runtime Runtime,
	metrics MetricResultReader,
	workload WorkloadResultReader,
	store evidencestore.ImmutableWriter,
) (*Coordinator, error) {
	if runtime == nil {
		return nil, errors.New("measurement runtime is required")
	}
	if metrics == nil {
		return nil, errors.New("metric result reader is required")
	}
	if workload == nil {
		return nil, errors.New("workload result reader is required")
	}
	if store == nil {
		return nil, errors.New("immutable evidence writer is required")
	}
	return &Coordinator{runtime: runtime, metrics: metrics, workload: workload, store: store}, nil
}

func (c *Coordinator) Execute(ctx context.Context, request Request) (evidencestore.Receipt, error) {
	digest, err := request.Intent.Digest()
	if err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("%w: validate frozen intent: %w", ErrExecutionContract, err)
	}
	if request.ExpectedIntentDigest != digest {
		return evidencestore.Receipt{}, fmt.Errorf(
			"%w: mounted intent digest does not match the expected digest", ErrExecutionContract,
		)
	}
	if strings.TrimSpace(request.PodUID) == "" || strings.TrimSpace(request.PodUID) != request.PodUID {
		return evidencestore.Receipt{}, fmt.Errorf(
			"%w: Pod UID is required without surrounding whitespace", ErrExecutionContract,
		)
	}
	if strings.TrimSpace(request.ScheduledNodeName) == "" ||
		strings.TrimSpace(request.ScheduledNodeName) != request.ScheduledNodeName {
		return evidencestore.Receipt{}, fmt.Errorf(
			"%w: scheduled Node name is required without surrounding whitespace", ErrExecutionContract,
		)
	}
	if request.ScheduledNodeName != request.Intent.TargetNode.Name {
		return evidencestore.Receipt{}, fmt.Errorf(
			"%w: scheduled Node does not match the frozen target Node", ErrExecutionContract,
		)
	}
	if err := request.RawLocation.Validate(); err != nil {
		return evidencestore.Receipt{}, fmt.Errorf(
			"%w: validate raw evidence location: %w", ErrExecutionContract, err,
		)
	}

	runtimeResult, err := c.runtime.Measure(ctx, RuntimeRequest{
		Intent:            request.Intent,
		PodUID:            request.PodUID,
		ScheduledNodeName: request.ScheduledNodeName,
	})
	if err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("%w: execute runtime measurement: %w", ErrRuntimeMeasurement, err)
	}
	if err := profilingrun.ValidateRuntime(request.Intent, request.PodUID, runtimeResult); err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("%w: validate runtime measurement: %w", ErrRuntimeMeasurement, err)
	}

	metricResult, err := c.metrics.Read(ctx)
	if err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("%w: read metric observation result: %w", ErrMetricObservation, err)
	}
	workloadResult, err := c.workload.Read(ctx)
	if err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("%w: read workload observation result: %w", ErrWorkloadObservation, err)
	}
	raw := evidence.RawMeasurement{
		SchemaVersion: evidence.RawMeasurementSchemaVersion,
		IntentDigest:  digest,
		PodUID:        request.PodUID,
		Runtime:       runtimeResult,
		Metrics:       metricResult,
		Workload:      &workloadResult,
	}
	payload, err := raw.CanonicalJSON(request.Intent)
	if err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("%w: construct raw measurement: %w", ErrMetricObservation, err)
	}

	identity := identityFor(request.RawLocation, payload)
	receipt, err := c.store.PutImmutable(ctx, identity, bytes.NewReader(payload))
	if err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("%w: commit raw measurement: %w", ErrEvidenceCommit, err)
	}
	if err := receipt.Validate(); err != nil || receipt.Object != identity {
		return evidencestore.Receipt{}, fmt.Errorf(
			"%w: raw evidence writer returned an invalid receipt", ErrEvidenceCommit,
		)
	}
	return receipt, nil
}

func identityFor(location evidencestore.ObjectLocation, payload []byte) evidencestore.ObjectIdentity {
	sum := sha256.Sum256(payload)
	return evidencestore.ObjectIdentity{
		Store:  location.Store,
		Key:    location.Key,
		Digest: "sha256:" + hex.EncodeToString(sum[:]),
		Size:   int64(len(payload)),
	}
}
