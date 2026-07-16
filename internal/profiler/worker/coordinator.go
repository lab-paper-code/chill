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
	"github.com/lab-paper-code/chill/internal/powerobserver"
	"github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

// Runtime performs the frozen warm-up and measurement protocol. Concrete
// runtime verification and measurement signaling belong to its adapter.
type Runtime interface {
	Measure(context.Context, profilingrun.Intent, string) (profilingrun.RuntimeResult, error)
}

// PowerResultReader returns the complete result produced by the separate
// PowerObserver process after the runtime measurement.
type PowerResultReader interface {
	Read(context.Context) (powerobserver.Result, error)
}

type Request struct {
	Intent               profilingrun.Intent
	ExpectedIntentDigest string
	PodUID               string
	RawLocation          evidencestore.ObjectLocation
}

// Coordinator assembles and immutably commits one raw measurement. It holds no
// state between calls and does not choose a runtime, power source, or store.
type Coordinator struct {
	runtime Runtime
	power   PowerResultReader
	store   evidencestore.ImmutableWriter
}

func NewCoordinator(
	runtime Runtime,
	power PowerResultReader,
	store evidencestore.ImmutableWriter,
) (*Coordinator, error) {
	if runtime == nil {
		return nil, errors.New("measurement runtime is required")
	}
	if power == nil {
		return nil, errors.New("power result reader is required")
	}
	if store == nil {
		return nil, errors.New("immutable evidence writer is required")
	}
	return &Coordinator{runtime: runtime, power: power, store: store}, nil
}

func (c *Coordinator) Execute(ctx context.Context, request Request) (evidencestore.Receipt, error) {
	digest, err := request.Intent.Digest()
	if err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("validate frozen intent: %w", err)
	}
	if request.ExpectedIntentDigest != digest {
		return evidencestore.Receipt{}, errors.New("mounted intent digest does not match the expected digest")
	}
	if strings.TrimSpace(request.PodUID) == "" || strings.TrimSpace(request.PodUID) != request.PodUID {
		return evidencestore.Receipt{}, errors.New("Pod UID is required without surrounding whitespace")
	}
	if err := request.RawLocation.Validate(); err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("validate raw evidence location: %w", err)
	}

	runtimeResult, err := c.runtime.Measure(ctx, request.Intent, request.PodUID)
	if err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("execute runtime measurement: %w", err)
	}
	if err := profilingrun.ValidateRuntime(request.Intent, request.PodUID, runtimeResult); err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("validate runtime measurement: %w", err)
	}

	powerResult, err := c.power.Read(ctx)
	if err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("read power observation result: %w", err)
	}
	raw := evidence.RawMeasurement{
		SchemaVersion: evidence.RawMeasurementSchemaVersion,
		IntentDigest:  digest,
		PodUID:        request.PodUID,
		Runtime:       runtimeResult,
		Power:         powerResult,
	}
	payload, err := raw.CanonicalJSON(request.Intent)
	if err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("construct raw measurement: %w", err)
	}

	identity := identityFor(request.RawLocation, payload)
	receipt, err := c.store.PutImmutable(ctx, identity, bytes.NewReader(payload))
	if err != nil {
		return evidencestore.Receipt{}, fmt.Errorf("commit raw measurement: %w", err)
	}
	if err := receipt.Validate(); err != nil || receipt.Object != identity {
		return evidencestore.Receipt{}, errors.New("raw evidence writer returned an invalid receipt")
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
