package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	idlebaseline "github.com/lab-paper-code/chill/internal/profiling/idlebaseline"
)

type ResultStore interface {
	evidencestore.LocationReader
	evidencestore.ImmutableWriter
}

type ResultWriterOptions struct {
	Store          string
	MaxObjectBytes int64
}

type ResultWriter struct {
	store   ResultStore
	options ResultWriterOptions
}

func NewResultWriter(store ResultStore, options ResultWriterOptions) (*ResultWriter, error) {
	if store == nil {
		return nil, errors.New("model-loaded idle baseline result store is required")
	}
	if strings.TrimSpace(options.Store) == "" || strings.TrimSpace(options.Store) != options.Store {
		return nil, errors.New("idle baseline result logical store is required without surrounding whitespace")
	}
	if options.MaxObjectBytes < 1 || options.MaxObjectBytes == math.MaxInt64 {
		return nil, errors.New("maximum idle baseline result bytes are invalid")
	}
	return &ResultWriter{store: store, options: options}, nil
}

func (w *ResultWriter) Write(
	ctx context.Context,
	parent *edgev1alpha1.ModelLoadedIdleBaseline,
	result idlebaseline.Result,
) (edgev1alpha1.ProfilingRunEvidenceReference, error) {
	location, err := w.location(parent)
	if err != nil {
		return edgev1alpha1.ProfilingRunEvidenceReference{}, err
	}
	if existing, found, err := w.load(ctx, location); err != nil {
		return edgev1alpha1.ProfilingRunEvidenceReference{}, err
	} else if found {
		return existing, nil
	}
	payload, err := result.MarshalJSON()
	if err != nil {
		return edgev1alpha1.ProfilingRunEvidenceReference{}, fmt.Errorf("encode idle baseline result: %w", err)
	}
	if int64(len(payload)) > w.options.MaxObjectBytes {
		return edgev1alpha1.ProfilingRunEvidenceReference{}, fmt.Errorf(
			"idle baseline result exceeds %d-byte limit", w.options.MaxObjectBytes,
		)
	}
	identity := evidencestore.ObjectIdentity{Store: location.Store, Key: location.Key, Size: int64(len(payload))}
	receipt, err := w.store.PutImmutable(ctx, identity, bytes.NewReader(payload))
	if err != nil {
		return edgev1alpha1.ProfilingRunEvidenceReference{}, fmt.Errorf("create idle baseline result: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return edgev1alpha1.ProfilingRunEvidenceReference{}, fmt.Errorf("validate idle baseline result receipt: %w", err)
	}
	if receipt.Object != identity {
		return edgev1alpha1.ProfilingRunEvidenceReference{}, errors.New("idle baseline result writer returned an unexpected object")
	}
	if receipt.Outcome == evidencestore.CreateOutcomeExisting {
		winner, found, loadErr := w.load(ctx, location)
		if loadErr != nil {
			return edgev1alpha1.ProfilingRunEvidenceReference{}, loadErr
		}
		if !found {
			return edgev1alpha1.ProfilingRunEvidenceReference{}, errors.New("existing idle baseline result disappeared")
		}
		return winner, nil
	}
	return resultReference(identity), nil
}

func (w *ResultWriter) Load(
	ctx context.Context,
	parent *edgev1alpha1.ModelLoadedIdleBaseline,
) (edgev1alpha1.ProfilingRunEvidenceReference, bool, error) {
	location, err := w.location(parent)
	if err != nil {
		return edgev1alpha1.ProfilingRunEvidenceReference{}, false, err
	}
	return w.load(ctx, location)
}

func (w *ResultWriter) location(parent *edgev1alpha1.ModelLoadedIdleBaseline) (evidencestore.ObjectLocation, error) {
	if parent == nil || parent.UID == "" {
		return evidencestore.ObjectLocation{}, errors.New("a ModelLoadedIdleBaseline with UID is required")
	}
	location := evidencestore.ObjectLocation{
		Store: w.options.Store, Key: fmt.Sprintf("model-loaded-idle-baselines/%s/result.json", parent.UID),
	}
	if err := location.Validate(); err != nil {
		return evidencestore.ObjectLocation{}, fmt.Errorf("validate idle baseline result location: %w", err)
	}
	return location, nil
}

func (w *ResultWriter) load(
	ctx context.Context,
	location evidencestore.ObjectLocation,
) (edgev1alpha1.ProfilingRunEvidenceReference, bool, error) {
	stream, err := w.store.OpenLocation(ctx, location)
	if errors.Is(err, evidencestore.ErrNotFound) {
		return edgev1alpha1.ProfilingRunEvidenceReference{}, false, nil
	}
	if err != nil {
		return edgev1alpha1.ProfilingRunEvidenceReference{}, false, fmt.Errorf("open idle baseline result: %w", err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, w.options.MaxObjectBytes+1))
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil {
		return edgev1alpha1.ProfilingRunEvidenceReference{}, false, errors.Join(readErr, closeErr)
	}
	if int64(len(payload)) > w.options.MaxObjectBytes {
		return edgev1alpha1.ProfilingRunEvidenceReference{}, false,
			fmt.Errorf("idle baseline result exceeds %d-byte limit", w.options.MaxObjectBytes)
	}
	result, err := idlebaseline.DecodeResult(payload)
	if err != nil {
		return edgev1alpha1.ProfilingRunEvidenceReference{}, false, err
	}
	identity := evidencestore.ObjectIdentity{Store: location.Store, Key: location.Key, Size: int64(len(payload))}
	return edgev1alpha1.ProfilingRunEvidenceReference{
		Store: identity.Store, Key: identity.Key, SchemaVersion: result.SchemaVersion(), Size: identity.Size,
	}, true, nil
}

func resultReference(identity evidencestore.ObjectIdentity) edgev1alpha1.ProfilingRunEvidenceReference {
	return edgev1alpha1.ProfilingRunEvidenceReference{
		Store: identity.Store, Key: identity.Key, SchemaVersion: idlebaseline.ResultSchemaVersion, Size: identity.Size,
	}
}
